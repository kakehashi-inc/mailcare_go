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
	if got := providerCommand(codex, "", ""); !slices.Equal(got, base) {
		t.Errorf("no model: %v", got)
	}
	got := providerCommand(codex, "gpt-5.5", "")
	if want := append(append([]string(nil), base...), "--model", "gpt-5.5"); !slices.Equal(got, want) {
		t.Errorf("with model: %v, want %v", got, want)
	}
	got = providerCommand(codex, "gpt-5.5", "high")
	if want := append(append([]string(nil), base...), "--model", "gpt-5.5", "-c", `model_reasoning_effort="high"`); !slices.Equal(got, want) {
		t.Errorf("with model and reasoning: %v, want %v", got, want)
	}
	got = providerCommand(codex, "", "low")
	if want := append(append([]string(nil), base...), "-c", `model_reasoning_effort="low"`); !slices.Equal(got, want) {
		t.Errorf("with reasoning only: %v, want %v", got, want)
	}
	// Command() must not be modified by the model.
	if !slices.Equal(codex.Command(), base) {
		t.Errorf("Command() changed: %v", codex.Command())
	}
	plain := plainProvider{fakeProvider{name: "plain", command: []string{"plain", "run"}}}
	if got := providerCommand(plain, "gpt-5.5", "high"); !slices.Equal(got, []string{"plain", "run"}) {
		t.Errorf("provider without models and reasoning: %v", got)
	}
	if !ProviderSupportsModel("codex") || ProviderSupportsModel("nope") {
		t.Error("ProviderSupportsModel")
	}
	if !ProviderSupportsReasoning("codex") || ProviderSupportsReasoning("nope") {
		t.Error("ProviderSupportsReasoning")
	}
}

func TestCodexModelsFromCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	if got := (codexProvider{}).Models(); got != nil {
		t.Errorf("without a cache: %v", got)
	}
	cache := `{"fetched_at":"x","models":[
		{"slug":"b-model","visibility":"list","priority":2,"supported_reasoning_levels":[{"effort":"low"},{"effort":"high"}]},
		{"slug":"hidden","visibility":"hide","priority":0,"supported_reasoning_levels":[{"effort":"medium"},{"effort":"Bad Level"}]},
		{"slug":"a-model","visibility":"list","priority":1},
		{"slug":"-bad","visibility":"list","priority":3,"supported_reasoning_levels":[{"effort":"low"}]}]}`
	if err := os.WriteFile(filepath.Join(home, "models_cache.json"), []byte(cache), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := (codexProvider{}).Models(); !slices.Equal(got, []string{"a-model", "b-model"}) {
		t.Errorf("models = %v", got)
	}
	// Reasoning levels: every valid model with levels, hidden ones included.
	levels := (codexProvider{}).ReasoningLevels()
	if len(levels) != 2 || !slices.Equal(levels["b-model"], []string{"low", "high"}) || !slices.Equal(levels["hidden"], []string{"medium"}) {
		t.Errorf("reasoning levels = %v", levels)
	}
	for _, c := range []struct {
		model, level string
		ok           bool
	}{
		{"b-model", "", true},
		{"b-model", "high", true},
		{"b-model", "medium", false}, // the model does not accept it
		{"hidden", "medium", true},
		{"a-model", "anything", true}, // levels unknown for the model
		{"not-listed", "anything", true},
		{"", "medium", true}, // CLI default model: any known level
		{"", "extreme", false},
		{"b-model", "HIGH", false}, // shape
	} {
		err := CheckReasoningEffort("codex", c.model, c.level)
		if (err == nil) != c.ok {
			t.Errorf("CheckReasoningEffort(%q, %q) = %v, want ok=%v", c.model, c.level, err, c.ok)
		}
	}
	if err := os.WriteFile(filepath.Join(home, "models_cache.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := (codexProvider{}).Models(); got != nil {
		t.Errorf("broken cache: %v", got)
	}
	// Without known levels only the shape is checked.
	if err := CheckReasoningEffort("codex", "", "whatever"); err != nil {
		t.Errorf("unknown levels: %v", err)
	}
	list := Providers()
	for _, p := range list {
		if p.Name == "codex" && (p.ModelOption != "--model" || p.ReasoningOption != "-c model_reasoning_effort" || p.ReasoningLevels == nil) {
			t.Errorf("codex status %+v", p)
		}
	}
}

func TestReasoningEffortValidation(t *testing.T) {
	for _, ok := range []string{"", "low", "xhigh"} {
		if err := ValidateReasoningEffort(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"-c", "high\"", "x=1", "Low", "a b", strings.Repeat("a", MaxReasoningEffortLength+1)} {
		if ValidateReasoningEffort(bad) == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
	registerFake(t, fakeProvider{name: "plain", command: []string{"plain"}})
	if err := CheckReasoningEffort("plain", "", "low"); err == nil {
		t.Error("a provider without reasoning support must refuse a level")
	}
	if err := CheckReasoningEffort("plain", "", ""); err != nil {
		t.Errorf("an empty level is always accepted: %v", err)
	}
	if err := CheckReasoningEffort("nope", "", "low"); err == nil {
		t.Error("an unknown provider must be refused")
	}
}

func TestCodexParseUsage(t *testing.T) {
	raw := "Reading prompt from stdin...\nOpenAI Codex v0.156.1\n--------\nworkdir: /tmp/x\nmodel: gpt-6-astra\n" +
		"provider: openai\nreasoning effort: xhigh\nsession id: 1\n--------\nuser\nPROMPT\n"
	answer := "codex\nreading\nexec\n/bin/bash -c 'cat evidence/k.txt'\n succeeded\nexec\n/bin/bash -c ls\n" +
		"tokens used\n54,394\n" + ReportBegin + "\n"
	u := (codexProvider{}).ParseUsage(raw+answer, answer)
	if u.Model != "gpt-6-astra" || u.ReasoningEffort != "xhigh" || !u.TokensUsed.Valid || u.TokensUsed.Int64 != 54394 ||
		!u.CommandCount.Valid || u.CommandCount.Int64 != 2 {
		t.Errorf("usage = %+v", u)
	}
	// The total on the same line; no command.
	u = (codexProvider{}).ParseUsage(raw+"tokens used: 1,234\n", "tokens used: 1,234\n")
	if !u.TokensUsed.Valid || u.TokensUsed.Int64 != 1234 || !u.CommandCount.Valid || u.CommandCount.Int64 != 0 {
		t.Errorf("same-line usage = %+v", u)
	}
	// Not a codex transcript: nothing is known.
	u = (codexProvider{}).ParseUsage("Error: HTTP 429\n", "Error: HTTP 429\n")
	if u.Model != "" || u.ReasoningEffort != "" || u.TokensUsed.Valid || u.CommandCount.Valid {
		t.Errorf("unknown output = %+v", u)
	}
}
