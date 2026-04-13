package llm

import (
	"fmt"
	"sort"
	"sync"
)

// Factory constructs a Client from a ProviderConfig.
type Factory func(cfg ProviderConfig) (Client, error)

var (
	mu       sync.RWMutex
	registry = map[string]Factory{}
)

// Register adds a provider factory to the global registry. It panics on
// duplicate names so wiring mistakes surface at program start.
func Register(name string, factory Factory) {
	mu.Lock()
	defer mu.Unlock()
	if _, exists := registry[name]; exists {
		panic(fmt.Sprintf("llm: duplicate provider registration: %q", name))
	}
	registry[name] = factory
}

// Get returns the factory for the named provider.
func Get(name string) (Factory, error) {
	mu.RLock()
	defer mu.RUnlock()
	f, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("llm: unknown provider %q", name)
	}
	return f, nil
}

// List returns the sorted names of all registered providers.
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
