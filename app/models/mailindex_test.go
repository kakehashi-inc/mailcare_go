package models

import (
	"path/filepath"
	"testing"
	"time"
)

// TestLikeContainsEscapesWildcards checks that a search string containing
// the LIKE wildcards or the escape character matches only literally.
func TestLikeContainsEscapesWildcards(t *testing.T) {
	for in, want := range map[string]string{
		"abc":        "%abc%",
		"50%":        `%50\%%`,
		"a_b":        `%a\_b%`,
		`C:\path`:    `%C:\\path%`,
		`%_\`:        `%\%\_\\%`,
		"plain text": "%plain text%",
	} {
		if got := likeContains(in); got != want {
			t.Errorf("likeContains(%q) = %q, want %q", in, got, want)
		}
	}

	db, err := OpenMailIndex(filepath.Join(t.TempDir(), "x.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	subjects := []string{"100% sure", "100 percent", "a_b", "axb", `back\slash`, "backslash"}
	for i, subject := range subjects {
		m := &Message{MessageKey: "20260901-12000" + string(rune('0'+i)) + "_aaaaaaaaaaaa", UID: uint32(i + 1), UIDValidity: 1,
			Subject: subject, Date: time.Now().UTC()}
		if err := InsertMessage(db, m); err != nil {
			t.Fatal(err)
		}
	}
	for q, want := range map[string]int{"100%": 1, "a_b": 1, `back\`: 1, "100": 2, "b": 4, "zzz": 0} {
		_, total, err := ListMessages(db, MessageFilter{Query: q})
		if err != nil {
			t.Fatalf("query %q: %v", q, err)
		}
		if total != want {
			t.Errorf("query %q matched %d messages, want %d", q, total, want)
		}
	}
	for i, unit := range []string{"50%", "50 percent", "x_y", "xzy"} {
		if err := UpsertGroup(db, &BounceGroup{GroupKey: "000000000000000" + string(rune('0'+i)), Category: "user_unknown",
			UnitValue: unit}); err != nil {
			t.Fatal(err)
		}
	}
	for q, want := range map[string]int{"50%": 1, "x_y": 1, "50": 2} {
		groups, err := ListGroups(db, GroupFilter{Query: q})
		if err != nil {
			t.Fatalf("group query %q: %v", q, err)
		}
		if len(groups) != want {
			t.Errorf("group query %q matched %d groups, want %d", q, len(groups), want)
		}
	}
}
