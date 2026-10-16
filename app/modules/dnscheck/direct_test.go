package dnscheck

import (
	"context"
	"sync"
	"testing"
	"time"

	"mailcare/app/models"
	"mailcare/app/modules/mailengine"
)

// The reuse rule of a notice that shows mail from the sending sources (direct): it uses an
// outcome the authoritative servers were asked for (dns_checks.direct 1, a
// direct memo of the run), and one of the resolvers alone only when it
// cannot be verified live with them or that gives nothing. Any other notice
// uses either kind.

// directSetup starts a fake server that resolves example.jp as existing and
// is its authoritative server (a live direct verification is exists), and
// returns it with a master database holding the rows given.
func directSetup(t *testing.T, rows ...*models.DNSCheck) (*fakeServer, *Checker) {
	t.Helper()
	db := openTestMasterDB(t)
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("example.jp MX", data("mx1.example.jp"))
	withAuthority(s)
	for _, r := range rows {
		if r.Name == "" {
			r.Name = "example.jp"
		}
		r.QType, r.Detail = "MX", "stored"
		if err := models.InsertDNSCheck(db, r); err != nil {
			t.Fatal(err)
		}
	}
	return s, newTestChecker(db, s.port, "127.0.0.1")
}

// verifyAt verifies example.jp MX for a notice received at notice.
func verifyAt(c *Checker, notice time.Time, direct bool) (mailengine.DNSOutcome, mailengine.DNSVerifyStatus) {
	return c.VerifyStatus(context.Background(), mailengine.DNSFailure{Name: "example.jp", QType: "MX"}, "example.jp", notice, direct)
}

