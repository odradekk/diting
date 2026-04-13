package pipeline

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/odradekk/diting/internal/fetch"
	"github.com/odradekk/diting/internal/llm"
	"github.com/odradekk/diting/internal/search"
)

// PlanMode controls how the pipeline phases are executed.
type PlanMode int

const (
	PlanModeAuto PlanMode = iota // run plan → search → fetch → answer (default)
	PlanModeShow                 // run plan only, stop before search (--plan-only)
	PlanModeRaw                  // run plan → search → fetch, skip answer (--raw)
)

// Config configures the pipeline.
type Config struct {
	MaxSourcesPerType int // per-source_type cap (default 5)
	MaxFetchedTotal   int // global cap (default 15)
	PlanMaxTokens     int // max tokens for plan LLM call (default 24576)
	AnswerMaxTokens   int // max tokens for answer LLM call (default 24576)
	PlanMode          PlanMode
	Concurrency       int // parallel search concurrency (default 4)

	// PlanClient is an OPTIONAL separate LLM client for the plan phase.
	// When set, the pipeline uses PlanClient for RunPlanPhase and the
	// main llm client (passed to New) for RunAnswerPhase. When nil
	// (default), both phases share the main client — backward-compatible
	// with all pre-Round-3 callers.
	//
	// Phase 5.7 Round 3.1 introduced this so a fast non-reasoning model
	// (DeepSeek Chat, gpt-4.1-mini) can handle the plan phase while a
	// reasoning model (MiniMax M2.7, Claude Sonnet) handles the answer.
	// MiniMax under load occasionally bloats plan-phase reasoning to
	// 10K-15K tokens, eating the wall-clock budget; a non-reasoning
	// plan model produces ~500-token plans in 2-5 seconds.
	PlanClient llm.Client
}

// planMaxTokens returns the plan-phase max_tokens budget.
//
// Default 24576 (24K) was chosen after the Phase 5.7 investigation: the
// first v2-single bench run showed 4 queries hitting "unexpected end of
// JSON input" with MiniMax M2.7 HighSpeed because the previous 8192 cap
// didn't leave enough headroom for <think> reasoning tokens + the final
// plan JSON envelope. Successful queries in the same run were already
// consuming up to ~4500 output tokens; a single bad query wanting 10k+
// of thinking then 1k of JSON would silently truncate. 24K is generous
// but MiniMax pricing on output is cheap, and truncation is catastrophic
// (the whole query fails).
func (c Config) planMaxTokens() int {
	if c.PlanMaxTokens > 0 {
		return c.PlanMaxTokens
	}
	return 24576
}

// answerMaxTokens returns the answer-phase max_tokens budget.
//
// Default 24576 (24K) mirrors planMaxTokens. Successful answer-phase
// output tokens in the Phase 5.7 run peaked at ~2600 (much smaller than
// plan), so the previous 16K was actually sufficient for answer alone.
// We bump both to 24K uniformly to keep the reasoning-model budget
// consistent — a query that hits the plan limit is likely to also need
// more answer headroom, and there's no cost reason to set them differently.
func (c Config) answerMaxTokens() int {
	if c.AnswerMaxTokens > 0 {
		return c.AnswerMaxTokens
	}
	return 24576
}

// Result is the full pipeline output.
type Result struct {
	Question string
	Plan     Plan
	Sources  []FetchedSource
	Answer   Answer
	Debug    DebugInfo
}

// DebugInfo holds token usage and timing for observability.
type DebugInfo struct {
	PlanInputTokens     int
	PlanOutputTokens    int
	PlanCacheReadTokens int
	AnswerInputTokens     int
	AnswerOutputTokens    int
	AnswerCacheReadTokens int
	TotalSearchResults  int
	SelectedSources     int
	FetchedSources      int
}

// Pipeline orchestrates the full diting search flow: plan → search → fetch → answer.
type Pipeline struct {
	modules []search.Module
	fetcher fetch.Fetcher
	llm     llm.Client // answer-phase client (and plan-phase fallback)
	planLLM llm.Client // resolved plan-phase client; equal to llm when Config.PlanClient is nil
	scorer  Scorer
	config  Config
	logger  *slog.Logger
}

