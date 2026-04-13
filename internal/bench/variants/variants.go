// Package variants is the registry for bench Variant implementations.
//
// Phase 4.10 ships the registry scaffold so the `diting bench` CLI can
// resolve variants by name without hardcoding a switch statement.
// Phase 5.6 adds the first real variants (v0-baseline, v2-single,
// v2-raw) — each lives in internal/bench/variants/<name>/ and
// registers itself via an init function.
//
// The registry is deliberately NOT inside internal/bench itself: the
// bench library layer is pure (no imports of variant implementations
// that would drag in search/fetch/llm clients). Keeping variants in a
// sibling subpackage lets the CLI blank-import them without polluting
// the library.
package variants

import (
	"fmt"
	"sort"
	"sync"

	"github.com/odradekk/diting/internal/bench"
)

// Factory constructs a bench.Variant. Each concrete variant package
// exposes one constructor and registers it from its own init function.
//
// For Phase 4.10 the factory takes no arguments: real variants that
// need an LLM client / fetch chain / module set can read those from
// their own configuration when Run() is called, or Phase 5.6 can
// widen the factory signature if the current shape proves too
// restrictive.
type Factory func() (bench.Variant, error)

var (
	mu       sync.RWMutex
	registry = map[string]Factory{}
)

// Register adds a variant factory to the global registry. Panics on
// duplicate names so wiring mistakes surface at program start, which
// matches the behaviour of search.Register and llm.Register.
func Register(name string, factory Factory) {
	mu.Lock()
	defer mu.Unlock()
	if _, exists := registry[name]; exists {
		panic(fmt.Sprintf("bench/variants: duplicate variant registration: %q", name))
	}
	registry[name] = factory
}

// Get returns the factory for the named variant, or an error if no
// such variant has been registered.
func Get(name string) (Factory, error) {
	mu.RLock()
	defer mu.RUnlock()
	f, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("bench: unknown variant %q", name)
	}
	return f, nil
}

// List returns the sorted names of every registered variant.
func List() []string {
	mu.RLock()
	defer mu.RUnlock()
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// resetRegistry clears the registry. Test-only.
func resetRegistry() {
	mu.Lock()
	defer mu.Unlock()
	registry = map[string]Factory{}
}