// directRows returns the stored rows of example.jp MX with their kind.
func directRows(t *testing.T, c *Checker) []bool {
	t.Helper()
	rows, err := c.db.Query(`SELECT direct FROM dns_checks WHERE name = 'example.jp' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []bool
	for rows.Next() {
		var d bool
		if err := rows.Scan(&d); err != nil {
			t.Fatal(err)
		}
		out = append(out, d)
	}
	return out
}

func TestVerifyDirectReusesDirectRow(t *testing.T) {
	s, c := directSetup(t, &models.DNSCheck{Outcome: "not_found", Direct: true, CheckedAt: time.Now().Add(-time.Hour)})
	for _, direct := range []bool{true, false} {
		if got, st := verifyAt(c, time.Now(), direct); got != mailengine.DNSOutcomeNotFound || st != mailengine.DNSStatusReused {
			t.Errorf("direct %v: %q, %v; want the stored direct row", direct, got, st)
		}
	}
	if q := s.received(); len(q) != 0 {
		t.Errorf("queries %v", q)
	}
}

// TestVerifyDirectSkipsResolverRow: a row of the resolvers alone does not
// decide a recent notice that shows mail from the sending sources: it is verified live with
// the authoritative servers and stores a direct row next to the other (the
// row of the resolvers alone does not count as present for it), which the
// next such notice reuses. A notice that does not show mail from the sending sources still
// uses the earliest row, the one of the resolvers alone.
func TestVerifyDirectSkipsResolverRow(t *testing.T) {
	s, c := directSetup(t, &models.DNSCheck{Outcome: "not_found", CheckedAt: time.Now().Add(-time.Hour)})
	if got, st := verifyAt(c, time.Now(), true); got != mailengine.DNSOutcomeExists || st != mailengine.DNSStatusVerified {
		t.Fatalf("direct: %q, %v; want verified live", got, st)
	}
	if !containsQuery(s.received(), "auth example.jp MX udp") {
		t.Errorf("the authoritative server was not asked: %v", s.received())
	}
	if kinds := directRows(t, c); len(kinds) != 2 || kinds[0] || !kinds[1] {
		t.Errorf("rows (direct) %v; want the stored one and a new direct one", kinds)
	}
	before := len(s.received())
	if got, st := verifyAt(c, time.Now(), true); got != mailengine.DNSOutcomeExists || st != mailengine.DNSStatusReused {
		t.Errorf("second direct: %q, %v; want the direct row", got, st)
	}
	if got, st := verifyAt(c, time.Now(), false); got != mailengine.DNSOutcomeNotFound || st != mailengine.DNSStatusReused {
		t.Errorf("not direct: %q, %v; want the earliest row", got, st)
	}
	if q := s.received()[before:]; len(q) != 0 {
		t.Errorf("reused outcomes made queries: %v", q)
	}
}

// TestVerifyDirectAcceptsResolverRowWhenNotLive: a notice that shows mail
// from the sending sources accepts a row of the resolvers alone when it cannot be verified
// live: too old, the check off (StoredOnly), only the OS resolver used (the
// authoritative servers are never asked then), or the authoritative stage
// off for the run.
func TestVerifyDirectAcceptsResolverRowWhenNotLive(t *testing.T) {
	old := time.Now().Add(-72 * time.Hour)
	t.Run("too old", func(t *testing.T) {
		s, c := directSetup(t, &models.DNSCheck{Outcome: "not_found", CheckedAt: old.Add(time.Hour)})
		if got, st := verifyAt(c, old, true); got != mailengine.DNSOutcomeNotFound || st != mailengine.DNSStatusReused {
			t.Errorf("%q, %v; want the row of the resolvers alone", got, st)
		}
		if q := s.received(); len(q) != 0 {
			t.Errorf("queries %v", q)
		}
	})
	t.Run("check off", func(t *testing.T) {
		s, c := directSetup(t, &models.DNSCheck{Outcome: "not_found", CheckedAt: time.Now().Add(-time.Hour)})
		stored := New(c.db, Config{StoredOnly: true, Resolvers: []string{"127.0.0.1"}, Port: s.port, Timeout: testTimeout})
		if got, st := verifyAt(stored, time.Now(), true); got != mailengine.DNSOutcomeNotFound || st != mailengine.DNSStatusReused {
			t.Errorf("%q, %v; want the row of the resolvers alone", got, st)
		}
		if q := s.received(); len(q) != 0 {
			t.Errorf("queries %v", q)
		}
	})
	t.Run("OS resolver only", func(t *testing.T) {
		s, c := directSetup(t, &models.DNSCheck{Outcome: "not_found", CheckedAt: time.Now().Add(-time.Hour)})
		sys := New(c.db, Config{Enabled: true, SystemOnly: true, Timeout: testTimeout})
		sys.sys = goResolverTo(s)
		if got, st := verifyAt(sys, time.Now(), true); got != mailengine.DNSOutcomeNotFound || st != mailengine.DNSStatusReused {
			t.Errorf("%q, %v; want the row of the resolvers alone", got, st)
		}
		if q := s.received(); len(q) != 0 {
			t.Errorf("queries %v", q)
		}
	})
	t.Run("authoritative stage off", func(t *testing.T) {
		s, c := directSetup(t, &models.DNSCheck{Outcome: "not_found", CheckedAt: time.Now().Add(-time.Hour)})
		c.authDomains = map[string]bool{"a.example": false, "b.example": false, "c.example": false}
		if got, st := verifyAt(c, time.Now(), true); got != mailengine.DNSOutcomeNotFound || st != mailengine.DNSStatusReused {
			t.Errorf("%q, %v; want the row of the resolvers alone", got, st)
		}
		if q := s.received(); len(q) != 0 {
			t.Errorf("queries %v", q)
		}
	})
}

// TestVerifyDirectFallsBackWithoutLiveOutcome: when a notice that shows
// mail from the sending sources may be verified live but that gives no outcome (the name may
// not be looked up for it, the limits of the run are reached, the
// verification is inconclusive), a row of the resolvers alone stands in, the
// same for every notice of the run; without one the status stays.
func TestVerifyDirectFallsBackWithoutLiveOutcome(t *testing.T) {
	t.Run("not applicable", func(t *testing.T) {
		db := openTestMasterDB(t)
		s := startFakeServers(t, "127.0.0.1")[0]
		s.resolve("customer.jp MX", data("mx.customer.jp"))
		if err := models.InsertDNSCheck(db, &models.DNSCheck{Name: "mx.provider.net", QType: "MX", Outcome: "exists", Detail: "stored",
			CheckedAt: time.Now().Add(-time.Hour)}); err != nil {
			t.Fatal(err)
		}
		c := newTestChecker(db, s.port, "127.0.0.1")
		f := mailengine.DNSFailure{Name: "mx.provider.net", QType: "MX"}
		if got, st := c.VerifyStatus(context.Background(), f, "customer.jp", time.Now(), true); got != mailengine.DNSOutcomeExists || st != mailengine.DNSStatusReused {
			t.Errorf("%q, %v; want the row of the resolvers alone", got, st)
		}
		f.Name = "mx.other.net"
		if got, st := c.VerifyStatus(context.Background(), f, "customer.jp", time.Now(), true); got != "" || st != mailengine.DNSStatusNotApplicable {
			t.Errorf("no row: %q, %v; want not applicable", got, st)
		}
	})
	t.Run("over budget", func(t *testing.T) {
		s, c := directSetup(t, &models.DNSCheck{Outcome: "not_found", CheckedAt: time.Now().Add(-time.Hour)})
		c.lookups = c.maxLookups
		if got, st := verifyAt(c, time.Now(), true); got != mailengine.DNSOutcomeNotFound || st != mailengine.DNSStatusReused {
			t.Errorf("%q, %v; want the row of the resolvers alone", got, st)
		}
		if q := s.receivedFor("example.jp"); len(q) != 0 {
			t.Errorf("queries %v", q)
		}
	})
	t.Run("inconclusive", func(t *testing.T) {
		s, c := directSetup(t, &models.DNSCheck{Outcome: "not_found", CheckedAt: time.Now().Add(-time.Hour)})
		s.resolve("example.jp MX", answerWith(fakeServFail))
		s.authority("example.jp MX", answerWith(fakeDrop))
		for i := 0; i < 2; i++ {
			if got, st := verifyAt(c, time.Now(), true); got != mailengine.DNSOutcomeNotFound || st != mailengine.DNSStatusReused {
				t.Errorf("notice %d: %q, %v; want the row of the resolvers alone", i, got, st)
			}
		}
		if q := s.receivedFor("example.jp"); !containsQuery(q, "auth example.jp MX udp") {
			t.Errorf("the name was not verified live with the authoritative server: %v", q)
		}
		if kinds := directRows(t, c); len(kinds) != 1 {
			t.Errorf("rows (direct) %v; want only the stored one", kinds)
		}
	})
}

// TestVerifyDirectMemory: the memory of the run follows the same rule
// (here without a database): an outcome of the resolvers alone does not
// decide a notice that shows mail from the sending sources, which is verified with the
// authoritative servers and decides the next ones of its kind; a notice that
// does not show mail from the sending sources uses either, the earlier first.
func TestVerifyDirectMemory(t *testing.T) {
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("example.jp MX", data("mx1.example.jp"))
	withAuthority(s)
	s.authority("example.jp MX", answerWith(fakeNX)) // with the authoritative server: broken
	c := newTestChecker(nil, s.port, "127.0.0.1")
	auth := func() int {
		n := 0
		for _, q := range s.received() {
			if q == "auth example.jp MX udp" {
				n++
			}
		}
		return n
	}
	steps := []struct {
		direct bool
		want   mailengine.DNSOutcome
		status mailengine.DNSVerifyStatus
		auth   int // authoritative queries so far
	}{
		{false, mailengine.DNSOutcomeExists, mailengine.DNSStatusVerified, 0},
		{true, mailengine.DNSOutcomeBroken, mailengine.DNSStatusVerified, 1},
		{true, mailengine.DNSOutcomeBroken, mailengine.DNSStatusReused, 1},
		{false, mailengine.DNSOutcomeExists, mailengine.DNSStatusReused, 1},
	}
	for i, st := range steps {
		got, status := verifyAt(c, time.Now(), st.direct)
		if got != st.want || status != st.status || auth() != st.auth {
			t.Errorf("step %d (direct %v): %q, %v, %d authoritative queries; want %q, %v, %d", i, st.direct, got, status, auth(), st.want, st.status, st.auth)
		}
	}
}

// TestVerifyDirectOtherRunResolverRow: a row of the resolvers alone that
// another run stores while this one verifies a notice that shows mail from
// the sending sources does not keep its direct row from being stored, and the outcome of
// this verification is returned.
func TestVerifyDirectOtherRunResolverRow(t *testing.T) {
	s, c := directSetup(t)
	var once sync.Once
	s.onQuery(func(q string) {
		if q != "auth example.jp MX udp" {
			return
		}
		once.Do(func() {
			if err := models.InsertDNSCheck(c.db, &models.DNSCheck{Name: "example.jp", QType: "MX", Outcome: "broken", Detail: "other run"}); err != nil {
				t.Error(err)
			}
		})
	})
	if got, st := verifyAt(c, time.Now(), true); got != mailengine.DNSOutcomeExists || st != mailengine.DNSStatusVerified {
		t.Errorf("%q, %v; want the direct outcome of this verification", got, st)
	}
	if kinds := directRows(t, c); len(kinds) != 2 || kinds[0] || !kinds[1] {
		t.Errorf("rows (direct) %v; want the other run's and a direct one", kinds)
	}
}

// TestVerifyDirectInconclusiveNotRepeated: a notice that shows mail from the sending sources
// whose verification was inconclusive before the authoritative stage could
// run (no resolver passed its control query) is not verified again for the
// next such notice of the run, as for any other notice.
func TestVerifyDirectInconclusiveNotRepeated(t *testing.T) {
	s, c := directSetup(t)
	s.resolve(controlName+" A", answerWith(fakeServFail))
	for i := 0; i < 2; i++ {
		if got, st := verifyAt(c, time.Now(), true); got != "" || st != mailengine.DNSStatusInconclusive {
			t.Errorf("notice %d: %q, %v; want inconclusive", i, got, st)
		}
	}
	if c.lookups != 1 {
		t.Errorf("%d live verifications, want 1", c.lookups)
	}
}
