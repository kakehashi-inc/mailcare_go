package modules

import (
	"testing"
	"time"

	"mailcare/app/models"
	"mailcare/app/modules/dnscheck"
)

// TestDNSCheckLookupReuseRule: with the check off the analysis finds the
// verification a notice uses by the rule of the stored-only mode (4.4): the
// same name (whatever record type the notice named), the earliest checked
// from 24 hours before the notice was received up to now; rows of the
// earlier verification per record type are not used.
func TestDNSCheckLookupReuseRule(t *testing.T) {
	db := newTestDB(t)
	jm, _ := newTestJobManager(t, db)
	if err := models.SetSetting(db, SettingDNSCheckEnabled, "0"); err != nil {
		t.Fatal(err)
	}
	notice := time.Now().Add(-72 * time.Hour).UTC().Truncate(time.Second)
	insert := func(name, qtype, outcome string, at time.Time) {
		t.Helper()
		if err := models.InsertDNSCheck(db, &models.DNSCheck{Name: name, QType: qtype, Outcome: outcome, Detail: "name=" + name + " outcome=" + outcome, CheckedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	const kind = dnscheck.VerifyKind
	insert("example.jp", kind, "not_found", notice.Add(-25*time.Hour))     // before the window
	insert("example.jp", kind, "broken", notice.Add(time.Hour))            // in the window, but later
	insert("example.jp", kind, "exists", notice.Add(-23*time.Hour))        // the earliest in the window
	insert("example.jp", "MX", "not_found", notice.Add(-24*time.Hour))     // a row of the earlier verification per type
	insert("odd.example.jp", kind, "inconclusive", notice)                 // not an outcome
	insert("today.example.jp", kind, "exists", time.Now())                 // verified today
	insert("late.example.jp", kind, "not_found", notice.Add(30*time.Hour)) // its verification, 30 hours after it
	insert("old.example.jp", "MX", "exists", notice)                       // only a row per type

	lookup := jm.dnsCheckLookup()
	res, ok := lookup("example.jp", notice)
	if !ok || res.Outcome != "exists" || !res.CheckedAt.Equal(notice.Add(-23*time.Hour)) || res.Detail != "name=example.jp outcome=exists" {
		t.Errorf("example.jp = %+v, %v", res, ok)
	}
	// A notice 2 hours later still finds the earliest row in its window.
	if res, ok := lookup("example.jp", notice.Add(2*time.Hour)); !ok || res.Outcome != "broken" {
		t.Errorf("2h later = %+v, %v (want the row checked after the notice)", res, ok)
	}
	// Rows checked after the notice, however much later, count: today's
	// and the one 30 hours after it.
	for name, want := range map[string]string{"today.example.jp": "exists", "late.example.jp": "not_found"} {
		if res, ok := lookup(name, notice); !ok || res.Outcome != want {
			t.Errorf("%s = %+v, %v; want %s", name, res, ok, want)
		}
	}
	for _, name := range []string{"other.example.jp", "odd.example.jp", "old.example.jp"} {
		if res, ok := lookup(name, notice); ok {
			t.Errorf("%s = %+v, want none", name, res)
		}
	}
}

// TestStoredDNSCheckReuseBoundary: with the check off the analysis shares
// the window of the grouping (dnscheck.ReuseRange): a result checked from 24 hours before the
// notice up to now is used, one checked a minute before that is not.
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
		"late.example.jp":    {48*time.Hour + time.Minute, true},
		"now.example.jp":     {72 * time.Hour, true},
	}
	for name, c := range cases {
		if err := models.InsertDNSCheck(db, &models.DNSCheck{Name: name, QType: dnscheck.VerifyKind, Outcome: "exists", Detail: "x", CheckedAt: notice.Add(c.at)}); err != nil {
			t.Fatal(err)
		}
	}
	for name, c := range cases {
		if res, ok := storedDNSCheck(db, name, notice, now, false); ok != c.want || (ok && res.Outcome != "exists") {
			t.Errorf("%s (checked %v from the notice): %+v, %v; want %v", name, c.at, res, ok, c.want)
		}
	}
}

// TestStoredDNSCheckFutureNoticeCountsAsNow: like dnscheck when grouping
// with the check off, a notice time that is zero or after now counts as now.
func TestStoredDNSCheckFutureNoticeCountsAsNow(t *testing.T) {
	db := newTestDB(t)
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	if err := models.InsertDNSCheck(db, &models.DNSCheck{Name: "example.jp", QType: dnscheck.VerifyKind, Outcome: "exists", Detail: "x", CheckedAt: now.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	for _, notice := range []time.Time{now.Add(72 * time.Hour), {}} {
		if res, ok := storedDNSCheck(db, "example.jp", notice, now, false); !ok || res.Outcome != "exists" {
			t.Errorf("notice %v = %+v, %v", notice, res, ok)
		}
	}
}

// TestStoredDNSCheckDirect: with the check off the analysis shows the
// earliest row of the window whichever its kind, and says whether the authoritative servers were
// asked for it (dns_checks.direct).
func TestStoredDNSCheckDirect(t *testing.T) {
	db := newTestDB(t)
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	notice := now.Add(-time.Hour)
	for _, r := range []*models.DNSCheck{
		{Name: "direct.example.jp", QType: dnscheck.VerifyKind, Outcome: "broken", Detail: "direct", Direct: true, CheckedAt: notice},
		{Name: "both.example.jp", QType: dnscheck.VerifyKind, Outcome: "exists", Detail: "resolvers", CheckedAt: notice},
		{Name: "both.example.jp", QType: dnscheck.VerifyKind, Outcome: "broken", Detail: "direct", Direct: true, CheckedAt: notice.Add(time.Minute)},
	} {
		if err := models.InsertDNSCheck(db, r); err != nil {
			t.Fatal(err)
		}
	}
	if res, ok := storedDNSCheck(db, "direct.example.jp", notice, now, false); !ok || !res.Direct || res.Outcome != "broken" {
		t.Errorf("direct row = %+v, %v", res, ok)
	}
	if res, ok := storedDNSCheck(db, "both.example.jp", notice, now, false); !ok || res.Direct || res.Detail != "resolvers" {
		t.Errorf("both kinds = %+v, %v; want the earliest, of the resolvers alone", res, ok)
	}
}

// TestStoredDNSCheckLive: while the check is on the analysis follows the
// grouping: the earliest row checked within dnscheck.CacheFreshAge before
// now, whatever the age of the notice, else the latest stored row of the
// name (a later verification, or the stand-in of one that gave nothing).
// The setting is read when the analysis asks.
func TestStoredDNSCheckLive(t *testing.T) {
	db := newTestDB(t)
	jm, _ := newTestJobManager(t, db)
	now := time.Now().UTC().Truncate(time.Second)
	notice := now.Add(-72 * time.Hour)
	fresh := dnscheck.CacheFreshAge
	for _, r := range []*models.DNSCheck{
		{Name: "fresh.example.jp", Outcome: "not_found", CheckedAt: now.Add(-fresh - time.Minute)},
		{Name: "fresh.example.jp", Outcome: "exists", CheckedAt: now.Add(-fresh + time.Minute)},
		{Name: "fresh.example.jp", Outcome: "broken", CheckedAt: now.Add(-time.Minute)},
		{Name: "stale.example.jp", Outcome: "exists", CheckedAt: notice.Add(-24 * time.Hour)},
		{Name: "stale.example.jp", Outcome: "broken", CheckedAt: now.Add(-fresh - time.Hour)},
		{Name: "stale.example.jp", Outcome: "not_found", CheckedAt: notice.Add(-48 * time.Hour)},
	} {
		r.QType, r.Detail = dnscheck.VerifyKind, r.Outcome
		if err := models.InsertDNSCheck(db, r); err != nil {
			t.Fatal(err)
		}
	}
	lookup := jm.dnsCheckLookup()
	if res, ok := lookup("fresh.example.jp", notice); !ok || res.Outcome != "exists" {
		t.Errorf("fresh rows = %+v, %v; want the earliest fresh one (exists)", res, ok)
	}
	if res, ok := lookup("stale.example.jp", notice); !ok || res.Outcome != "broken" {
		t.Errorf("stale rows = %+v, %v; want the latest one (broken)", res, ok)
	}
	if res, ok := lookup("none.example.jp", notice); ok {
		t.Errorf("no row = %+v, want none", res)
	}
	// Switched off: the window of the notice again (24 hours before it up
	// to now, the earliest).
	if err := models.SetSetting(db, SettingDNSCheckEnabled, "0"); err != nil {
		t.Fatal(err)
	}
	if res, ok := lookup("stale.example.jp", notice); !ok || res.Outcome != "exists" {
		t.Errorf("check off = %+v, %v; want the earliest of the window (exists)", res, ok)
	}
}
