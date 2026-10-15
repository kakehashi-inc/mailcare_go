package dnscheck

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"mailcare/app/models"
	"mailcare/app/modules/mailengine"
)

// The fixed signature mailengine.DNSVerifier expects of Verify.
var _ interface {
	Verify(ctx context.Context, f mailengine.DNSFailure, recipientDomain string, noticeTime time.Time, direct bool) (mailengine.DNSOutcome, bool)
} = (*Checker)(nil)

// testTimeout is the per-query timeout of the tests (dropped queries wait
// for it).
const testTimeout = 250 * time.Millisecond

// newTestChecker returns a Checker asking the fake servers (resolvers at the
// given addresses, all on the port of the servers) that accepts loopback
// authoritative servers.
func newTestChecker(db *sql.DB, port int, resolvers ...string) *Checker {
	c := New(db, Config{Enabled: true, Resolvers: resolvers, Port: port, Timeout: testTimeout})
	c.allowLocal = true
	return c
}

// storedChecks returns every row of dns_checks.
func storedChecks(t *testing.T, db *sql.DB) []*models.DNSCheck {
	t.Helper()
	rows, err := db.Query(`SELECT name, qtype, outcome, detail FROM dns_checks ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []*models.DNSCheck
	for rows.Next() {
		c := &models.DNSCheck{}
		if err := rows.Scan(&c.Name, &c.QType, &c.Outcome, &c.Detail); err != nil {
			t.Fatal(err)
		}
		out = append(out, c)
	}
	return out
}

// verify verifies a name for a notice received now that clearly concerns
// our own mail (direct: the authoritative servers are asked too).
func verify(t *testing.T, c *Checker, name, qtype, recipientDomain string) (mailengine.DNSOutcome, bool) {
	t.Helper()
	return c.Verify(context.Background(), mailengine.DNSFailure{Name: name, QType: qtype}, recipientDomain, time.Now(), true)
}

// withAuthority makes s the authoritative server of example.jp: the
// resolver role names ns1.example.jp at s, and the authoritative role
// answers the MX of example.jp unless the test changes it.
func withAuthority(s *fakeServer) {
	s.resolve("example.jp NS", data("ns1.example.jp"))
	s.resolve("ns1.example.jp A", data(s.ip))
	s.authority("example.jp MX", data("mx1.example.jp"))
}

// TestVerifyExists verifies a name every path answers: exists, stored with
// the evidence of the resolver and the authoritative server, and reused by
// the next notice without a query.
func TestVerifyExists(t *testing.T) {
	db := openTestMasterDB(t)
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("example.jp MX", data("mx1.example.jp", "mx2.example.jp"))
	withAuthority(s)
	c := newTestChecker(db, s.port, "127.0.0.1")

	if got, ok := verify(t, c, "Example.JP.", "MX", "example.jp"); !ok || got != mailengine.DNSOutcomeExists {
		t.Fatalf("Verify = %q, %v", got, ok)
	}
	rows := storedChecks(t, db)
	if len(rows) != 1 {
		t.Fatalf("stored %d rows", len(rows))
	}
	r := rows[0]
	if r.Name != "example.jp" || r.QType != "MX" || r.Outcome != "exists" {
		t.Errorf("row %+v", r)
	}
	for _, want := range []string{
		"name=example.jp type=MX outcome=exists",
		"resolver 127.0.0.1: control ok; MX ok (mx1.example.jp, mx2.example.jp)",
		"authoritative ns1.example.jp (127.0.0.1): MX ok (mx1.example.jp)",
	} {
		if !strings.Contains(r.Detail, want) {
			t.Errorf("detail lacks %q:\n%s", want, r.Detail)
		}
	}
	if auth := s.receivedFor("example.jp"); !containsQuery(auth, "auth example.jp MX udp") {
		t.Errorf("the authoritative server was not asked: %v", auth)
	}

	before := len(s.received())
	if got, ok := verify(t, c, "example.jp", "MX", "example.jp"); !ok || got != mailengine.DNSOutcomeExists {
		t.Fatalf("second Verify = %q, %v", got, ok)
	}
	if after := len(s.received()); after != before {
		t.Errorf("the reused outcome made %d queries", after-before)
	}
	if st := c.Stats(); st != (Stats{Verified: 1, Reused: 1}) {
		t.Errorf("stats %+v", st)
	}
}

func containsQuery(queries []string, want string) bool {
	for _, q := range queries {
		if q == want {
			return true
		}
	}
	return false
}

// TestVerifyNotFound verifies a name the resolver answers with NXDOMAIN and
// whose organization domain has no name servers.
func TestVerifyNotFound(t *testing.T) {
	db := openTestMasterDB(t)
	s := startFakeServers(t, "127.0.0.1")[0]
	c := newTestChecker(db, s.port, "127.0.0.1")
	if got, ok := verify(t, c, "nosuch.jp", "MX", "nosuch.jp"); !ok || got != mailengine.DNSOutcomeNotFound {
		t.Fatalf("Verify = %q, %v", got, ok)
	}
	rows := storedChecks(t, db)
	if len(rows) != 1 || rows[0].Outcome != "not_found" {
		t.Fatalf("rows %+v", rows)
	}
	for _, want := range []string{"resolver 127.0.0.1: control ok; MX NXDOMAIN", "authoritative: no NS records for nosuch.jp"} {
		if !strings.Contains(rows[0].Detail, want) {
			t.Errorf("detail lacks %q:\n%s", want, rows[0].Detail)
		}
	}
}

// TestVerifyBrokenByAuthoritative checks that a resolver answer the
// authoritative server contradicts (NXDOMAIN, SERVFAIL) is broken, and that
// an authoritative server that does not answer or is not authoritative for
// the name does not vote.
func TestVerifyBrokenByAuthoritative(t *testing.T) {
	for _, tc := range []struct {
		name string
		auth fakeAnswer
		want mailengine.DNSOutcome
		note string
	}{
		{"nxdomain", answerWith(fakeNX), mailengine.DNSOutcomeBroken, "MX NXDOMAIN"},
		{"servfail", answerWith(fakeServFail), mailengine.DNSOutcomeBroken, "MX SERVFAIL"},
		{"nodata", answerWith(fakeNoData), mailengine.DNSOutcomeBroken, "MX none; A no data; AAAA no data"},
		{"not authoritative", answerWith(fakeNotAuth, "mx1.example.jp"), mailengine.DNSOutcomeExists, "MX not authoritative for the name"},
		{"cname", data("cname:mail.example.net"), mailengine.DNSOutcomeExists, "MX CNAME mail.example.net"},
		{"drop", answerWith(fakeDrop), mailengine.DNSOutcomeExists, "MX timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestMasterDB(t)
			s := startFakeServers(t, "127.0.0.1")[0]
			s.resolve("example.jp MX", data("mx1.example.jp"))
			withAuthority(s)
			s.authority("example.jp MX", tc.auth)
			c := newTestChecker(db, s.port, "127.0.0.1")
			if got, ok := verify(t, c, "example.jp", "MX", "example.jp"); !ok || got != tc.want {
				t.Fatalf("Verify = %q, %v; want %q", got, ok, tc.want)
			}
			rows := storedChecks(t, db)
			if len(rows) != 1 || !strings.Contains(rows[0].Detail, "authoritative ns1.example.jp (127.0.0.1): "+tc.note) {
				t.Errorf("rows %d, detail lacks %q:\n%s", len(rows), tc.note, rows[0].Detail)
			}
		})
	}
}

// TestVerifySilentAuthoritativeNotAskedAgain checks that an authoritative
// address that did not answer is not asked again in the run.
func TestVerifySilentAuthoritativeNotAskedAgain(t *testing.T) {
	db := openTestMasterDB(t)
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("example.jp MX", data("mx1.example.jp"))
	s.resolve("www.example.jp A", data("192.0.2.1"))
	withAuthority(s)
	s.authority("example.jp MX", answerWith(fakeDrop))
	s.authority("www.example.jp A", data("192.0.2.1"))
	c := newTestChecker(db, s.port, "127.0.0.1")
	if got, ok := verify(t, c, "example.jp", "MX", "example.jp"); !ok || got != mailengine.DNSOutcomeExists {
		t.Fatalf("Verify = %q, %v", got, ok)
	}
	if got, ok := verify(t, c, "www.example.jp", "A", "example.jp"); !ok || got != mailengine.DNSOutcomeExists {
		t.Fatalf("second Verify = %q, %v", got, ok)
	}
	if q := s.receivedFor("www.example.jp"); containsQuery(q, "auth www.example.jp A udp") {
		t.Errorf("the silent authoritative server was asked again: %v", q)
	}
	rows := storedChecks(t, db)
	if len(rows) != 2 || !strings.Contains(rows[1].Detail, "no response earlier in this run") {
		t.Errorf("rows %+v", rows)
	}
}

// TestVerifySplitResolvers checks that resolvers that disagree give broken.
func TestVerifySplitResolvers(t *testing.T) {
	db := openTestMasterDB(t)
	servers := startFakeServers(t, "127.0.0.1", "127.0.0.2")
	servers[0].resolve("example.jp MX", data("mx1.example.jp"))
	// 127.0.0.2 does not know the name: NXDOMAIN.
	c := newTestChecker(db, servers[0].port, "127.0.0.1", "127.0.0.2")
	if got, ok := verify(t, c, "example.jp", "MX", "example.jp"); !ok || got != mailengine.DNSOutcomeBroken {
		t.Fatalf("Verify = %q, %v", got, ok)
	}
	rows := storedChecks(t, db)
	if len(rows) != 1 || !strings.Contains(rows[0].Detail, "resolver 127.0.0.2: control ok; MX NXDOMAIN") {
		t.Errorf("rows %+v", rows)
	}
}

// TestVerifyIndirect: a notice that does not clearly concern our own mail
// (direct false; a forged one names a domain and servers of its author's
// choosing) is verified through the resolvers alone: neither the name
// servers of the domain are looked up nor the authoritative server at the
// address they give is asked, and the evidence says why. With direct the
// same name reaches the authoritative server.
func TestVerifyIndirect(t *testing.T) {
	servers := startFakeServers(t, "127.0.0.1", "127.0.0.2")
	resolver, attacker := servers[0], servers[1]
	resolver.resolve("evil.example MX", data("mx.evil.example"))
	resolver.resolve("evil.example NS", data("ns1.evil.example"))
	resolver.resolve("ns1.evil.example A", data(attacker.ip))
	attacker.authority("evil.example MX", data("mx.evil.example"))
	forged := mailengine.DNSFailure{Name: "evil.example", QType: "MX"}

	db := openTestMasterDB(t)
	c := newTestChecker(db, resolver.port, "127.0.0.1")
	if got, ok := c.Verify(context.Background(), forged, "evil.example", time.Now(), false); !ok || got != mailengine.DNSOutcomeExists {
		t.Fatalf("Verify = %q, %v", got, ok)
	}
	if q := attacker.received(); len(q) != 0 {
		t.Errorf("the name server of the notice's domain was asked: %v", q)
	}
	if q := resolver.receivedFor("evil.example"); containsQuery(q, "rd evil.example NS udp") {
		t.Errorf("the name servers were looked up: %v", q)
	}
	if rows := storedChecks(t, db); len(rows) != 1 ||
		!strings.Contains(rows[0].Detail, "authoritative: not asked (the notice does not show mail of ours)") {
		t.Errorf("rows %+v", rows)
	}

	c = newTestChecker(openTestMasterDB(t), resolver.port, "127.0.0.1")
	if got, ok := c.Verify(context.Background(), forged, "evil.example", time.Now(), true); !ok || got != mailengine.DNSOutcomeExists {
		t.Fatalf("direct: Verify = %q, %v", got, ok)
	}
	if q := attacker.received(); !containsQuery(q, "auth evil.example MX udp") {
		t.Errorf("direct: the authoritative server was not asked: %v", q)
	}
}

// TestVerifyIndirectVote: without the authoritative servers the resolvers
// alone vote: all of them answering is exists, all NXDOMAIN is not_found
// (where an authoritative server holding the name would have made it
// broken), and resolvers that disagree are broken.
func TestVerifyIndirectVote(t *testing.T) {
	servers := startFakeServers(t, "127.0.0.1", "127.0.0.2")
	for _, s := range servers {
		s.resolve("both.example.jp MX", data("mx1.example.jp"))
	}
	servers[0].resolve("half.example.jp MX", data("mx1.example.jp")) // 127.0.0.2: NXDOMAIN
	withAuthority(servers[0])
	servers[0].authority("gone.example.jp MX", data("mx1.example.jp")) // the resolvers: NXDOMAIN
	for _, tc := range []struct {
		name             string
		direct, indirect mailengine.DNSOutcome
	}{
		{"both.example.jp", mailengine.DNSOutcomeBroken, mailengine.DNSOutcomeExists}, // the authoritative server knows no such MX
		{"gone.example.jp", mailengine.DNSOutcomeBroken, mailengine.DNSOutcomeNotFound},
		{"half.example.jp", mailengine.DNSOutcomeBroken, mailengine.DNSOutcomeBroken},
	} {
		for direct, want := range map[bool]mailengine.DNSOutcome{true: tc.direct, false: tc.indirect} {
			c := newTestChecker(nil, servers[0].port, "127.0.0.1", "127.0.0.2")
			got, ok := c.Verify(context.Background(), mailengine.DNSFailure{Name: tc.name, QType: "MX"}, "example.jp", time.Now(), direct)
			if !ok || got != want {
				t.Errorf("%s, direct %v: Verify = %q, %v; want %q", tc.name, direct, got, ok, want)
			}
		}
	}
}

// TestVerifyImplicitMX checks the implicit MX: a name without MX records
// exists when it has an address, and is broken when it has none.
func TestVerifyImplicitMX(t *testing.T) {
	db := openTestMasterDB(t)
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("host.example.jp A", data("192.0.2.10"))
	s.resolve("v6.example.jp AAAA", data("2001:db8::10"))
	s.resolve("txt.example.jp TXT", data())
	c := newTestChecker(db, s.port, "127.0.0.1")
	for _, tc := range []struct {
		name string
		want mailengine.DNSOutcome
		note string
	}{
		{"host.example.jp", mailengine.DNSOutcomeExists, "MX none; A ok (192.0.2.10)"},
		{"v6.example.jp", mailengine.DNSOutcomeExists, "MX none; A no data; AAAA ok (2001:db8::10)"},
		{"txt.example.jp", mailengine.DNSOutcomeBroken, "MX none; A no data; AAAA no data"},
	} {
		if got, ok := verify(t, c, tc.name, "MX", "example.jp"); !ok || got != tc.want {
			t.Errorf("%s: Verify = %q, %v; want %q", tc.name, got, ok, tc.want)
		}
	}
	rows := storedChecks(t, db)
	if len(rows) != 3 {
		t.Fatalf("rows %+v", rows)
	}
	for i, note := range []string{"MX none; A ok (192.0.2.10)", "MX none; A no data; AAAA ok (2001:db8::10)", "MX none; A no data; AAAA no data"} {
		if !strings.Contains(rows[i].Detail, "control ok; "+note) {
			t.Errorf("row %d: %s", i, rows[i].Detail)
		}
	}
}

// TestVerifyAddressNoData checks that a failure of type A or AAAA whose
// name exists without records of that type (an IPv4-only host asked for
// AAAA) is exists, whether the resolver or the authoritative server gives
// no data, while a name that does not exist stays not_found.
func TestVerifyAddressNoData(t *testing.T) {
	db := openTestMasterDB(t)
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("v4.example.jp A", data("192.0.2.10"))
	s.resolve("v6.example.jp AAAA", data("2001:db8::10"))
	s.resolve("dual.example.jp AAAA", data("2001:db8::20"))
	withAuthority(s)
	s.authority("v4.example.jp A", data("192.0.2.10"))
	s.authority("v6.example.jp AAAA", data("2001:db8::10"))
	s.authority("dual.example.jp A", data("192.0.2.20")) // the authoritative server has no AAAA of it
	c := newTestChecker(db, s.port, "127.0.0.1")
	for _, tc := range []struct {
		name, qtype string
		want        mailengine.DNSOutcome
		resolver    string
		auth        string
	}{
		{"v4.example.jp", "AAAA", mailengine.DNSOutcomeExists, "AAAA no data (no AAAA record, name exists)", "AAAA no data (no AAAA record, name exists)"},
		{"v6.example.jp", "A", mailengine.DNSOutcomeExists, "A no data (no A record, name exists)", "A no data (no A record, name exists)"},
		{"dual.example.jp", "AAAA", mailengine.DNSOutcomeExists, "AAAA ok (2001:db8::20)", "AAAA no data (no AAAA record, name exists)"},
		{"nosuch.example.jp", "AAAA", mailengine.DNSOutcomeNotFound, "AAAA NXDOMAIN", "AAAA NXDOMAIN"},
	} {
		got, ok := verify(t, c, tc.name, tc.qtype, "example.jp")
		if !ok || got != tc.want {
			t.Errorf("%s %s: Verify = %q, %v; want %q", tc.name, tc.qtype, got, ok, tc.want)
			continue
		}
		row, err := models.FindDNSCheck(db, tc.name, tc.qtype, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		for _, want := range []string{"resolver 127.0.0.1: control ok; " + tc.resolver, "authoritative ns1.example.jp (127.0.0.1): " + tc.auth} {
			if !strings.Contains(row.Detail, want) {
				t.Errorf("%s %s: detail lacks %q:\n%s", tc.name, tc.qtype, want, row.Detail)
			}
		}
	}
}

// TestVerifyOtherRunStoredFirst simulates another job verifying the same
// name at the same time and storing its outcome while this Checker is still
// asking: no second row is stored, and the other run's outcome is returned
// and remembered, so every notice of the window gets the same outcome.
func TestVerifyOtherRunStoredFirst(t *testing.T) {
	db := openTestMasterDB(t)
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("example.jp MX", data("mx1.example.jp"))
	withAuthority(s)
	var once sync.Once
	s.onQuery(func(q string) {
		if q != "auth example.jp MX udp" {
			return
		}
		once.Do(func() {
			if err := models.InsertDNSCheck(db, &models.DNSCheck{Name: "example.jp", QType: "MX", Outcome: "broken", Detail: "other run"}); err != nil {
				t.Error(err)
			}
		})
	})
	c := newTestChecker(db, s.port, "127.0.0.1")
	if got, ok := verify(t, c, "example.jp", "MX", "example.jp"); !ok || got != mailengine.DNSOutcomeBroken {
		t.Fatalf("Verify = %q, %v; want the other run's broken", got, ok)
	}
	rows := storedChecks(t, db)
	if len(rows) != 1 || rows[0].Detail != "other run" {
		t.Errorf("rows %+v", rows)
	}
	before := len(s.received())
	if got, ok := c.Verify(context.Background(), mailengine.DNSFailure{Name: "example.jp", QType: "MX"}, "example.jp", time.Time{}, true); !ok ||
		got != mailengine.DNSOutcomeBroken {
		t.Errorf("second Verify = %q, %v", got, ok)
	}
	if after := len(s.received()); after != before {
		t.Errorf("the second notice made %d queries", after-before)
	}
	if st := c.Stats(); st != (Stats{Reused: 2}) {
		t.Errorf("stats %+v", st)
	}
}

// TestVerifyConcurrentCheckers races two Checkers (two jobs) verifying the
// same name through resolvers that disagree: whichever stores first, both
// return its outcome and one row is stored.
func TestVerifyConcurrentCheckers(t *testing.T) {
	db := openTestMasterDB(t)
	servers := startFakeServers(t, "127.0.0.1", "127.0.0.2")
	servers[0].resolve("example.jp MX", data("mx1.example.jp"))
	// 127.0.0.2 does not know the name: NXDOMAIN.
	checkers := []*Checker{newTestChecker(db, servers[0].port, "127.0.0.1"), newTestChecker(db, servers[0].port, "127.0.0.2")}
	got := make([]mailengine.DNSOutcome, len(checkers))
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i, c := range checkers {
		wg.Go(func() {
			<-start
			got[i], _ = c.Verify(context.Background(), mailengine.DNSFailure{Name: "example.jp", QType: "MX"}, "example.jp", time.Now(), true)
		})
	}
	close(start)
	wg.Wait()
	rows := storedChecks(t, db)
	if len(rows) != 1 {
		t.Fatalf("stored %d rows: %+v", len(rows), rows)
	}
	for i, outcome := range got {
		if string(outcome) != rows[0].Outcome {
			t.Errorf("checker %d returned %q, the stored row says %q", i, outcome, rows[0].Outcome)
		}
	}
}

// TestVerifyApplicability checks that a name outside the organization
// domain of the recipient domain is verified only when it is one of the MX
// targets of the recipient domain, and is never looked up otherwise.
func TestVerifyApplicability(t *testing.T) {
	db := openTestMasterDB(t)
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("customer.jp MX", data("mx.provider.net"))
	s.resolve("mx.provider.net A", data("192.0.2.25"))
	s.resolve("evil.example.net A", data("192.0.2.66"))
	c := newTestChecker(db, s.port, "127.0.0.1")

	if got, ok := verify(t, c, "mx.provider.net", "A", "customer.jp"); !ok || got != mailengine.DNSOutcomeExists {
		t.Fatalf("MX target: Verify = %q, %v", got, ok)
	}
	for _, tc := range []struct{ name, recipient string }{
		{"evil.example.net", "customer.jp"}, // neither the organization domain nor an MX target
		{"evil.example.net", ""},            // no recipient domain
		{"192.0.2.66", "customer.jp"},       // an IP literal is no name
		{"localhost", "customer.jp"},        // a single label
		{"evil.example.net", "not a domain"},
	} {
		if got, ok := verify(t, c, tc.name, "A", tc.recipient); ok || got != "" {
			t.Errorf("%s for %q: Verify = %q, %v", tc.name, tc.recipient, got, ok)
		}
	}
	if q := s.receivedFor("evil.example.net"); len(q) != 0 {
		t.Errorf("a name out of the conditions was looked up: %v", q)
	}
	if q := s.receivedFor("customer.jp"); len(q) != 1 {
		t.Errorf("the MX targets of the recipient domain were looked up %d times: %v", len(q), q)
	}
	if st := c.Stats(); st != (Stats{Verified: 1, NotApplicable: 5}) {
		t.Errorf("stats %+v", st)
	}
}

// TestVerifyControlFailure checks that a resolver that fails its control
// query is left out of the vote, is not asked again in the run, and that
// without any reachable resolver nothing is verified or stored.
func TestVerifyControlFailure(t *testing.T) {
	db := openTestMasterDB(t)
	servers := startFakeServers(t, "127.0.0.1", "127.0.0.2")
	good, bad := servers[0], servers[1]
	good.resolve("example.jp MX", data("mx1.example.jp"))
	good.resolve("www.example.jp A", data("192.0.2.1"))
	bad.resolve(controlName+" A", answerWith(fakeDrop))
	bad.resolve("example.jp MX", answerWith(fakeNX))
	c := newTestChecker(db, good.port, "127.0.0.2", "127.0.0.1")

	if got, ok := verify(t, c, "example.jp", "MX", "example.jp"); !ok || got != mailengine.DNSOutcomeExists {
		t.Fatalf("Verify = %q, %v", got, ok)
	}
	if got, ok := verify(t, c, "www.example.jp", "A", "example.jp"); !ok || got != mailengine.DNSOutcomeExists {
		t.Fatalf("second Verify = %q, %v", got, ok)
	}
	if q := bad.received(); len(q) != 1 || q[0] != "rd example.com A udp" {
		t.Errorf("the unreachable resolver received %v", q)
	}
	rows := storedChecks(t, db)
	if len(rows) != 2 || !strings.Contains(rows[0].Detail, "resolver 127.0.0.2: control failed (timeout); not asked") {
		t.Errorf("rows %+v", rows)
	}

	// No reachable resolver at all: inconclusive, not stored.
	db2 := openTestMasterDB(t)
	c2 := newTestChecker(db2, good.port, "127.0.0.2")
	if got, ok := verify(t, c2, "example.jp", "MX", "example.jp"); ok || got != "" {
		t.Errorf("without a reachable resolver: Verify = %q, %v", got, ok)
	}
	if rows := storedChecks(t, db2); len(rows) != 0 {
		t.Errorf("stored %+v", rows)
	}
}

// TestVerifyInconclusiveNotStored checks that a name every resolver fails
// on is not verified, not stored, and not looked up again in the run.
func TestVerifyInconclusiveNotStored(t *testing.T) {
	db := openTestMasterDB(t)
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("example.jp MX", answerWith(fakeServFail))
	s.resolve("example.jp NS", answerWith(fakeServFail))
	c := newTestChecker(db, s.port, "127.0.0.1")
	for i := 0; i < 2; i++ {
		if got, ok := verify(t, c, "example.jp", "MX", "example.jp"); ok || got != "" {
			t.Fatalf("Verify %d = %q, %v", i, got, ok)
		}
	}
	if rows := storedChecks(t, db); len(rows) != 0 {
		t.Errorf("stored %+v", rows)
	}
	if q := s.receivedFor("example.jp"); len(q) != 2 { // MX and NS of the first call only
		t.Errorf("queries %v", q)
	}
	if st := c.Stats(); st != (Stats{Inconclusive: 2}) {
		t.Errorf("stats %+v", st)
	}
}

// TestVerifyMaxLookups checks the bound on live verifications per run.
func TestVerifyMaxLookups(t *testing.T) {
	db := openTestMasterDB(t)
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("a.example.jp A", data("192.0.2.1"))
	s.resolve("b.example.jp A", data("192.0.2.2"))
	c := New(db, Config{Enabled: true, Resolvers: []string{"127.0.0.1"}, Port: s.port, Timeout: testTimeout, MaxLookups: 1})
	if _, ok := verify(t, c, "a.example.jp", "A", "example.jp"); !ok {
		t.Fatal("first name not verified")
	}
	if got, ok := verify(t, c, "b.example.jp", "A", "example.jp"); ok || got != "" {
		t.Errorf("over the limit: Verify = %q, %v", got, ok)
	}
	if q := s.receivedFor("b.example.jp"); len(q) != 0 {
		t.Errorf("a name over the limit was looked up: %v", q)
	}
	// A reused outcome does not count against the limit.
	if _, ok := verify(t, c, "a.example.jp", "A", "example.jp"); !ok {
		t.Error("reuse over the limit failed")
	}
	if st := c.Stats(); st != (Stats{Verified: 1, Reused: 1, OverBudget: 1}) {
		t.Errorf("stats %+v", st)
	}
}

// TestVerifyReusesStoredOutcome checks the reuse rule: the earliest outcome
// checked from 24 hours before the notice is used without a query, and an
// older one is not (the notice, recent enough, is verified live).
func TestVerifyReusesStoredOutcome(t *testing.T) {
	db := openTestMasterDB(t)
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("old.example.jp MX", data("mx1.example.jp"))
	notice := time.Now().Add(-10 * time.Hour)
	for _, r := range []*models.DNSCheck{
		{Name: "example.jp", QType: "MX", Outcome: "broken", Detail: "earliest", CheckedAt: notice.Add(-23 * time.Hour)},
		{Name: "example.jp", QType: "MX", Outcome: "exists", Detail: "later", CheckedAt: notice.Add(time.Hour)},
		{Name: "old.example.jp", QType: "MX", Outcome: "not_found", Detail: "too old", CheckedAt: notice.Add(-25 * time.Hour)},
	} {
		if err := models.InsertDNSCheck(db, r); err != nil {
			t.Fatal(err)
		}
	}
	c := newTestChecker(db, s.port, "127.0.0.1")
	got, ok := c.Verify(context.Background(), mailengine.DNSFailure{Name: "example.jp", QType: "MX"}, "example.jp", notice, true)
	if !ok || got != mailengine.DNSOutcomeBroken {
		t.Fatalf("Verify = %q, %v", got, ok)
	}
	if q := s.received(); len(q) != 0 {
		t.Errorf("a reused outcome made queries: %v", q)
	}
	got, ok = c.Verify(context.Background(), mailengine.DNSFailure{Name: "old.example.jp", QType: "MX"}, "example.jp", notice, true)
	if !ok || got != mailengine.DNSOutcomeExists {
		t.Fatalf("too old: Verify = %q, %v", got, ok)
	}
	if st := c.Stats(); st != (Stats{Verified: 1, Reused: 1}) {
		t.Errorf("stats %+v", st)
	}
	// A notice dated in the future finds the outcome checked now.
	future := time.Now().Add(72 * time.Hour)
	if got, ok := c.Verify(context.Background(), mailengine.DNSFailure{Name: "old.example.jp", QType: "MX"}, "example.jp", future, true); !ok || got != mailengine.DNSOutcomeExists {
		t.Errorf("future notice: Verify = %q, %v", got, ok)
	}
	if st := c.Stats(); st.Reused != 2 || st.Verified != 1 {
		t.Errorf("future notice: stats %+v", st)
	}
}

// TestVerifyDisabled checks that nothing is queried while the verification
// is off.
func TestVerifyDisabled(t *testing.T) {
	db := openTestMasterDB(t)
	s := startFakeServers(t, "127.0.0.1")[0]
	c := New(db, Config{Enabled: false, Resolvers: []string{"127.0.0.1"}, Port: s.port, Timeout: testTimeout})
	if got, ok := verify(t, c, "example.jp", "MX", "example.jp"); ok || got != "" {
		t.Errorf("Verify = %q, %v", got, ok)
	}
	if addrs, answered := c.ResolveHost(context.Background(), "mail.example.jp"); addrs != nil || answered {
		t.Errorf("ResolveHost = %v, %v; want not answered", addrs, answered)
	}
	if addrs, answered := c.ResolveReporter(context.Background(), "mail.example.jp"); addrs != nil || answered {
		t.Errorf("ResolveReporter = %v, %v; want not answered", addrs, answered)
	}
	if q := s.received(); len(q) != 0 {
		t.Errorf("queries %v", q)
	}
	if st := c.Stats(); st != (Stats{CheckOff: 1}) {
		t.Errorf("stats %+v", st)
	}
}

// TestVerifyStoredOnly checks the Checker of a run with the check off: the
// outcome stored for a notice (the reuse rule) is used, recent notice or
// not, and anything else stays not verified, while no question at all
// reaches a resolver, a name server or the OS resolver; the own host
// names, the reporting MTAs and the SPF records are not looked up.
func TestVerifyStoredOnly(t *testing.T) {
	db := openTestMasterDB(t)
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("example.jp MX", data("mx1.example.jp"))
	s.resolve("example.jp TXT", data("v=spf1 ip4:192.0.2.0/24"))
	withAuthority(s)
	old, recent := time.Now().Add(-72*time.Hour), time.Now().Add(-time.Hour)
	for name, at := range map[string]time.Time{"old.example.jp": old, "recent.example.jp": recent} {
		if err := models.InsertDNSCheck(db, &models.DNSCheck{Name: name, QType: "MX", Outcome: "exists", Detail: "stored",
			CheckedAt: at.Add(-time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	sys := &stubSystem{addrs: map[string][]netip.Addr{"mail.example.jp.": addrs("192.0.2.25")}}
	c := New(db, Config{StoredOnly: true, Resolvers: []string{"127.0.0.1"}, Port: s.port, Timeout: testTimeout})
	c.sys = sys
	c.allowLocal = true
	ctx := context.Background()
	for name, at := range map[string]time.Time{"old.example.jp": old, "recent.example.jp": recent} {
		if got, ok := c.Verify(ctx, mailengine.DNSFailure{Name: name, QType: "MX"}, "example.jp", at, true); !ok || got != mailengine.DNSOutcomeExists {
			t.Errorf("%s, stored: Verify = %q, %v", name, got, ok)
		}
	}
	if got, ok := c.Verify(ctx, mailengine.DNSFailure{Name: "example.jp", QType: "MX"}, "example.jp", time.Now(), true); ok || got != "" {
		t.Errorf("nothing stored: Verify = %q, %v; want not verified", got, ok)
	}
	if addrs, answered := c.ResolveHost(ctx, "mail.example.jp"); addrs != nil || answered {
		t.Errorf("ResolveHost = %v, %v; want not answered", addrs, answered)
	}
	if addrs, answered := c.ResolveReporter(ctx, "mx1.example.jp"); addrs != nil || answered {
		t.Errorf("ResolveReporter = %v, %v; want not answered", addrs, answered)
	}
	if got := c.LookupSPF(ctx, "example.jp"); got.Complete || got.Prefixes != nil {
		t.Errorf("LookupSPF = %+v; want nothing read", got)
	}
	if q := s.received(); len(q) != 0 {
		t.Errorf("queries %v", q)
	}
	if len(sys.asked) != 0 {
		t.Errorf("the OS resolver was asked %v", sys.asked)
	}
	if rows := storedChecks(t, db); len(rows) != 2 {
		t.Errorf("rows %+v: nothing may be stored", rows)
	}
	if st := c.Stats(); st != (Stats{Reused: 2, CheckOff: 1}) {
		t.Errorf("stats %+v", st)
	}
}

// TestVerifyLiveOnlyForRecentNotices checks the age limit of the live
// verification (mailengine.DNSLiveMaxAge, 48 hours by the notice time): an
// older notice uses the outcome stored for it and otherwise stays not
// verified without a single question (not even the control query or the MX
// targets of the recipient domain); a recent one is verified live. The
// limit is inclusive.
func TestVerifyLiveOnlyForRecentNotices(t *testing.T) {
	if mailengine.DNSLiveMaxAge != 48*time.Hour {
		t.Fatalf("DNSLiveMaxAge = %v, want 48h", mailengine.DNSLiveMaxAge)
	}
	db := openTestMasterDB(t)
	s := startFakeServers(t, "127.0.0.1")[0]
	for _, name := range []string{"old.example.jp", "recent.example.jp", "edge-in.example.jp", "edge-out.example.jp", "partner.example.com"} {
		s.resolve(name+" MX", data("mx1."+name))
	}
	threeDays := time.Now().Add(-72 * time.Hour)
	if err := models.InsertDNSCheck(db, &models.DNSCheck{Name: "stored.example.jp", QType: "MX", Outcome: "broken", Detail: "stored",
		CheckedAt: threeDays.Add(-2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	c := newTestChecker(db, s.port, "127.0.0.1")
	ctx := context.Background()
	check := func(name, recipient string, at time.Time) (mailengine.DNSOutcome, bool) {
		return c.Verify(ctx, mailengine.DNSFailure{Name: name, QType: "MX"}, recipient, at, false)
	}

	// Three days old, nothing stored: not verified, no question at all
	// (the name is outside the recipient domain, so the MX targets would be
	// needed too).
	if got, ok := check("old.example.jp", "example.jp", threeDays); ok || got != "" {
		t.Errorf("3 days old, nothing stored: Verify = %q, %v", got, ok)
	}
	if got, ok := check("partner.example.com", "example.jp", threeDays); ok || got != "" {
		t.Errorf("3 days old, another domain: Verify = %q, %v", got, ok)
	}
	if q := s.received(); len(q) != 0 {
		t.Errorf("queries for old notices: %v", q)
	}
	// Three days old, stored inside the window of its time: used.
	if got, ok := check("stored.example.jp", "example.jp", threeDays); !ok || got != mailengine.DNSOutcomeBroken {
		t.Errorf("3 days old, stored: Verify = %q, %v; want the stored broken", got, ok)
	}
	if q := s.received(); len(q) != 0 {
		t.Errorf("queries for a stored outcome: %v", q)
	}
	// One hour old: verified live.
	if got, ok := check("recent.example.jp", "example.jp", time.Now().Add(-time.Hour)); !ok || got != mailengine.DNSOutcomeExists {
		t.Errorf("1 hour old: Verify = %q, %v", got, ok)
	}
	// The limit: 47h59m is live, 48h01m is not.
	if got, ok := check("edge-in.example.jp", "example.jp", time.Now().Add(-48*time.Hour+time.Minute)); !ok || got != mailengine.DNSOutcomeExists {
		t.Errorf("47h59m old: Verify = %q, %v", got, ok)
	}
	if got, ok := check("edge-out.example.jp", "example.jp", time.Now().Add(-48*time.Hour-time.Minute)); ok || got != "" {
		t.Errorf("48h01m old: Verify = %q, %v", got, ok)
	}
	if q := s.receivedFor("edge-out.example.jp"); len(q) != 0 {
		t.Errorf("queries for the notice past the limit: %v", q)
	}
	// The window ends 48 hours after the notice: an old notice about a name
	// verified live for a recent one in the meantime (stored, and remembered
	// by this Checker) does not use that outcome and asks nothing.
	before := len(s.received())
	if got, ok := check("recent.example.jp", "example.jp", threeDays); ok || got != "" {
		t.Errorf("3 days old after a live verification: Verify = %q, %v; want not verified", got, ok)
	}
	if q := s.received(); len(q) != before {
		t.Errorf("queries for the old notice: %v", q[before:])
	}
	if st := c.Stats(); st != (Stats{Verified: 2, Reused: 1, TooOld: 4}) {
		t.Errorf("stats %+v", st)
	}
}

// TestVerifyOwnRowLater checks a notice verified live late in its window:
// the row checked 30 hours after the notice is its own and is reused when
// the notice is grouped again days later, without a query, while a new
// Checker for a notice of today does not take it (it lies before that
// window) and verifies live.
func TestVerifyOwnRowLater(t *testing.T) {
	db := openTestMasterDB(t)
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("example.jp MX", data("mx1.example.jp"))
	notice := time.Now().Add(-5 * 24 * time.Hour).UTC().Truncate(time.Second)
	failure := mailengine.DNSFailure{Name: "example.jp", QType: "MX"}
	ctx := context.Background()

	first := newTestChecker(db, s.port, "127.0.0.1")
	first.now = func() time.Time { return notice.Add(30 * time.Hour) }
	if got, status := first.VerifyStatus(ctx, failure, "example.jp", notice, true); got != mailengine.DNSOutcomeExists || status != mailengine.DNSStatusVerified {
		t.Fatalf("30 hours old: VerifyStatus = %q, %v", got, status)
	}
	rows := storedChecks(t, db)
	if len(rows) != 1 {
		t.Fatalf("rows %+v", rows)
	}
	asked := len(s.received())

	again := newTestChecker(db, s.port, "127.0.0.1")
	if got, status := again.VerifyStatus(ctx, failure, "example.jp", notice, true); got != mailengine.DNSOutcomeExists || status != mailengine.DNSStatusReused {
		t.Errorf("5 days old, own row: VerifyStatus = %q, %v; want the stored exists", got, status)
	}
	if q := s.received(); len(q) != asked {
		t.Errorf("queries for a reused outcome: %v", q[asked:])
	}
	if got, status := again.VerifyStatus(ctx, failure, "example.jp", time.Now(), true); got != mailengine.DNSOutcomeExists || status != mailengine.DNSStatusVerified {
		t.Errorf("a notice of today: VerifyStatus = %q, %v; want a live verification", got, status)
	}
	if rows := storedChecks(t, db); len(rows) != 2 {
		t.Errorf("rows %+v, want a second one for today's notice", rows)
	}
}

// TestVerifyStoreClampedToWindow checks a live verification that ends after
// the window of its notice (a notice exactly 48 hours old when the check
// started): the row is dated at the end of the window, so that grouping the
// notice again finds it.
func TestVerifyStoreClampedToWindow(t *testing.T) {
	db := openTestMasterDB(t)
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("example.jp MX", data("mx1.example.jp"))
	start := time.Now().UTC().Truncate(time.Second)
	notice := start.Add(-mailengine.DNSLiveMaxAge)
	c := newTestChecker(db, s.port, "127.0.0.1")
	calls := 0
	c.now = func() time.Time {
		calls++
		if calls == 1 {
			return start // the age gate and the window
		}
		return start.Add(5 * time.Second) // the check took five seconds
	}
	failure := mailengine.DNSFailure{Name: "example.jp", QType: "MX"}
	if got, status := c.VerifyStatus(context.Background(), failure, "example.jp", notice, true); got != mailengine.DNSOutcomeExists || status != mailengine.DNSStatusVerified {
		t.Fatalf("VerifyStatus = %q, %v", got, status)
	}
	from, to := ReuseRange(notice, start)
	row, err := models.FindDNSCheck(db, "example.jp", "MX", from, to)
	if err != nil || !row.CheckedAt.Equal(to) || row.Outcome != "exists" {
		t.Errorf("stored row %+v, %v; want one checked at %v", row, err, to)
	}
}

// TestVerifySendmailForm checks that a failure without a type (the
// Sendmail forms) is verified and stored as MX, and that an unknown type is
// not verified.
func TestVerifySendmailForm(t *testing.T) {
	db := openTestMasterDB(t)
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("example.jp MX", data("mx1.example.jp"))
	c := newTestChecker(db, s.port, "127.0.0.1")
	if got, ok := verify(t, c, "example.jp", "", "example.jp"); !ok || got != mailengine.DNSOutcomeExists {
		t.Fatalf("Verify = %q, %v", got, ok)
	}
	if rows := storedChecks(t, db); len(rows) != 1 || rows[0].QType != "MX" {
		t.Errorf("rows %+v", rows)
	}
	if got, ok := verify(t, c, "example.jp", "TXT", "example.jp"); ok || got != "" {
		t.Errorf("TXT: Verify = %q, %v", got, ok)
	}
}

// TestVerifyTransport checks the client: the TCP fallback after a
// truncated UDP reply, and replies with another ID or question ignored.
func TestVerifyTransport(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode fakeMode
		tcp  bool
	}{
		{"truncated", fakeTruncate, true},
		{"wrong id", fakeWrongID, false},
		{"wrong question", fakeWrongQuestion, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestMasterDB(t)
			s := startFakeServers(t, "127.0.0.1")[0]
			s.resolve("example.jp MX", answerWith(tc.mode, "mx1.example.jp"))
			c := newTestChecker(db, s.port, "127.0.0.1")
			if got, ok := verify(t, c, "example.jp", "MX", "example.jp"); !ok || got != mailengine.DNSOutcomeExists {
				t.Fatalf("Verify = %q, %v", got, ok)
			}
			if got := containsQuery(s.received(), "rd example.jp MX tcp"); got != tc.tcp {
				t.Errorf("asked over TCP: %v (%v)", got, s.received())
			}
		})
	}
}

// TestVerifyTruncatedWithoutTCP checks that a truncated reply whose TCP
// exchange fails counts as a failure of the resolver.
func TestVerifyTruncatedWithoutTCP(t *testing.T) {
	db := openTestMasterDB(t)
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("example.jp MX", answerWith(fakeTruncate, "mx1.example.jp"))
	s.tcp.Close()
	c := newTestChecker(db, s.port, "127.0.0.1")
	if got, ok := verify(t, c, "example.jp", "MX", "example.jp"); ok || got != "" {
		t.Errorf("Verify = %q, %v", got, ok)
	}
}

// TestVerifyCanceled checks that a canceled context verifies nothing and
// does not mark the resolver unreachable.
func TestVerifyCanceled(t *testing.T) {
	db := openTestMasterDB(t)
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("example.jp MX", data("mx1.example.jp"))
	c := newTestChecker(db, s.port, "127.0.0.1")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, ok := c.Verify(ctx, mailengine.DNSFailure{Name: "example.jp", QType: "MX"}, "example.jp", time.Now(), true); ok || got != "" {
		t.Errorf("canceled: Verify = %q, %v", got, ok)
	}
	if got, ok := verify(t, c, "example.jp", "MX", "example.jp"); !ok || got != mailengine.DNSOutcomeExists {
		t.Errorf("after the cancellation: Verify = %q, %v", got, ok)
	}
}

// TestResolveHost checks the lookup of our own host names: A and AAAA,
// remembered for the run; a name without addresses (NXDOMAIN, or an
// IPv4-only host) is answered, what is no host name is not.
func TestResolveHost(t *testing.T) {
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("mail.example.jp A", data("192.0.2.25"))
	s.resolve("mail.example.jp AAAA", data("2001:db8::25"))
	s.resolve("v4only.example.jp A", data("192.0.2.26"))
	c := newTestChecker(nil, s.port, "127.0.0.1")
	want := []netip.Addr{netip.MustParseAddr("192.0.2.25"), netip.MustParseAddr("2001:db8::25")}
	for i := 0; i < 2; i++ {
		got, answered := c.ResolveHost(context.Background(), "Mail.Example.JP.")
		if len(got) != 2 || got[0] != want[0] || got[1] != want[1] || !answered {
			t.Errorf("call %d: ResolveHost = %v, %v", i, got, answered)
		}
	}
	if q := s.receivedFor("mail.example.jp"); len(q) != 2 {
		t.Errorf("queries %v", q)
	}
	if got, answered := c.ResolveHost(context.Background(), "v4only.example.jp"); len(got) != 1 || !answered {
		t.Errorf("IPv4-only host: ResolveHost = %v, %v", got, answered)
	}
	if got, answered := c.ResolveHost(context.Background(), "unknown.example.jp"); got != nil || !answered {
		t.Errorf("unknown: ResolveHost = %v, %v; want answered without addresses", got, answered)
	}
	for _, host := range []string{"192.0.2.25", "", "bad host"} {
		if got, answered := c.ResolveHost(context.Background(), host); got != nil || answered {
			t.Errorf("ResolveHost(%q) = %v, %v", host, got, answered)
		}
	}
}

// TestResolveHostHalfAnswered: a lookup of an own host name is answered
// only when both the A and the AAAA question are (a SERVFAIL or no reply
// for one of them leaves it unanswered, with the addresses the other gave);
// the next resolver is asked then, and one that answers both decides.
func TestResolveHostHalfAnswered(t *testing.T) {
	for _, c := range []struct {
		name       string
		a, aaaa    fakeAnswer
		want       []netip.Addr
		answered   bool
		secondTime bool // the second resolver answers both questions
	}{
		{"A ok, AAAA SERVFAIL", data("192.0.2.25"), answerWith(fakeServFail), addrs("192.0.2.25"), false, false},
		{"A ok, AAAA no reply", data("192.0.2.25"), answerWith(fakeDrop), addrs("192.0.2.25"), false, false},
		{"A SERVFAIL, AAAA ok", answerWith(fakeServFail), data("2001:db8::25"), addrs("2001:db8::25"), false, false},
		{"A SERVFAIL, AAAA no data", answerWith(fakeServFail), answerWith(fakeNoData), nil, false, false},
		{"A ok, AAAA no data", data("192.0.2.25"), answerWith(fakeNoData), addrs("192.0.2.25"), true, false},
		{"A ok, AAAA SERVFAIL, the next resolver answers both", data("192.0.2.25"), answerWith(fakeServFail),
			addrs("192.0.2.25", "2001:db8::25"), true, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			servers := startFakeServers(t, "127.0.0.1", "127.0.0.2")
			servers[0].resolve("mail.example.jp A", c.a)
			servers[0].resolve("mail.example.jp AAAA", c.aaaa)
			resolvers := []string{"127.0.0.1"}
			if c.secondTime {
				servers[1].resolve("mail.example.jp A", data("192.0.2.25"))
				servers[1].resolve("mail.example.jp AAAA", data("2001:db8::25"))
				resolvers = append(resolvers, "127.0.0.2")
			}
			chk := newTestChecker(nil, servers[0].port, resolvers...)
			got, answered := chk.ResolveHost(context.Background(), "mail.example.jp")
			if fmt.Sprint(got) != fmt.Sprint(c.want) || answered != c.answered {
				t.Errorf("ResolveHost = %v, %v; want %v, %v", got, answered, c.want, c.answered)
			}
		})
	}
}

// addrs parses addresses.
func addrs(s ...string) []netip.Addr {
	out := make([]netip.Addr, len(s))
	for i, a := range s {
		out[i] = netip.MustParseAddr(a)
	}
	return out
}

// TestSystemResolver runs the OS resolver mode with a Go resolver that
// sends its queries to a fake server (no network needed).
func TestSystemResolver(t *testing.T) {
	db := openTestMasterDB(t)
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve(controlName+" AAAA", data())
	s.resolve("example.jp MX", data("mx1.example.jp"))
	s.resolve("host.example.jp A", data("192.0.2.10"))
	withAuthority(s)
	s.authority("host.example.jp A", data("192.0.2.10"))
	c := New(db, Config{Enabled: true, SystemOnly: true, Resolvers: []string{"192.0.2.53"}, Port: s.port, Timeout: time.Second})
	c.allowLocal = true
	c.sys = goResolverTo(s)
	skipWithoutGoResolver(t, c, s)

	for _, tc := range []struct {
		name string
		want mailengine.DNSOutcome
		note string
	}{
		{"example.jp", mailengine.DNSOutcomeExists, "resolver system: control ok; MX ok (mx1.example.jp)"},
		{"host.example.jp", mailengine.DNSOutcomeExists, "MX not found (NXDOMAIN or no data); address ok (192.0.2.10)"},
		{"nosuch.example.jp", mailengine.DNSOutcomeNotFound, "address not found (NXDOMAIN or no data)"},
	} {
		got, ok := verify(t, c, tc.name, "MX", "example.jp")
		if !ok || got != tc.want {
			t.Errorf("%s: Verify = %q, %v; want %q", tc.name, got, ok, tc.want)
			continue
		}
		row, err := models.FindDNSCheck(db, tc.name, "MX", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
		if err != nil || !strings.Contains(row.Detail, tc.note) {
			t.Errorf("%s: %v %+v", tc.name, err, row)
		}
	}
	// No AAAA record (Go reports it as not found): an address of the other
	// family shows that the name exists.
	if got, ok := verify(t, c, "host.example.jp", "AAAA", "example.jp"); !ok || got != mailengine.DNSOutcomeExists {
		t.Errorf("AAAA of an IPv4-only host: Verify = %q, %v", got, ok)
	} else if row, err := models.FindDNSCheck(db, "host.example.jp", "AAAA", time.Now().Add(-time.Hour), time.Now().Add(time.Hour)); err != nil ||
		!strings.Contains(row.Detail, "AAAA not found (NXDOMAIN or no data); address ok (192.0.2.10) (no AAAA record, name exists)") ||
		!strings.Contains(row.Detail, "authoritative: not asked (only the OS resolver is used)") {
		t.Errorf("AAAA of an IPv4-only host: %v %+v", err, row)
	}
	// Only the OS resolver is contacted: no authoritative server directly,
	// even for a notice that clearly concerns our own mail.
	for _, q := range s.received() {
		if strings.HasPrefix(q, "auth ") {
			t.Errorf("an authoritative server was asked in the OS resolver mode: %q", q)
		}
	}
	// An IPv4-only host: the AAAA question is answered without data.
	if addrs, answered := c.ResolveHost(context.Background(), "host.example.jp"); len(addrs) != 1 || addrs[0] != netip.MustParseAddr("192.0.2.10") || !answered {
		t.Errorf("ResolveHost = %v, %v", addrs, answered)
	}
	if len(c.paths) != 1 || c.paths[0].label != "system" {
		t.Errorf("the configured resolvers are used: %+v", c.paths)
	}

	// The control query fails: nothing is verified.
	s.resolve(controlName+" A", answerWith(fakeServFail))
	s.resolve(controlName+" AAAA", answerWith(fakeServFail))
	c2 := New(db, Config{Enabled: true, SystemOnly: true, Port: s.port, Timeout: time.Second})
	c2.sys = goResolverTo(s)
	if got, ok := verify(t, c2, "other.example.jp", "MX", "example.jp"); ok || got != "" {
		t.Errorf("control failed: Verify = %q, %v", got, ok)
	}
}

// TestNormalize checks the validation of names and types.
func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"Example.JP.":                   "example.jp",
		" mx-1.example.jp":              "mx-1.example.jp",
		"example":                       "",
		"192.0.2.1":                     "",
		"-a.example.jp":                 "",
		"a_b.example.jp":                "",
		"a..example.jp":                 "",
		strings.Repeat("a", 64) + ".jp": "",
		strings.Repeat("abcdefgh.", 28) + "example.jp": "",
	} {
		got, ok := normalizeName(in)
		if got != want || ok != (want != "") {
			t.Errorf("normalizeName(%q) = %q, %v", in, got, ok)
		}
	}
	for in, want := range map[string]string{"": "MX", "mx": "MX", "A": "A", "aaaa": "AAAA", "TXT": "", "NS": ""} {
		got, ok := normalizeQType(in)
		if got != want || ok != (want != "") {
			t.Errorf("normalizeQType(%q) = %q, %v", in, got, ok)
		}
	}
}

// TestServerAllowed checks that authoritative servers are asked only at
// global unicast addresses outside the special-purpose ranges (an
// IPv4-mapped address is judged unmapped), and that allowLocal lets the
// tests' loopback servers through.
func TestServerAllowed(t *testing.T) {
	c := New(nil, Config{})
	for in, want := range map[string]bool{
		// Public addresses.
		"1.1.1.1": true, "8.8.8.8": true, "203.0.112.255": true, "198.51.99.1": true, "100.128.0.1": true,
		"2001:4860:4860::8888": true, "2606:4700:4700::1111": true, "::ffff:8.8.8.8": true,
		// Ranges netip classifies.
		"127.0.0.1": false, "10.0.0.1": false, "172.16.0.1": false, "192.168.1.1": false, "169.254.0.1": false,
		"224.0.0.1": false, "0.0.0.0": false, "::1": false, "::": false, "ff02::1": false, "::ffff:10.0.0.1": false,
		// Special-purpose ranges.
		"0.1.2.3":          false, // 0.0.0.0/8
		"100.64.0.1":       false, // 100.64.0.0/10
		"100.127.255.254":  false,
		"192.0.0.8":        false, // 192.0.0.0/24
		"192.0.2.1":        false, // 192.0.2.0/24
		"198.18.0.1":       false, // 198.18.0.0/15
		"198.19.255.254":   false,
		"198.51.100.1":     false, // 198.51.100.0/24
		"203.0.113.1":      false, // 203.0.113.0/24
		"240.0.0.1":        false, // 240.0.0.0/4
		"255.255.255.254":  false,
		"255.255.255.255":  false, // 255.255.255.255/32
		"fc00::1":          false, // fc00::/7
		"fd12:3456::1":     false,
		"fe80::1":          false, // fe80::/10
		"febf::1":          false,
		"64:ff9b::808:808": false, // 64:ff9b::/96
		"64:ff9b:1::1":     false, // 64:ff9b:1::/48
		"2002:c000:201::1": false, // 2002::/16
		"2001:0:4136::1":   false, // 2001::/32
		"2001:db8::1":      false, // 2001:db8::/32
		"100::1":           false, // 100::/64
	} {
		if got := c.serverAllowed(netip.MustParseAddr(in)); got != want {
			t.Errorf("serverAllowed(%s) = %v, want %v", in, got, want)
		}
	}
	if got := c.serverAllowed(netip.MustParseAddr("fe80::1%eth0")); got {
		t.Error("an address with a zone is allowed")
	}
	if got := c.usableServerAddrs([]netip.Addr{netip.MustParseAddr("2001:4860:4860::8888"), netip.MustParseAddr("10.0.0.1"),
		netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("2001:db8::1"), netip.MustParseAddr("8.8.8.8")}); len(got) != 2 ||
		got[0] != netip.MustParseAddr("1.1.1.1") || got[1] != netip.MustParseAddr("8.8.8.8") {
		t.Errorf("usableServerAddrs = %v", got)
	}
	local := New(nil, Config{})
	local.allowLocal = true
	if !local.serverAllowed(netip.MustParseAddr("127.0.0.1")) {
		t.Error("allowLocal does not let a loopback server through")
	}
}

// TestFindDNSCheckNoRows keeps the contract the Checker relies on.
func TestFindDNSCheckNoRows(t *testing.T) {
	db := openTestMasterDB(t)
	if _, err := models.FindDNSCheck(db, "example.jp", "MX", time.Now(), time.Now().Add(time.Hour)); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("err %v", err)
	}
}
