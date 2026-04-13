// Package v2raw implements the "v2-raw" bench variant — the Go v2
// pipeline with the answer phase skipped (PlanModeRaw).
//
// The point of this variant is to isolate how much the LLM answer
// synthesis step contributes to the composite score. If v2-raw
// comes out close to v2-single, the answer phase is adding little
// beyond what the scorer already extracts from the raw fetched
// sources — and we should look at the answer prompt or the fetched
// source ranking. If v2-raw scores poorly, the answer phase is
// doing real work (stitching citations, filtering noise, disclaiming
// gaps) that the scorer rewards.
//
// Construction is identical to v2-single except for:
//  1. PlanModeRaw instead of PlanModeAuto
//  2. Smaller answer-token budget (we never synthesize, so the
//     bigger number wouldn't be used)
package v2raw

import (
	"context"
	"fmt"
	"time"

	"github.com/odradekk/diting/internal/bench"
	"github.com/odradekk/diting/internal/bench/variants"
	"github.com/odradekk/diting/internal/bench/variants/v2shared"
	"github.com/odradekk/diting/internal/pipeline"
)

// Name is the registry key used by `diting bench run --variant v2-raw`.
const Name = "v2-raw"

func init() {
	variants.Register(Name, New)
}

// variant is the v2-raw implementation.
type variant struct {
	pipeline    *pipeline.Pipeline
	planModel   string
	answerModel string

	//nolint:unused // reserved for future Close() plumbing
	closers []func()
}

// New constructs a v2-raw variant. Shares v2shared's build helpers
// with v2-single, differing only in the pipeline.Config.PlanMode.
//
// As of Phase 5.7 Round 3.1, v2-raw also picks up the optional
// DEEPSEEK_API_KEY plan-phase split — when set, the plan phase uses
// DeepSeek Chat. The answer phase is unused in raw mode (PlanModeRaw
// short-circuits before answer synthesis), so the answerModel field
// is only consulted for cost tracking of any token usage that did
// happen before raw mode short-circuited.
func New() (bench.Variant, error) {
	handle, err := v2shared.BuildLLMFromEnv()
	if err != nil {
		return nil, fmt.Errorf("v2-raw: %w", err)
	}

	planHandle, err := v2shared.BuildPlanLLMFromEnv()
	if err != nil {
		return nil, fmt.Errorf("v2-raw: build plan client: %w", err)
	}

	modules, closeModules := v2shared.BuildSearchModules()
	if len(modules) == 0 {
		return nil, fmt.Errorf("v2-raw: no search modules available")
	}

	chainHandle, err := v2shared.BuildFetchChain()
	if err != nil {
		return nil, fmt.Errorf("v2-raw: build fetch chain: %w", err)
	}

	cfg := pipeline.Config{
		PlanMode: pipeline.PlanModeRaw,
	}
	planModel := handle.Model
	if planHandle != nil {
		cfg.PlanClient = planHandle.Client
		planModel = planHandle.Model
	}

	p := pipeline.New(
		modules,
		chainHandle.Chain,
		handle.Client,
		nil,
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
// pipeline so tests can inject mocks. Single model name is used for
// both plan and answer cost lookup.
func newWithPipeline(p *pipeline.Pipeline, model string) *variant {
	return &variant{pipeline: p, planModel: model, answerModel: model}
}

// Name returns the registry key.
func (v *variant) Name() string { return Name }

// Run executes the pipeline in raw mode. Because PlanModeRaw
// short-circuits after the fetch phase, the returned pipeline.Result
// has populated Sources but an empty Answer — v2shared.Convert
// detects that and builds citations from the fetched sources.
func (v *variant) Run(ctx context.Context, in bench.RunInput) (bench.Result, error) {
	start := time.Now()
	result, err := v.pipeline.Run(ctx, in.Query)
	latency := time.Since(start)

	if err != nil {
		return v2shared.ErrorResult(in.ID, err, latency, v.planModel, v.answerModel), nil
	}

	return v2shared.ConvertPipelineResult(in.ID, result, latency, v.planModel, v.answerModel), nil
}
