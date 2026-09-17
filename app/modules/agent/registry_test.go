package agent

import (
	"sort"
	"testing"
)

// fakeProvider is a test-only provider whose Command() is set by the test.
type fakeProvider struct {
	name    string
	command []string
}

func (f fakeProvider) Name() string      { return f.name }
func (f fakeProvider) Label() string     { return "Fake " + f.name }
func (f fakeProvider) Command() []string { return f.command }

// registerFake registers a provider for the duration of the test.
func registerFake(t *testing.T, p Provider) {
	t.Helper()
	prevList := append([]Provider(nil), providers...)
	prevMap := map[string]Provider{}
	for k, v := range providerByName {
		prevMap[k] = v
	}
	registerProvider(p)
	t.Cleanup(func() {
		providers = prevList
		providerByName = prevMap
	})
}

func TestRegistryCodexRegistered(t *testing.T) {
	if !IsValidProvider("codex") {
		t.Fatal("codex must be registered")
	}
	if IsValidProvider("") || IsValidProvider("nope") {
		t.Fatal("unknown names must be invalid")
	}
	if ProviderAvailable("nope") {
		t.Fatal("unknown provider cannot be available")
	}
	p, ok := lookupProvider("")
	if !ok || p.Name() != DefaultProvider {
		t.Fatalf("empty name must resolve to the default provider, got %v %v", p, ok)
	}
	cmd := p.Command()
	if len(cmd) == 0 || cmd[0] != "codex" {
		t.Fatalf("unexpected codex command %v", cmd)
	}
}

func TestProvidersSortedWithAvailability(t *testing.T) {
	registerFake(t, fakeProvider{name: "aaa-fake", command: []string{"definitely-not-a-real-binary-mlc"}})
	registerFake(t, fakeProvider{name: "zzz-fake", command: []string{"definitely-not-a-real-binary-mlc"}})
	list := Providers()
	names := make([]string, 0, len(list))
	for _, s := range list {
		names = append(names, s.Name)
	}
	if !sort.StringsAreSorted(names) {
		t.Fatalf("providers not in name order: %v", names)
	}
	if names[0] != "aaa-fake" || names[len(names)-1] != "zzz-fake" {
		t.Fatalf("fakes missing from listing: %v", names)
	}
	for _, s := range list {
		if s.Name == "aaa-fake" && (s.Available || s.Label != "Fake aaa-fake") {
			t.Fatalf("fake status wrong: %+v", s)
		}
	}
}

func TestRegisterProviderReplaces(t *testing.T) {
	registerFake(t, fakeProvider{name: "dup", command: []string{"one"}})
	registerFake(t, fakeProvider{name: "dup", command: []string{"two"}})
	n := 0
	for _, p := range providers {
		if p.Name() == "dup" {
			n++
			if p.Command()[0] != "two" {
				t.Fatal("later registration must win")
			}
		}
	}
	if n != 1 {
		t.Fatalf("expected one entry for dup, got %d", n)
	}
}
