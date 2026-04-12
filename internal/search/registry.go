package search

import (
	"fmt"
	"sort"
	"sync"
)

// Factory constructs a Module from a ModuleConfig. Each subpackage registers
// one via Register in its init function.
type Factory func(cfg ModuleConfig) (Module, error)

var (
	mu       sync.RWMutex
	registry = map[string]Factory{}
)

// Register adds a module factory to the global registry. It panics on
// duplicate names so wiring mistakes surface at program start.
func Register(name string, factory Factory) {
	mu.Lock()
	defer mu.Unlock()
	if _, exists := registry[name]; exists {
		panic(fmt.Sprintf("search: duplicate module registration: %q", name))
	}
	registry[name] = factory
}

// Get returns the factory for the named module. Returns an error if the
// name is not registered — this is a startup-fatal condition.
func Get(name string) (Factory, error) {
	mu.RLock()
	defer mu.RUnlock()
	f, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("search: unknown module %q", name)
	}
	return f, nil
}

// List returns the sorted names of all registered modules.
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
