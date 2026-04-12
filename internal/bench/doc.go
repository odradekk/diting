// Package bench implements the diting benchmark harness. It loads a YAML
// query set, executes each query through a pluggable Variant, scores the
// Results against the ground-truth annotations with a six-metric composite
// scorer, and renders a Markdown report.
//
// The canonical 50-query set lives at docs/bench/final/queries.yaml and is
// symlinked to test/bench/queries.yaml for the runner. Each query carries
// must_contain_domains, must_contain_terms, forbidden_domains, expected
// source types, and a canonical answer — all machine-checkable by the
// scorer.
//
// Variant is the single extension point. Real variants (v0-baseline,
// v2-single, v2-raw) land in Phase 5.6 once the underlying fetch, search,
// llm, and pipeline packages are in place; this package is scaffolding that
// unblocks them. Tests in this package use scripted in-memory variants, not
// real pipelines.
//
// See docs/architecture.md §12 for metric definitions and weights, and §14
// Phase 5 for the task breakdown this package implements.
package bench
