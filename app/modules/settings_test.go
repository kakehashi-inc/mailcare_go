package modules

import (
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
