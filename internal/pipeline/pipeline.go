package pipeline

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/odradekk/diting/internal/fetch"
	"github.com/odradekk/diting/internal/llm"
	"github.com/odradekk/diting/internal/search"
)

// PlanMode controls how the plan phase is exposed.
type PlanMode int

const (
	PlanModeAuto    PlanMode = iota // run plan + answer silently
	PlanModeShow                    // print plan and stop (--plan-only)
)

// Config configures the pipeline.
type Config struct {
	MaxSourcesPerType int  // per-source_type cap (default 5)
	MaxFetchedTotal   int  // global cap (default 15)
	PlanMaxTokens     int  // max tokens for plan LLM call (default 2048)
	AnswerMaxTokens   int  // max tokens for answer LLM call (default 4096)
	PlanMode          PlanMode
	Concurrency       int  // parallel search concurrency (default 4)
}

func (c Config) planMaxTokens() int {
	if c.PlanMaxTokens > 0 {
		return c.PlanMaxTokens
	}
	return 8192 // reasoning models (MiniMax, DeepSeek) need headroom for <think> tokens
}

func (c Config) answerMaxTokens() int {
	if c.AnswerMaxTokens > 0 {
		return c.AnswerMaxTokens
	}
	return 16384 // reasoning models need headroom for <think> tokens
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
	llm     llm.Client
	scorer  Scorer
	config  Config
	logger  *slog.Logger
}

// New creates a Pipeline.
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
	return &Pipeline{
		modules: modules,
		fetcher: fetcher,
		llm:     llmClient,
		scorer:  scorer,
		config:  config,
		logger:  logger,
	}
}

// Run executes the full pipeline for a single question.
func (p *Pipeline) Run(ctx context.Context, question string) (*Result, error) {
	// Build system prompt with available modules.
	systemPrompt, err := p.buildSystemPrompt()
	if err != nil {
		return nil, fmt.Errorf("pipeline: system prompt: %w", err)
	}
	conv := NewConversation(systemPrompt)

	// --- Phase 1: Plan ---
	planResult, err := RunPlanPhase(ctx, p.llm, conv, question, p.config.planMaxTokens())
	if err != nil {
		return nil, fmt.Errorf("pipeline: plan: %w", err)
	}

	p.logger.Info("plan phase complete",
		"queries", planResult.Plan.TotalQueries(),
		"input_tokens", planResult.InputTokens,
		"output_tokens", planResult.OutputTokens,
	)

	// --plan-only short circuit.
	if p.config.PlanMode == PlanModeShow {
		return &Result{
			Question: question,
			Plan:     planResult.Plan,
			Debug: DebugInfo{
				PlanInputTokens:  planResult.InputTokens,
				PlanOutputTokens: planResult.OutputTokens,
				PlanCacheReadTokens: planResult.CacheReadTokens,
			},
		}, nil
	}

	// --- Phase 2: Execute (search) ---
	execResult, err := RunExecutePhase(ctx, planResult.Plan, p.modules, p.scorer, question, ExecuteConfig{
		MaxSourcesPerType: p.config.MaxSourcesPerType,
		MaxFetchedTotal:   p.config.MaxFetchedTotal,
		Concurrency:       p.config.Concurrency,
	})
	if err != nil {
		return nil, fmt.Errorf("pipeline: execute: %w", err)
	}

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

	p.logger.Info("fetch phase complete",
		"requested", len(urls),
		"fetched", fetchedCount,
	)

	// --- Phase 4: Answer ---
	answerResult, err := RunAnswerPhase(ctx, p.llm, conv, planResult.RawContent, sources, p.config.answerMaxTokens())
	if err != nil {
		return nil, fmt.Errorf("pipeline: answer: %w", err)
	}

	p.logger.Info("answer phase complete",
		"confidence", answerResult.Answer.Confidence,
		"citations", len(answerResult.Answer.Citations),
		"input_tokens", answerResult.InputTokens,
		"output_tokens", answerResult.OutputTokens,
	)

	return &Result{
		Question: question,
		Plan:     planResult.Plan,
		Sources:  sources,
		Answer:   answerResult.Answer,
		Debug: DebugInfo{
			PlanInputTokens:       planResult.InputTokens,
			PlanOutputTokens:      planResult.OutputTokens,
			PlanCacheReadTokens:   planResult.CacheReadTokens,
			AnswerInputTokens:     answerResult.InputTokens,
			AnswerOutputTokens:    answerResult.OutputTokens,
			AnswerCacheReadTokens: answerResult.CacheReadTokens,
			TotalSearchResults:    len(execResult.AllResults),
			SelectedSources:       len(execResult.Selected),
			FetchedSources:        fetchedCount,
		},
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
