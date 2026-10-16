package models

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// The analysis flag of a recipient-side group comes from an explicit
// request only and stays until the analysis settles: a counter refresh, a
// re-check and a state change that keeps the group open leave it, resolving
// or ignoring clears it. A group that stops being actionable loses the flag
// the grouping set.
func TestAnalysisFlagOfRecipientSideGroup(t *testing.T) {
	db, err := OpenMailIndex(filepath.Join(t.TempDir(), "x.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const key = "0000000000000001"
	excluded := &BounceGroup{GroupKey: key, Category: "user_unknown", UnitValue: "a@x.example", Responsible: "recipient"}
	if err := UpsertGroup(db, excluded); err != nil {
		t.Fatal(err)
	}
	flag := func() bool {
		t.Helper()
		g, err := GetGroup(db, key)
		if err != nil {
			t.Fatal(err)
		}
		return g.NeedsAnalysis
	}
	if flag() {
		t.Fatal("a new recipient-side group is flagged")
	}
	if err := SetGroupNeedsAnalysis(db, key, true); err != nil { // the explicit request
		t.Fatal(err)
	}
	if err := RefreshGroupCounters(db, key); err != nil {
		t.Fatal(err)
	}
	if err := SetGroupState(db, key, "open", sql.NullString{}, sql.NullString{}, time.Now(), false); err != nil {
		t.Fatal(err)
	}
	if !flag() {
		t.Error("the request was dropped by a counter refresh or an open state")
	}
	waiting, err := ListGroupsNeedingAnalysis(db)
	if err != nil || len(waiting) != 1 || waiting[0].GroupKey != key {
		t.Errorf("waiting %d (err %v), want the requested group", len(waiting), err)
	}
	reason := sql.NullString{String: "other", Valid: true}
	if err := SetGroupState(db, key, "resolved", reason, sql.NullString{}, time.Now(), true); err != nil {
		t.Fatal(err)
	}
	if flag() {
		t.Error("resolving kept the flag")
	}
	if err := SetGroupNeedsAnalysis(db, key, true); err != nil { // asked again while resolved
		t.Fatal(err)
	}
	if err := SetGroupNeedsRecheck(db, key); err != nil {
		t.Fatal(err)
	}
	if !flag() {
		t.Error("a re-check dropped the request")
	}

	// An actionable group flagged when created loses that flag once it is
	// no longer actionable.
	const other = "0000000000000002"
	actionable := &BounceGroup{GroupKey: other, Category: "ip_blocked", Actionable: true, UnitValue: "203.0.113.5", Responsible: "sender"}
	if err := UpsertGroup(db, actionable); err != nil {
		t.Fatal(err)
	}
	actionable.Actionable = false
	if err := UpsertGroup(db, actionable); err != nil {
		t.Fatal(err)
	}
	if g, err := GetGroup(db, other); err != nil || g.NeedsAnalysis {
		t.Errorf("group no longer actionable: %+v (err %v), want no flag", g, err)
	}
}