// New creates a Pipeline.
//
// llmClient is the primary client used for the answer phase. The plan
// phase uses config.PlanClient if set, otherwise falls back to llmClient
// — backward compatible with all pre-Round-3 callers that passed a
// single client.
func New(
	modules []search.Module,
	fetcher fetch.Fetcher,
	llmClient llm.Client,
	scorer Scorer,
	config Config,
	logger *slog.Logger,
) *Pipeline {
	if logger == nil {
		logger = slog.Default()
	}
	if scorer == nil {
		scorer = DefaultScorer()
	}
	planLLM := config.PlanClient
	if planLLM == nil {
		planLLM = llmClient
	}
	return &Pipeline{
		modules: modules,
		fetcher: fetcher,
		llm:     llmClient,
		planLLM: planLLM,
		scorer:  scorer,
		config:  config,
		logger:  logger,
	}
}

// PipelineError wraps an internal phase error with a best-effort snapshot
// of the DebugInfo state at the moment of failure. Callers that want to
// surface token counts / fetch counts / phase reached for failed queries
// (bench runner, diagnostic CLI) can type-assert on this via errors.As.
//
// The Error() format matches the historical "pipeline: <phase>: <inner>"
// string so existing string-matching callers are unaffected.
type PipelineError struct {
	// Phase is one of "system_prompt", "plan", "execute", "answer".
	// "fetch" is not currently a failure phase because the fetch layer
	// soft-fails — partial fetch is OK.
	Phase string
	Err   error
	// Debug captures whatever DebugInfo fields were populated before the
	// failure. Later-phase fields (e.g. AnswerInputTokens) will be zero
	// when the pipeline failed in an earlier phase.
	Debug DebugInfo
}

func (e *PipelineError) Error() string {
	return fmt.Sprintf("pipeline: %s: %s", e.Phase, e.Err.Error())
}

func (e *PipelineError) Unwrap() error { return e.Err }

