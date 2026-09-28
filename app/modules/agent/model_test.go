package agent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestValidateModel(t *testing.T) {
	for _, ok := range []string{"", "gpt-5.5", "o3", "openai/gpt-5:latest", "model@2026+x"} {
		if err := ValidateModel(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"-m", "--model=x", "gpt 5", "a\nb", ".hidden", strings.Repeat("a", MaxModelLength+1)} {
		if ValidateModel(bad) == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

// plainProvider does not implement ModelSelector.
type plainProvider struct{ fakeProvider }

func TestProviderCommandWithModel(t *testing.T) {
	codex, _ := lookupProvider("codex")
	base := codex.Command()
	if got := providerCommand(codex, ""); !slices.Equal(got, base) {
		t.Errorf("no model: %v", got)
	}
	got := providerCommand(codex, "gpt-5.5")
	if want := append(append([]string(nil), base...), "--model", "gpt-5.5"); !slices.Equal(got, want) {
		t.Errorf("with model: %v, want %v", got, want)
	}
	// Command() must not be modified by the model.
	if !slices.Equal(codex.Command(), base) {
		t.Errorf("Command() changed: %v", codex.Command())
	}
	plain := plainProvider{fakeProvider{name: "plain", command: []string{"plain", "run"}}}
	if got := providerCommand(plain, "gpt-5.5"); !slices.Equal(got, []string{"plain", "run"}) {
		t.Errorf("provider without models: %v", got)
	}
	if !ProviderSupportsModel("codex") || ProviderSupportsModel("nope") {
		t.Error("ProviderSupportsModel")
	}
}

func TestCodexModelsFromCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	if got := (codexProvider{}).Models(); got != nil {
		t.Errorf("without a cache: %v", got)
	}
	cache := `{"fetched_at":"x","models":[
		{"slug":"b-model","visibility":"list","priority":2},
		{"slug":"hidden","visibility":"hide","priority":0},
		{"slug":"a-model","visibility":"list","priority":1},
		{"slug":"-bad","visibility":"list","priority":3}]}`
	if err := os.WriteFile(filepath.Join(home, "models_cache.json"), []byte(cache), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := (codexProvider{}).Models(); !slices.Equal(got, []string{"a-model", "b-model"}) {
		t.Errorf("models = %v", got)
	}
	if err := os.WriteFile(filepath.Join(home, "models_cache.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := (codexProvider{}).Models(); got != nil {
		t.Errorf("broken cache: %v", got)
	}
	list := Providers()
	for _, p := range list {
		if p.Name == "codex" && p.ModelOption != "--model" {
			t.Errorf("codex status %+v", p)
		}
	}
}
