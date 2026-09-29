package mailengine

import (
	"context"
	"database/sql"
	"slices"
	"testing"
	"time"

	"mailcare/app/models"
)

// blockedGroup files spamhaus_block_a.eml (sent 2025-09-03) into a new
// mailbox, marks its ip_blocked group with the state at the given time and
// returns the mailbox and the group key.
func blockedGroup(t *testing.T, state, reason string, at time.Time) (root, address, key string) {
	t.Helper()
	root, address = t.TempDir(), "newsletter@example.jp"
	msg, _ := storeOne(t, root, address, "spamhaus_block_a.eml", 1)
	if _, err := GroupMailbox(context.Background(), root, address, false, nil, nil); err != nil {
		t.Fatal(err)
	}
	db := mustOpenIndex(t, root, address)
	key = groupOf(t, db, msg.MessageKey)
	if g, err := models.GetGroup(db, key); err != nil || g.Category != categoryIPBlocked {
		t.Fatalf("group of the block notice: %+v (err %v)", g, err)
	}
	setStateForTest(t, db, key, state, reason, "", at)
	return root, address, key
}

// fileSecondBlock files spamhaus_block_b.eml (the same block reported by
// another receiving server with other wording, returned mail sent
// 2025-09-04) and returns the group afterwards and whether the run flagged
// it for analysis.
func fileSecondBlock(t *testing.T, root, address, key string, days RecheckDays) (*models.BounceGroup, bool) {
	t.Helper()
	storeOne(t, root, address, "spamhaus_block_b.eml", 2)
	res, err := GroupMailbox(context.Background(), root, address, false, days, nil)
	if err != nil {
		t.Fatal(err)
	}
	g, err := models.GetGroup(mustOpenIndex(t, root, address), key)
	if err != nil {
		t.Fatal(err)
	}
	return g, slices.Contains(res.GroupsTouched, key)
}

func TestRecheckByDaysAfterResolved(t *testing.T) {
	jst := time.FixedZone("JST", 9*3600)
	for _, c := range []struct {
		name    string
		state   string
		reason  string
		decided time.Time
		days    RecheckDays
		want    bool
	}{
		// Within the 14 days of ip_blocked: another server reporting the
		// same block is expected while the delisting takes effect.
		{"within the settling period", "resolved", models.ResolveActionDelisting, time.Date(2025, 9, 1, 0, 0, 0, 0, jst), nil, false},
		// The returned mail was sent more than 14 days after the decision.
		{"after the settling period", "resolved", models.ResolveActionDelisting, time.Date(2025, 8, 1, 0, 0, 0, 0, jst), nil, true},
		// The settings lengthen the period.
		{"longer period from the settings", "resolved", models.ResolveActionDelisting, time.Date(2025, 8, 1, 0, 0, 0, 0, jst),
			RecheckDays{categoryIPBlocked: 60}, false},
		// An ignored group keeps receiving what it was ignored for.
		{"ignored", "ignored", models.IgnoreReasonLowImpact, time.Date(2025, 8, 1, 0, 0, 0, 0, jst), nil, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			root, address, key := blockedGroup(t, c.state, c.reason, c.decided)
			g, touched := fileSecondBlock(t, root, address, key, c.days)
			if g.State != c.state || g.NeedsRecheck != c.want || g.NeedsAnalysis != c.want || touched != c.want {
				t.Errorf("state %s recheck %v analysis %v touched %v, want %s / %v", g.State, g.NeedsRecheck,
					g.NeedsAnalysis, touched, c.state, c.want)
			}
			if g.MessageCount != 2 {
				t.Errorf("message count = %d, want 2 (the counters follow every notice)", g.MessageCount)
			}
		})
	}
}

func TestRecheckClearedByTheNextDecision(t *testing.T) {
	jst := time.FixedZone("JST", 9*3600)
	root, address, key := blockedGroup(t, "resolved", models.ResolveActionDelisting, time.Date(2025, 8, 1, 0, 0, 0, 0, jst))
	if g, _ := fileSecondBlock(t, root, address, key, nil); !g.NeedsRecheck {
		t.Fatal("the group was not sent back")
	}
	db := mustOpenIndex(t, root, address)
	setStateForTest(t, db, key, "resolved", models.ResolveActionOther, "asked again", time.Now().UTC())
	g, err := models.GetGroup(db, key)
	if err != nil || g.NeedsRecheck || g.NeedsAnalysis || g.StateNote.String != "asked again" {
		t.Errorf("after the new decision: %+v (err %v)", g, err)
	}
	history, err := models.ListGroupStateChanges(db, key)
	if err != nil || len(history) != 2 {
		t.Errorf("history = %+v (err %v), want both decisions", history, err)
	}
}

