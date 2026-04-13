// Package v2single implements the "v2-single" bench variant — the
// default Go v2 pipeline: plan → search → fetch → answer, single
// round, no refinement.
//
// This is the variant the Phase 5 gate measures against the Python
// v1 baseline. Anything shipped in v2 must meet or exceed v1's
// composite score on the same 50 queries.
//
// Construction at factory time:
//  1. Build the answer-phase LLM client from env vars. Cascade:
//     DEEPSEEK_API_KEY → ANTHROPIC_API_KEY → OPENAI_API_KEY.
//     (R4.1: DeepSeek is now checked first so dual-DeepSeek is default
//     when DEEPSEEK_API_KEY is set.)
//  2. Optionally build a separate plan-phase client (DEEPSEEK_API_KEY).
//     Falls back to the answer client when not set.
//  3. Instantiate the standard search modules: brave and serp require
//     API keys (BRAVE_API_KEY, SERP_API_KEY) and are skipped when
//     missing; github uses GITHUB_TOKEN when set (unauthenticated
//     fallback otherwise); all other modules are keyless.
//  4. Build the full fetch chain (utls → chromedp → jina → archive →
//     tavily-if-key) with content cache, fetch concurrency=8.
//  5. Wire it all into pipeline.New with PlanModeAuto.
//
// Run() timing wraps pipeline.Run so latency reflects the
// end-to-end wall clock, not just the LLM call.
package v2single

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/odradekk/diting/internal/bench"
	"github.com/odradekk/diting/internal/bench/variants"
	"github.com/odradekk/diting/internal/bench/variants/v2shared"
	"github.com/odradekk/diting/internal/fetch"
	"github.com/odradekk/diting/internal/pipeline"
	"github.com/odradekk/diting/internal/search"
)

// Name is the registry key used by `diting bench run --variant v2-single`.
const Name = "v2-single"

func init() {
	variants.Register(Name, New)
}

// variant is the v2-single implementation. Kept unexported so
// same-package tests can construct it directly with mocks.
type variant struct {
	pipeline    *pipeline.Pipeline
	planModel   string // model name for plan-phase cost lookup (may equal answerModel)
	answerModel string // model name for answer-phase cost lookup

	// closers are called when the variant is done. We don't expose
	// a Close() method yet — the benchmark CLI has no cleanup
	// lifecycle — but we keep the handles so the runtime cost of
	// the fetch chain (chromedp + cache) is paid once per run,
	// not once per query.
	//
	//nolint:unused // reserved for future Close() plumbing
	closers []func()
	once    sync.Once
}

// New constructs a v2-single variant backed by the real LLM client,
// search registry, and fetch chain. It returns an error if the
// factory can't reach a working LLM provider — there's no point
// running a 50-query benchmark with a dead client, and failing fast
// at factory time gives the user a clean error message before any
// queries hit the network.
//
// As of Phase 5.7 Round 4.1, v2-single uses DeepSeek Chat for BOTH
// phases when DEEPSEEK_API_KEY is set (previously only the plan phase
// used it). The answer phase now also uses BuildLLMFromEnv, which
// prefers DeepSeek first. Two separate clients are constructed — one
// from BuildLLMFromEnv for the answer phase and one from
// BuildPlanLLMFromEnv for the plan phase — but both point at the same
// DeepSeek endpoint when DEEPSEEK_API_KEY is configured.
//
// The key behaviour change: AnswerMaxTokens is now also clamped to
// the answer handle's MaxOutputTokens when non-zero. Without this clamp,
// the pipeline default (24576) exceeds DeepSeek Chat's 8192 cap and
// causes HTTP 400 errors on every answer-phase call.
//
// When no DEEPSEEK_API_KEY is set, the existing ANTHROPIC/OPENAI
// fallback cascade applies for both handles unchanged.
func New() (bench.Variant, error) {
	handle, err := v2shared.BuildLLMFromEnv()
	if err != nil {
		return nil, fmt.Errorf("v2-single: %w", err)
	}

	// Optional separate plan-phase client. Returns (nil, nil) when no
	// DEEPSEEK_API_KEY is set, in which case planHandle stays nil and
	// the pipeline falls back to using the answer client for both.
	planHandle, err := v2shared.BuildPlanLLMFromEnv()
	if err != nil {
		return nil, fmt.Errorf("v2-single: build plan client: %w", err)
	}

	modules, closeModules := v2shared.BuildSearchModules()
	if len(modules) == 0 {
		return nil, fmt.Errorf("v2-single: no search modules available")
	}

	chainHandle, err := v2shared.BuildFetchChain()
	if err != nil {
		return nil, fmt.Errorf("v2-single: build fetch chain: %w", err)
	}

	cfg := pipeline.Config{
		PlanMode: pipeline.PlanModeAuto,
	}
	planModel := handle.Model

	// Clamp AnswerMaxTokens to the answer handle's provider cap (R4.1).
	// Previously only PlanMaxTokens was clamped because MaxOutputTokens
	// was only populated by BuildPlanLLMFromEnv. Now that BuildLLMFromEnv
	// also populates it for DeepSeek, we must apply the same clamp on
	// the answer side — DeepSeek Chat rejects max_tokens > 8192.
	if handle.MaxOutputTokens > 0 {
		cfg.AnswerMaxTokens = handle.MaxOutputTokens
	}

	if planHandle != nil {
		cfg.PlanClient = planHandle.Client
		planModel = planHandle.Model
		// Clamp the plan-phase max_tokens to the plan provider's
		// reported cap, if any. DeepSeek Chat caps at 8192; without
		// this clamp the pipeline default (24576) hits a 400 error.
		if planHandle.MaxOutputTokens > 0 {
			cfg.PlanMaxTokens = planHandle.MaxOutputTokens
		}
	}

	p := pipeline.New(
		modules,
		chainHandle.Chain,
		handle.Client,
		nil, // default scorer
		cfg,
		v2shared.SilentLogger(),
	)

	return &variant{
		pipeline:    p,
		planModel:   planModel,
		answerModel: handle.Model,
		closers: []func(){
			closeModules,
			chainHandle.Close,
		},
	}, nil
}

// newWithPipeline is a test constructor: accepts a pre-built
// pipeline so tests can inject mocked LLM + modules + fetcher
// without touching the real registry or fetch chain. The single
// model name is used for both plan and answer cost lookup.
func newWithPipeline(p *pipeline.Pipeline, model string) *variant {
	return &variant{pipeline: p, planModel: model, answerModel: model}
}

// Name returns the registry key.
func (v *variant) Name() string { return Name }

// Run executes one query through the full pipeline. Errors from
// the pipeline are captured in Metadata["error"] (plus partial
// DebugInfo via v2shared.ErrorResult) instead of propagated — a
// single bad query shouldn't fail the run.
func (v *variant) Run(ctx context.Context, in bench.RunInput) (bench.Result, error) {
	start := time.Now()
	result, err := v.pipeline.Run(ctx, in.Query)
	latency := time.Since(start)

	if err != nil {
		return v2shared.ErrorResult(in.ID, err, latency, v.planModel, v.answerModel), nil
	}

	return v2shared.ConvertPipelineResult(in.ID, result, latency, v.planModel, v.answerModel), nil
}

// Avoid unused-import complaints if the factory path is never
// reached in tests — these are pulled in by the production
// constructor but the test constructor doesn't touch them.
var _ = search.Module(nil)
var _ *fetch.Chain = nil