// Run executes the full pipeline for a single question.
//
// On error, Run returns (nil, *PipelineError) where the PipelineError
// carries a partial DebugInfo snapshot from all successful phases before
// the failure. Use errors.As to extract it; plain error handling (string
// matching, errors.Is(...)) continues to work via Error() + Unwrap().
func (p *Pipeline) Run(ctx context.Context, question string) (*Result, error) {
	// Build system prompt with available modules.
	systemPrompt, err := p.buildSystemPrompt()
	if err != nil {
		return nil, &PipelineError{Phase: "system_prompt", Err: err}
	}
	conv := NewConversation(systemPrompt)

	// debug accumulates DebugInfo as phases complete. On any phase error
	// we attach this snapshot to the returned PipelineError so the
	// caller can see how far we got + the token counts we consumed.
	var debug DebugInfo

	// --- Phase 1: Plan ---
	// Uses p.planLLM, which is config.PlanClient when set, otherwise
	// falls back to p.llm. See Config.PlanClient docs for rationale.
	planResult, err := RunPlanPhase(ctx, p.planLLM, conv, question, p.config.planMaxTokens())
	if err != nil {
		return nil, &PipelineError{Phase: "plan", Err: err, Debug: debug}
	}
	debug.PlanInputTokens = planResult.InputTokens
	debug.PlanOutputTokens = planResult.OutputTokens
	debug.PlanCacheReadTokens = planResult.CacheReadTokens

	p.logger.Info("plan phase complete",
		"queries", planResult.Plan.TotalQueries(),
		"input_tokens", planResult.InputTokens,
		"output_tokens", planResult.OutputTokens,
	)
	// At debug level, dump the raw LLM response so --debug users can
	// inspect exactly what came back (caught many MiniMax/reasoning-model
	// issues in Phase 3). Preview is capped to keep logs readable.
	p.logger.Debug("plan: raw response",
		"content_preview", preview(planResult.RawContent, 200),
		"content_length", len(planResult.RawContent),
	)

	// --plan-only short circuit.
	if p.config.PlanMode == PlanModeShow {
		return &Result{
			Question: question,
			Plan:     planResult.Plan,
			Debug:    debug,
		}, nil
	}

	// --- Phase 2: Execute (search) ---
	execResult, err := RunExecutePhase(ctx, planResult.Plan, p.modules, p.scorer, question, ExecuteConfig{
		MaxSourcesPerType: p.config.MaxSourcesPerType,
		MaxFetchedTotal:   p.config.MaxFetchedTotal,
		Concurrency:       p.config.Concurrency,
		Logger:            p.logger,
	})
	if err != nil {
		return nil, &PipelineError{Phase: "execute", Err: err, Debug: debug}
	}
	debug.TotalSearchResults = len(execResult.AllResults)
	debug.SelectedSources = len(execResult.Selected)

	p.logger.Info("execute phase complete",
		"total_results", len(execResult.AllResults),
		"selected", len(execResult.Selected),
	)

	// --- Phase 3: Fetch selected URLs ---
	urls := make([]string, len(execResult.Selected))
	for i, s := range execResult.Selected {
		urls[i] = s.URL
	}

	var fetchResults []*fetch.Result
	if p.fetcher != nil && len(urls) > 0 {
		fetchResults, err = p.fetcher.FetchMany(ctx, urls)
		if err != nil {
			p.logger.Warn("fetch partial failure", "error", err)
			// Continue with whatever we got — partial success is OK.
		}
	}

	// Build FetchedSource list.
	sources := make([]FetchedSource, len(execResult.Selected))
	for i, s := range execResult.Selected {
		sources[i] = FetchedSource{
			ID:     i + 1,
			Result: s,
		}
		if i < len(fetchResults) && fetchResults[i] != nil {
			sources[i].Fetched = fetchResults[i]
		}
	}

	fetchedCount := 0
	for _, s := range sources {
		if s.Fetched != nil {
			fetchedCount++
		}
	}
	debug.FetchedSources = fetchedCount

	p.logger.Info("fetch phase complete",
		"requested", len(urls),
		"fetched", fetchedCount,
	)

	// --raw short circuit: return sources without calling the answer LLM.
	if p.config.PlanMode == PlanModeRaw {
		return &Result{
			Question: question,
			Plan:     planResult.Plan,
			Sources:  sources,
			Debug:    debug,
		}, nil
	}

	// --- Phase 4: Answer ---
	answerResult, err := RunAnswerPhase(ctx, p.llm, conv, planResult.RawContent, sources, p.config.answerMaxTokens())
	if err != nil {
		return nil, &PipelineError{Phase: "answer", Err: err, Debug: debug}
	}

	p.logger.Info("answer phase complete",
		"confidence", answerResult.Answer.Confidence,
		"citations", len(answerResult.Answer.Citations),
		"input_tokens", answerResult.InputTokens,
		"output_tokens", answerResult.OutputTokens,
	)
	p.logger.Debug("answer: raw response",
		"content_preview", preview(answerResult.RawContent, 200),
		"content_length", len(answerResult.RawContent),
	)

	debug.AnswerInputTokens = answerResult.InputTokens
	debug.AnswerOutputTokens = answerResult.OutputTokens
	debug.AnswerCacheReadTokens = answerResult.CacheReadTokens

	return &Result{
		Question: question,
		Plan:     planResult.Plan,
		Sources:  sources,
		Answer:   answerResult.Answer,
		Debug:    debug,
	}, nil
}

func (p *Pipeline) buildSystemPrompt() (string, error) {
	var sourceTypes []string
	stSet := make(map[search.SourceType]bool)
	var moduleDescs []string

	for _, m := range p.modules {
		man := m.Manifest()
		if !stSet[man.SourceType] {
			sourceTypes = append(sourceTypes, string(man.SourceType))
			stSet[man.SourceType] = true
		}
		moduleDescs = append(moduleDescs, fmt.Sprintf("- %s (%s, %s): %s", man.Name, man.SourceType, man.CostTier, man.Scope))
	}

	return RenderSystem(SystemPromptData{
		SourceTypes: joinStrings(sourceTypes),
		Modules:     joinLines(moduleDescs),
	})
}

func joinStrings(ss []string) string {
	result := ""
	for i, s := range ss {
		if i > 0 {
			result += ", "
		}
		result += s
	}
	return result
}

func joinLines(ss []string) string {
	result := ""
	for i, s := range ss {
		if i > 0 {
			result += "\n"
		}
		result += s
	}
	return result
}

// preview returns the first n characters of s, appending a "…" marker if
// the string was truncated. Used for debug log previews so that --debug
// can inspect raw LLM responses without spamming the terminal with 30 KB
// reasoning-model outputs.
func preview(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
