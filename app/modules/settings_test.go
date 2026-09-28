package modules

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"mailcare/app/models"
)

func TestParseCheckTimes(t *testing.T) {
	got, err := ParseCheckTimes([]string{"18:30", "6:00,06:00", " 12:05 "})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := []string{"06:00", "12:05", "18:30"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	for _, bad := range []string{"24:00", "6:5", "abc", "12:60", "1200", "-1:00"} {
		if _, err := ParseCheckTimes([]string{bad}); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
	empty, err := ParseCheckTimes(nil)
	if err != nil || len(empty) != 0 || empty == nil {
		t.Errorf("empty input: got %v, %v", empty, err)
	}
}

func TestAgentModelClearedOnProviderChange(t *testing.T) {
	db := newTestDB(t)
	if err := SetAgentModel(db, "gpt-5.5"); err != nil {
		t.Fatal(err)
	}
	// Setting the same provider keeps the model.
	if err := SetAgentProvider(db, DefaultAgentProvider); err != nil {
		t.Fatal(err)
	}
	if got := ResolveAgentModel(db); got != "gpt-5.5" {
		t.Errorf("same provider: model %q", got)
	}
	// Another provider clears it (the name is not validated here).
	if err := SetAgentProvider(db, "other"); err != nil {
		t.Fatal(err)
	}
	if got := ResolveAgentModel(db); got != "" {
		t.Errorf("provider change kept model %q", got)
	}
	if SetAgentModel(db, "--bad") == nil {
		t.Error("invalid model accepted")
	}
	// A stored value that is not valid is ignored.
	if err := models.SetSetting(db, SettingAgentModel, "bad model"); err != nil {
		t.Fatal(err)
	}
	if got := ResolveAgentModel(db); got != "" {
		t.Errorf("invalid stored model resolved to %q", got)
	}
}

// writeCodexCache points CODEX_HOME at a model cache in which "m1" accepts
// low and high and "m2" accepts medium.
func writeCodexCache(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	cache := `{"models":[
		{"slug":"m1","visibility":"list","priority":1,"supported_reasoning_levels":[{"effort":"low"},{"effort":"high"}]},
		{"slug":"m2","visibility":"list","priority":2,"supported_reasoning_levels":[{"effort":"medium"}]}]}`
	if err := os.WriteFile(filepath.Join(home, "models_cache.json"), []byte(cache), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAgentReasoningEffort(t *testing.T) {
	writeCodexCache(t)
	db := newTestDB(t)
	if got := ResolveAgentReasoningEffort(db); got != "" {
		t.Fatalf("default level %q, want unset", got)
	}
	// Model unset: any level a known model accepts.
	if err := SetAgentReasoningEffort(db, "medium"); err != nil {
		t.Fatal(err)
	}
	if SetAgentReasoningEffort(db, "extreme") == nil {
		t.Error("a level no model accepts must be refused")
	}
	// A model that does not accept the saved level is refused; nothing is
	// saved.
	if SetAgentModel(db, "m1") == nil {
		t.Error("m1 does not accept medium: the model change must be refused")
	}
	if got := ResolveAgentModel(db); got != "" {
		t.Errorf("refused model saved: %q", got)
	}
	if err := SetAgentModel(db, "m2"); err != nil {
		t.Fatal(err)
	}
	if SetAgentReasoningEffort(db, "high") == nil {
		t.Error("m2 does not accept high")
	}
	// Changing both at once is checked as a whole.
	model, level := "m1", "high"
	if err := SaveAgentSettings(db, nil, &model, &level); err != nil {
		t.Fatal(err)
	}
	if ResolveAgentModel(db) != "m1" || ResolveAgentReasoningEffort(db) != "high" {
		t.Errorf("combined save: %q %q", ResolveAgentModel(db), ResolveAgentReasoningEffort(db))
	}
	bad := "medium"
	if SaveAgentSettings(db, nil, nil, &bad) == nil || ResolveAgentReasoningEffort(db) != "high" {
		t.Error("a refused combination must save nothing")
	}
	// A model the cache does not list: only the shape is checked.
	other := "custom-model"
	if err := SaveAgentSettings(db, nil, &other, nil); err != nil {
		t.Errorf("unlisted model: %v", err)
	}
	// "" restores the CLI setting.
	if err := SetAgentReasoningEffort(db, ""); err != nil || ResolveAgentReasoningEffort(db) != "" {
		t.Errorf("clear level: %v %q", err, ResolveAgentReasoningEffort(db))
	}
	// A provider change clears the model and the level.
	level = "high"
	model = "m1"
	if err := SaveAgentSettings(db, nil, &model, &level); err != nil {
		t.Fatal(err)
	}
	if err := SetAgentProvider(db, "other"); err != nil {
		t.Fatal(err)
	}
	if ResolveAgentModel(db) != "" || ResolveAgentReasoningEffort(db) != "" {
		t.Error("provider change must clear the model and the level")
	}
	// A stored value of the wrong shape is ignored.
	if err := models.SetSetting(db, SettingAgentReasoningEffort, "High Level"); err != nil {
		t.Fatal(err)
	}
	if got := ResolveAgentReasoningEffort(db); got != "" {
		t.Errorf("invalid stored level resolved to %q", got)
	}
}
