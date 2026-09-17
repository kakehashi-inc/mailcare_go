package agent

import (
	"os/exec"
	"sort"
)

// Provider registry.
//
// Providers register themselves from init() in their provider_<name>.go file.
// The list is kept in name order so every listing (Web settings, CLI) is
// alphabetical without further sorting. Tests may register a fake provider
// through registerProvider; registering a name twice replaces the earlier
// entry.

var (
	providers      []Provider
	providerByName = map[string]Provider{}
)

// registerProvider adds (or replaces) a provider and keeps the list sorted.
func registerProvider(p Provider) {
	name := p.Name()
	if name == "" {
		panic("agent: provider with empty name")
	}
	if _, exists := providerByName[name]; exists {
		for i, q := range providers {
			if q.Name() == name {
				providers = append(providers[:i], providers[i+1:]...)
				break
			}
		}
	}
	providerByName[name] = p
	providers = append(providers, p)
	sort.Slice(providers, func(i, j int) bool { return providers[i].Name() < providers[j].Name() })
}

// lookupProvider resolves a provider name ("" means DefaultProvider).
func lookupProvider(name string) (Provider, bool) {
	if name == "" {
		name = DefaultProvider
	}
	p, ok := providerByName[name]
	return p, ok
}

// commandAvailable reports whether the argv's executable can be launched on
// this machine. exec.LookPath is cross-platform: on Windows it honors PATHEXT
// (.exe/.cmd/.bat); on macOS/Linux it checks the executable bit on PATH.
func commandAvailable(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	_, err := exec.LookPath(argv[0])
	return err == nil
}

// Providers lists every registered provider in name order with availability.
func Providers() []ProviderStatus {
	out := make([]ProviderStatus, 0, len(providers))
	for _, p := range providers {
		out = append(out, ProviderStatus{Name: p.Name(), Label: p.Label(), Available: commandAvailable(p.Command())})
	}
	return out
}

// IsValidProvider reports whether name is registered.
func IsValidProvider(name string) bool {
	_, ok := providerByName[name]
	return ok
}

// ProviderAvailable reports whether the provider's CLI can be launched.
func ProviderAvailable(name string) bool {
	p, ok := providerByName[name]
	return ok && commandAvailable(p.Command())
}
