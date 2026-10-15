package modules

import (
	"testing"
	"time"

	"mailcare/app/models"
)

// TestDNSCheckLookupReuseRule: the analysis finds the verification a notice
// uses by the reuse rule of 4.4: the same name and type (none named = MX),
// the earliest checked from 24 hours before the notice was received to 48
// hours after.
func TestDNSCheckLookupReuseRule(t *testing.T) {
	db := newTestDB(t)
	jm, _ := newTestJobManager(t, db)
	notice := time.Now().Add(-72 * time.Hour).UTC().Truncate(time.Second)
	insert := func(name, qtype, outcome string, at time.Time) {
		t.Helper()
		if err := models.InsertDNSCheck(db, &models.DNSCheck{Name: name, QType: qtype, Outcome: outcome, Detail: "name=" + name + " outcome=" + outcome, CheckedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	insert("example.jp", "MX", "not_found", notice.Add(-25*time.Hour))    // before the window
	insert("example.jp", "MX", "broken", notice.Add(time.Hour))           // in the window, but later
	insert("example.jp", "MX", "exists", notice.Add(-23*time.Hour))       // the earliest in the window
	insert("example.jp", "A", "not_found", notice.Add(-time.Hour))        // another type
	insert("odd.example.jp", "MX", "inconclusive", notice)                // not an outcome
	insert("today.example.jp", "MX", "exists", time.Now())                // verified today for a recent notice
	insert("own.example.jp", "MX", "not_found", notice.Add(30*time.Hour)) // its own verification, 30 hours after it

	lookup := jm.dnsCheckLookup()
	res, ok := lookup("example.jp", "MX", notice)
	if !ok || res.Outcome != "exists" || !res.CheckedAt.Equal(notice.Add(-23*time.Hour)) || res.Detail != "name=example.jp outcome=exists" {
		t.Errorf("MX = %+v, %v", res, ok)
	}
	if res, ok := lookup("example.jp", "", notice); !ok || res.Outcome != "exists" {
		t.Errorf("no type named = %+v, %v (want the MX row)", res, ok)
	}
	if res, ok := lookup("example.jp", "A", notice); !ok || res.Outcome != "not_found" {
		t.Errorf("A = %+v, %v", res, ok)
	}
	// A notice 2 hours later still finds the earliest row in its window;
	// one 30 hours later finds none (every row is older than its window).
	if res, ok := lookup("example.jp", "MX", notice.Add(2*time.Hour)); !ok || res.Outcome != "broken" {
		t.Errorf("2h later = %+v, %v (want the row checked after the notice)", res, ok)
	}
	if res, ok := lookup("example.jp", "MX", notice.Add(30*time.Hour)); ok {
		t.Errorf("30h later = %+v, want none", res)
	}
	// A row stored today (72 hours after the notice) is no evidence for it;
	// the one checked 30 hours after it is.
	if res, ok := lookup("own.example.jp", "MX", notice); !ok || res.Outcome != "not_found" {
		t.Errorf("30h after = %+v, %v (want the row)", res, ok)
	}
	for _, name := range []string{"other.example.jp", "odd.example.jp", "today.example.jp"} {
		if res, ok := lookup(name, "MX", notice); ok {
			t.Errorf("%s = %+v, want none", name, res)
		}
	}
}

// TestStoredDNSCheckReuseBoundary: the analysis shares the window of the
// grouping (dnscheck.ReuseRange): a result checked from 24 hours before the
// notice to 48 hours after it is used, one checked a minute outside either
// end is not.
func TestStoredDNSCheckReuseBoundary(t *testing.T) {
	db := newTestDB(t)
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	notice := now.Add(-72 * time.Hour)
	cases := map[string]struct {
		at   time.Duration // checked at, relative to the notice
		want bool
	}{
		"inside.example.jp":  {-(23*time.Hour + 30*time.Minute), true},
		"start.example.jp":   {-24 * time.Hour, true},
		"outside.example.jp": {-(24*time.Hour + time.Minute), false},
		"end.example.jp":     {48 * time.Hour, true},
		"late.example.jp":    {48*time.Hour + time.Minute, false},
	}
	for name, c := range cases {
		if err := models.InsertDNSCheck(db, &models.DNSCheck{Name: name, QType: "MX", Outcome: "exists", Detail: "x", CheckedAt: notice.Add(c.at)}); err != nil {
			t.Fatal(err)
		}
	}
	for name, c := range cases {
		if res, ok := storedDNSCheck(db, name, "MX", notice, now); ok != c.want || (ok && res.Outcome != "exists") {
			t.Errorf("%s (checked %v from the notice): %+v, %v; want %v", name, c.at, res, ok, c.want)
		}
	}
}

// TestStoredDNSCheckFutureNoticeCountsAsNow: like dnscheck when grouping, a
// notice time that is zero or after now counts as now.
func TestStoredDNSCheckFutureNoticeCountsAsNow(t *testing.T) {
	db := newTestDB(t)
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	if err := models.InsertDNSCheck(db, &models.DNSCheck{Name: "example.jp", QType: "MX", Outcome: "exists", Detail: "x", CheckedAt: now.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	for _, notice := range []time.Time{now.Add(72 * time.Hour), {}} {
		if res, ok := storedDNSCheck(db, "example.jp", "MX", notice, now); !ok || res.Outcome != "exists" {
			t.Errorf("notice %v = %+v, %v", notice, res, ok)
		}
	}
}