func TestRecheckSendsBack(t *testing.T) {
	decided := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	member := func(status, template, mta string, notice time.Time, sent sql.NullTime) *models.GroupBounce {
		m := &models.GroupBounce{Date: notice}
		m.StatusCode, m.DiagnosticTemplate, m.RemoteMTA, m.DiagnosticSource = status, template, mta, DiagnosticSourceDSN
		m.OriginalDate = sent
		return m
	}
	known := member("5.7.26", "550 5.7.26 unauthenticated mail", "mx1.example.net", decided, sql.NullTime{})
	basis := &recheckBasis{
		change: &models.GroupStateChange{State: "ignored", ChangedAt: decided},
		keys:   map[string]bool{RecheckKey(known): true},
	}
	after := decided.AddDate(0, 0, 1)
	for _, c := range []struct {
		name     string
		category string
		state    string
		m        *models.GroupBounce
		want     bool
	}{
		{"auth_failure, known pattern", categoryAuthFailure, "ignored", known, false},
		{"auth_failure, known pattern from another server", categoryAuthFailure, "ignored",
			member("5.7.26", "550 5.7.26 unauthenticated mail", "mx2.example.org", after, sql.NullTime{}), false},
		{"auth_failure, new wording", categoryAuthFailure, "ignored",
			member("5.7.1", "550 5.7.1 spf check failed", "mx1.example.net", after, sql.NullTime{}), true},
		{"ip_blocked, new wording", categoryIPBlocked, "ignored",
			member("5.7.1", "554 5.7.1 listed at zen.spamhaus.org", "mx3.example.com", after, sql.NullTime{}), false},
		{"resolved ip_blocked, notice after the period about mail sent before it", categoryIPBlocked, "resolved",
			member("5.7.1", "554 5.7.1 x", "mx3.example.com", decided.AddDate(0, 0, 20),
				sql.NullTime{Time: decided.AddDate(0, 0, 10), Valid: true}), false},
		{"resolved ip_blocked, mail sent after the period", categoryIPBlocked, "resolved",
			member("5.7.1", "554 5.7.1 x", "mx3.example.com", decided.AddDate(0, 0, 20),
				sql.NullTime{Time: decided.AddDate(0, 0, 15), Valid: true}), true},
		{"resolved ip_blocked, no send date: the notice date", categoryIPBlocked, "resolved",
			member("5.7.1", "554 5.7.1 x", "mx3.example.com", decided.AddDate(0, 0, 15), sql.NullTime{}), true},
		{"open group", categoryIPBlocked, "open",
			member("5.7.1", "554 5.7.1 x", "mx3.example.com", decided.AddDate(0, 0, 30), sql.NullTime{}), false},
	} {
		g := &models.BounceGroup{Category: c.category, State: c.state}
		if got := basis.sendsBack(g, c.m, nil); got != c.want {
			t.Errorf("%s: sendsBack = %v, want %v", c.name, got, c.want)
		}
	}
	var none *recheckBasis
	if none.sendsBack(&models.BounceGroup{Category: categoryAuthFailure, State: "resolved"}, known, nil) {
		t.Error("a group without a recorded state change must not be sent back")
	}
}

func TestOpenIndexFillsMigratedStateChanges(t *testing.T) {
	root, address, key := blockedGroup(t, "resolved", models.ResolveActionDelisting, time.Now().UTC())
	db := mustOpenIndex(t, root, address)
	// A history row as the migration creates it: no re-check keys.
	if _, err := db.Exec(`DELETE FROM group_state_change_patterns`); err != nil {
		t.Fatal(err)
	}
	idx, err := OpenIndex(context.Background(), root, address, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	change, err := models.LatestGroupStateChange(idx, key)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := models.ListGroupStateChangePatterns(idx, change.ID)
	want, _ := GroupRecheckKeys(idx, key)
	if err != nil || len(keys) != len(want) || len(want) == 0 {
		t.Errorf("filled keys = %v (err %v), want %v", keys, err, want)
	}
}
