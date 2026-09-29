package modules

import (
	"strings"
	"testing"

	"mailcare/app/models"
	"mailcare/app/modules/wording"
)

func TestGroupStateInput(t *testing.T) {
	long := strings.Repeat("あ", MaxGroupStateNoteLength+1)
	for _, c := range []struct {
		name                 string
		state, reason, note  string
		wantReason, wantNote string // "" = NULL
		wantKey              string // error key; "" = valid
	}{
		{name: "open drops the reason and the note", state: "open", reason: "other", note: "x"},
		{name: "resolved needs a reason", state: "resolved", note: "x", wantKey: "validation.group.resolveActionRequired"},
		{name: "resolved with an unknown reason", state: "resolved", reason: models.IgnoreReasonLowImpact, wantKey: "system.invalidRequest"},
		{name: "resolved keeps the note with any reason", state: " resolved ", reason: models.ResolveActionDelisting,
			note: "  asked Spamhaus\n", wantReason: models.ResolveActionDelisting, wantNote: "asked Spamhaus"},
		{name: "resolved with a blank note", state: "resolved", reason: models.ResolveActionOther, note: "  ",
			wantReason: models.ResolveActionOther},
		{name: "resolved note too long", state: "resolved", reason: models.ResolveActionOther, note: long,
			wantKey: "validation.group.stateNoteTooLong"},
		{name: "ignored needs a reason", state: "ignored", note: "x", wantKey: "validation.group.ignoreReasonRequired"},
		{name: "ignored with an unknown reason", state: "ignored", reason: models.ResolveActionDelisting, wantKey: "system.invalidRequest"},
		{name: "ignored drops the note of a fixed reason", state: "ignored", reason: models.IgnoreReasonSpoofing, note: "x",
			wantReason: models.IgnoreReasonSpoofing},
		{name: "ignored other keeps the note", state: "ignored", reason: models.IgnoreReasonOther, note: " test run ",
			wantReason: models.IgnoreReasonOther, wantNote: "test run"},
		{name: "ignored other without a note", state: "ignored", reason: models.IgnoreReasonOther, wantReason: models.IgnoreReasonOther},
		{name: "unknown state", state: "done", wantKey: "system.invalidRequest"},
	} {
		got, err := GroupStateInput(c.state, c.reason, c.note)
		if c.wantKey != "" {
			if m, ok := wording.As(err); !ok || m.Key != c.wantKey {
				t.Errorf("%s: err = %v, want %s", c.name, err, c.wantKey)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got.State != strings.TrimSpace(c.state) || got.Reason.Valid != (c.wantReason != "") ||
			got.Reason.String != c.wantReason || got.Note.Valid != (c.wantNote != "") || got.Note.String != c.wantNote {
			t.Errorf("%s: got %+v, want reason %q note %q", c.name, got, c.wantReason, c.wantNote)
		}
	}
}
