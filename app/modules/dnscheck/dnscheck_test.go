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
// mail from the sending sources (direct: the authoritative servers are asked too).
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

// mailHost gives a host name addresses in the resolver role of s.
func mailHost(s *fakeServer, host string, a ...string) {
	for _, v := range a {
		t := "A"
		if netip.MustParseAddr(v).Is6() {
			t = "AAAA"
		}
		s.resolve(host+" "+t, data(v))
	}
}

// TestVerifyExists verifies a name every path answers: exists, stored with
// the evidence of the lookups (the MX targets and the A and AAAA of each)
// and of the authoritative server, and reused by the next notice without a
// query.
func TestVerifyExists(t *testing.T) {
	db := openTestMasterDB(t)
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("example.jp MX", data("mx1.example.jp", "mx2.example.jp"))
	mailHost(s, "mx1.example.jp", "192.0.2.1")
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
	if r.Name != "example.jp" || r.QType != VerifyKind || r.Outcome != "exists" {
		t.Errorf("row %+v", r)
	}
	for _, want := range []string{
		"name=example.jp outcome=exists",
		"checked as a mail domain: MX, then the A and AAAA of every MX target (or of the name itself without MX)",
		"resolver 127.0.0.1: control ok; MX ok (mx1.example.jp, mx2.example.jp); mx1.example.jp: A ok (192.0.2.1), AAAA no data; " +
			"mx2.example.jp: A NXDOMAIN, AAAA NXDOMAIN",
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
	if got, ok := verify(t, c, "example.jp", "A", "example.jp"); !ok || got != mailengine.DNSOutcomeExists {
		t.Fatalf("second Verify (another record type) = %q, %v", got, ok)
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

// TestVerifyMailDomain checks the verification of a mail domain through the
// configured resolvers: every MX target is looked up for both A and AAAA,
// and the domain exists when any target has an address (every combination
// of A and AAAA per target of a domain with two targets); without any
// address it is broken, a null MX (RFC 7505) is broken, a domain without MX
// records is the mail host itself (RFC 5321), and NXDOMAIN is not_found.
func TestVerifyMailDomain(t *testing.T) {
	s := startFakeServers(t, "127.0.0.1")[0]
	// The states of one MX target.
	type target struct {
		name  string
		addrs []string // "" in the list: exists without an address
	}
	states := []target{
		{"both", []string{"192.0.2.1", "2001:db8::1"}},
		{"a", []string{"192.0.2.2"}},
		{"aaaa", []string{"2001:db8::3"}},
		{"none", nil},
		{"nx", nil},
	}
	hostOf := func(st target, domain string) string { return st.name + "." + domain }
	setTarget := func(st target, domain string) {
		h := hostOf(st, domain)
		switch st.name {
		case "none":
			s.resolve(h+" TXT", data("no address")) // the name exists without A and AAAA
		case "nx":
		default:
			mailHost(s, h, st.addrs...)
		}
	}
	hasAddress := func(st target) bool { return len(st.addrs) > 0 }
	type want struct {
		domain  string
		outcome mailengine.DNSOutcome
	}
	var cases []want
	for i, x := range states {
		for j, y := range states {
			domain := fmt.Sprintf("c%d%d.example.jp", i, j)
			s.resolve(domain+" MX", data(hostOf(x, domain), hostOf(y, domain)))
			setTarget(x, domain)
			setTarget(y, domain)
			outcome := mailengine.DNSOutcomeBroken
			if hasAddress(x) || hasAddress(y) {
				outcome = mailengine.DNSOutcomeExists
			}
			cases = append(cases, want{domain, outcome})
		}
		// One target alone.
		domain := fmt.Sprintf("s%d.example.jp", i)
		s.resolve(domain+" MX", data(hostOf(x, domain)))
		setTarget(x, domain)
		outcome := mailengine.DNSOutcomeBroken
		if hasAddress(x) {
			outcome = mailengine.DNSOutcomeExists
		}
		cases = append(cases, want{domain, outcome})
	}
	s.resolve("nullmx.example.jp MX", data(""))
	s.resolve("implicit.example.jp A", data("192.0.2.9"))
	s.resolve("implicit6.example.jp AAAA", data("2001:db8::9"))
	s.resolve("noaddr.example.jp TXT", data("nothing to deliver to"))
	cases = append(cases,
		want{"nullmx.example.jp", mailengine.DNSOutcomeBroken},
		want{"implicit.example.jp", mailengine.DNSOutcomeExists},
		want{"implicit6.example.jp", mailengine.DNSOutcomeExists},
		want{"noaddr.example.jp", mailengine.DNSOutcomeBroken},
		want{"nosuch.example.jp", mailengine.DNSOutcomeNotFound},
	)
	db := openTestMasterDB(t)
	c := newTestChecker(db, s.port, "127.0.0.1")
	for _, tc := range cases {
		got, ok := c.Verify(context.Background(), mailengine.DNSFailure{Name: tc.domain, QType: "MX"}, tc.domain, time.Now(), false)
		if !ok || got != tc.outcome {
			t.Errorf("%s: Verify = %q, %v; want %q", tc.domain, got, ok, tc.outcome)
		}
	}
	detail := func(name string) string {
		t.Helper()
		row, err := models.FindDNSCheck(db, name, VerifyKind, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), false)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return row.Detail
	}
	for name, want := range map[string]string{
		"c03.example.jp": "MX ok (both.c03.example.jp, none.c03.example.jp); both.c03.example.jp: A ok (192.0.2.1), AAAA ok (2001:db8::1); " +
			"none.c03.example.jp: A no data, AAAA no data",
		"c34.example.jp":       "MX ok (none.c34.example.jp, nx.c34.example.jp); none.c34.example.jp: A no data, AAAA no data; nx.c34.example.jp: A NXDOMAIN, AAAA NXDOMAIN",
		"nullmx.example.jp":    "MX ok (.) (null MX: the domain accepts no mail)",
		"implicit.example.jp":  "MX none (the name itself is the mail host); A ok (192.0.2.9), AAAA no data",
		"implicit6.example.jp": "MX none (the name itself is the mail host); A no data, AAAA ok (2001:db8::9)",
		"noaddr.example.jp":    "MX none (the name itself is the mail host); A no data, AAAA no data",
	} {
		if d := detail(name); !strings.Contains(d, "resolver 127.0.0.1: control ok; "+want) {
			t.Errorf("%s: detail lacks %q:\n%s", name, want, d)
		}
	}
}

// TestVerifyHost checks the verification of a host name (an MX target of
// the recipient domain): its A and AAAA records both; it exists with an
// address of either family, whatever record type the notice named (a notice
// "type=AAAA: Host found but no data record" for a host with an A record
// is exists), and is broken without any and not_found when it does not
// exist. The authoritative servers are asked whether it exists (A; no
// data counts as existing).
func TestVerifyHost(t *testing.T) {
	db := openTestMasterDB(t)
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("example.jp MX", data("v4.example.jp", "v6.example.jp", "dual.example.jp", "none.example.jp", "nosuch.example.jp"))
	mailHost(s, "v4.example.jp", "192.0.2.10")
	mailHost(s, "v6.example.jp", "2001:db8::10")
	mailHost(s, "dual.example.jp", "192.0.2.20", "2001:db8::20")
	s.resolve("none.example.jp TXT", data("no address"))
	withAuthority(s)
	s.authority("v4.example.jp A", data("192.0.2.10"))
	s.authority("v6.example.jp AAAA", data("2001:db8::10"))
	s.authority("dual.example.jp A", data("192.0.2.20"))
	s.authority("none.example.jp TXT", data("no address"))
	c := newTestChecker(db, s.port, "127.0.0.1")
	for _, tc := range []struct {
		name, qtype string
		want        mailengine.DNSOutcome
		resolver    string
		auth        string
	}{
		{"v4.example.jp", "AAAA", mailengine.DNSOutcomeExists, "A ok (192.0.2.10), AAAA no data", "A ok (192.0.2.10)"},
		{"v6.example.jp", "A", mailengine.DNSOutcomeExists, "A no data, AAAA ok (2001:db8::10)", "A no data"},
		{"dual.example.jp", "AAAA", mailengine.DNSOutcomeExists, "A ok (192.0.2.20), AAAA ok (2001:db8::20)", "A ok (192.0.2.20)"},
		{"none.example.jp", "A", mailengine.DNSOutcomeBroken, "A no data, AAAA no data", "A no data"},
		{"nosuch.example.jp", "AAAA", mailengine.DNSOutcomeNotFound, "A NXDOMAIN, AAAA NXDOMAIN", "A NXDOMAIN"},
	} {
		got, ok := verify(t, c, tc.name, tc.qtype, "example.jp")
		if !ok || got != tc.want {
			t.Errorf("%s %s: Verify = %q, %v; want %q", tc.name, tc.qtype, got, ok, tc.want)
			continue
		}
		row, err := models.FindDNSCheck(db, tc.name, VerifyKind, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), false)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		for _, want := range []string{
			"checked as a host (an MX target of the recipient domain): A and AAAA",
			"resolver 127.0.0.1: control ok; " + tc.resolver,
			"authoritative ns1.example.jp (127.0.0.1): " + tc.auth,
		} {
			if !strings.Contains(row.Detail, want) {
				t.Errorf("%s %s: detail lacks %q:\n%s", tc.name, tc.qtype, want, row.Detail)
			}
		}
	}
}

// TestVerifyAuthoritativeVote checks the authoritative answers: NXDOMAIN
// against resolvers that obtained an address is broken, an answer that the
// name exists (records, no data, a CNAME) agrees, and a server that fails,
// does not answer or is not authoritative for the name does not vote.
func TestVerifyAuthoritativeVote(t *testing.T) {
	for _, tc := range []struct {
		name string
		auth fakeAnswer
		want mailengine.DNSOutcome
		note string
	}{
		{"nxdomain", answerWith(fakeNX), mailengine.DNSOutcomeBroken, "MX NXDOMAIN"},
		{"servfail", answerWith(fakeServFail), mailengine.DNSOutcomeExists, "MX SERVFAIL"},
		{"nodata", answerWith(fakeNoData), mailengine.DNSOutcomeExists, "MX no data"},
		{"not authoritative", answerWith(fakeNotAuth, "mx1.example.jp"), mailengine.DNSOutcomeExists, "MX not authoritative for the name"},
		{"cname", data("cname:mail.example.net"), mailengine.DNSOutcomeExists, "MX CNAME mail.example.net"},
		{"drop", answerWith(fakeDrop), mailengine.DNSOutcomeExists, "MX timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestMasterDB(t)
			s := startFakeServers(t, "127.0.0.1")[0]
			s.resolve("example.jp MX", data("mx1.example.jp"))
			mailHost(s, "mx1.example.jp", "192.0.2.1")
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
// address that did not answer is not asked again for failureTTL
// (RFC 2308 7.2), and is asked again after it.
func TestVerifySilentAuthoritativeNotAskedAgain(t *testing.T) {
	db := openTestMasterDB(t)
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("example.jp MX", data("mx1.example.jp"))
	mailHost(s, "mx1.example.jp", "192.0.2.1")
	mailHost(s, "www.example.jp", "192.0.2.1")
	mailHost(s, "mail.example.jp", "192.0.2.1")
	withAuthority(s)
	s.authority("example.jp MX", answerWith(fakeDrop))
	s.authority("www.example.jp MX", answerWith(fakeNoData))
	s.authority("mail.example.jp MX", answerWith(fakeNoData))
	c := newTestChecker(db, s.port, "127.0.0.1")
	start := time.Now()
	now := start
	c.now = func() time.Time { return now }
	if got, ok := verify(t, c, "example.jp", "MX", "example.jp"); !ok || got != mailengine.DNSOutcomeExists {
		t.Fatalf("Verify = %q, %v", got, ok)
	}
	now = start.Add(failureTTL - time.Second)
	if got, ok := verify(t, c, "www.example.jp", "A", "example.jp"); !ok || got != mailengine.DNSOutcomeExists {
		t.Fatalf("second Verify = %q, %v", got, ok)
	}
	if q := s.receivedFor("www.example.jp"); containsQuery(q, "auth www.example.jp MX udp") {
		t.Errorf("the silent authoritative server was asked again within the TTL: %v", q)
	}
	rows := storedChecks(t, db)
	if len(rows) != 2 || !strings.Contains(rows[1].Detail, "no response in the last 5 minutes") {
		t.Errorf("rows %+v", rows)
	}
	now = start.Add(failureTTL)
	if got, ok := verify(t, c, "mail.example.jp", "A", "example.jp"); !ok || got != mailengine.DNSOutcomeExists {
		t.Fatalf("third Verify = %q, %v", got, ok)
	}
	if q := s.receivedFor("mail.example.jp"); !containsQuery(q, "auth mail.example.jp MX udp") {
		t.Errorf("the authoritative server was not asked again after the TTL: %v", q)
	}
}

// TestVerifySplitResolvers checks that resolvers that disagree give broken.
func TestVerifySplitResolvers(t *testing.T) {
	db := openTestMasterDB(t)
	servers := startFakeServers(t, "127.0.0.1", "127.0.0.2")
	servers[0].resolve("example.jp MX", data("mx1.example.jp"))
	mailHost(servers[0], "mx1.example.jp", "192.0.2.1")
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

// TestVerifyOneResolverFailing checks that a resolver that fails (SERVFAIL,
// no reply) while the others answer is left out of the vote: the others
// decide (exists, not_found); only when every resolver fails is the name
// broken (DNS does not answer normally for it), stored and reused.
func TestVerifyOneResolverFailing(t *testing.T) {
	servers := startFakeServers(t, "127.0.0.1", "127.0.0.2", "127.0.0.3")
	for _, s := range servers[:2] {
		s.resolve("example.jp MX", data("mx1.example.jp"))
		mailHost(s, "mx1.example.jp", "192.0.2.1")
	}
	servers[2].resolve("example.jp MX", answerWith(fakeServFail))
	servers[1].resolve("gone.example.jp MX", answerWith(fakeDrop))
	for _, s := range servers {
		s.resolve("dead.example.jp MX", answerWith(fakeServFail))
	}
	db := openTestMasterDB(t)
	c := newTestChecker(db, servers[0].port, "127.0.0.1", "127.0.0.2", "127.0.0.3")
	for _, tc := range []struct {
		name string
		want mailengine.DNSOutcome
	}{
		{"example.jp", mailengine.DNSOutcomeExists},
		{"gone.example.jp", mailengine.DNSOutcomeNotFound},
		{"dead.example.jp", mailengine.DNSOutcomeBroken},
	} {
		got, ok := c.Verify(context.Background(), mailengine.DNSFailure{Name: tc.name, QType: "MX"}, "example.jp", time.Now(), false)
		if !ok || got != tc.want {
			t.Errorf("%s: Verify = %q, %v; want %q", tc.name, got, ok, tc.want)
		}
	}
	rows := storedChecks(t, db)
	if len(rows) != 3 || !strings.Contains(rows[0].Detail, "resolver 127.0.0.3: control ok; MX SERVFAIL") {
		t.Errorf("rows %+v", rows)
	}
	before := len(servers[0].received())
	if got, ok := c.Verify(context.Background(), mailengine.DNSFailure{Name: "dead.example.jp", QType: "MX"}, "example.jp", time.Now(), false); !ok ||
		got != mailengine.DNSOutcomeBroken {
		t.Errorf("dead.example.jp again: Verify = %q, %v", got, ok)
	}
	if q := servers[0].received(); len(q) != before {
		t.Errorf("a stored broken outcome made queries: %v", q[before:])
	}
}

// TestVerifyIndirect: a notice that does not clearly concern mail from the sending sources
// (direct false; a forged one names a domain and servers of its author's
// choosing) is verified through the resolvers alone: neither the name
// servers of the domain are looked up nor the authoritative server at the
// address they give is asked, and the evidence says why. With direct the
// same name reaches the authoritative server.
func TestVerifyIndirect(t *testing.T) {
	servers := startFakeServers(t, "127.0.0.1", "127.0.0.2")
	resolver, attacker := servers[0], servers[1]
	resolver.resolve("evil.example MX", data("mx.evil.example"))
	mailHost(resolver, "mx.evil.example", "192.0.2.66")
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
		!strings.Contains(rows[0].Detail, "authoritative: not asked (the notice does not show mail sent from the sending sources)") {
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
// alone vote: all of them obtaining an address is exists, all NXDOMAIN is
// not_found (where an authoritative server holding the name would have
// made it broken), and resolvers that disagree are broken.
func TestVerifyIndirectVote(t *testing.T) {
	servers := startFakeServers(t, "127.0.0.1", "127.0.0.2")
	for _, s := range servers {
		s.resolve("both.example.jp MX", data("mx1.example.jp"))
		mailHost(s, "mx1.example.jp", "192.0.2.1")
	}
	servers[0].resolve("half.example.jp MX", data("mx1.example.jp")) // 127.0.0.2: NXDOMAIN
	withAuthority(servers[0])
	servers[0].authority("gone.example.jp MX", data("mx1.example.jp")) // the resolvers: NXDOMAIN
	for _, tc := range []struct {
		name             string
		direct, indirect mailengine.DNSOutcome
	}{
		{"both.example.jp", mailengine.DNSOutcomeBroken, mailengine.DNSOutcomeExists}, // the authoritative server knows no such name
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

// TestVerifyOtherRunStoredFirst simulates another job verifying the same
// name at the same time and storing its outcome while this Checker is still
// asking: no second row is stored, and the other run's outcome is returned,
// so every notice of the name gets the same outcome while it is fresh.
func TestVerifyOtherRunStoredFirst(t *testing.T) {
	db := openTestMasterDB(t)
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("example.jp MX", data("mx1.example.jp"))
	mailHost(s, "mx1.example.jp", "192.0.2.1")
	withAuthority(s)
	var once sync.Once
	s.onQuery(func(q string) {
		if q != "auth example.jp MX udp" {
			return
		}
		once.Do(func() {
			if err := models.InsertDNSCheck(db, &models.DNSCheck{Name: "example.jp", QType: VerifyKind, Outcome: "broken", Detail: "other run", Direct: true}); err != nil {
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
	mailHost(servers[0], "mx1.example.jp", "192.0.2.1")
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
	mailHost(good, "mx1.example.jp", "192.0.2.1")
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
	if q := bad.received(); len(q) != queryAttempts || q[0] != "rd example.com A udp" {
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

// TestVerifyReusesStoredOutcome checks the reuse rule of a live Checker:
// the earliest outcome checked within CacheFreshAge before now is used
// without a query, whatever the age of the notice, and an older one is not
// (the name is verified live).
func TestVerifyReusesStoredOutcome(t *testing.T) {
	db := openTestMasterDB(t)
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("old.example.jp MX", data("mx1.example.jp"))
	mailHost(s, "mx1.example.jp", "192.0.2.1")
	now := time.Now()
	notice := now.Add(-10 * 24 * time.Hour)
	for _, r := range []*models.DNSCheck{
		{Name: "example.jp", QType: VerifyKind, Outcome: "broken", Detail: "earliest", Direct: true, CheckedAt: now.Add(-CacheFreshAge + time.Hour)},
		{Name: "example.jp", QType: VerifyKind, Outcome: "exists", Detail: "later", Direct: true, CheckedAt: now.Add(-time.Hour)},
		{Name: "old.example.jp", QType: VerifyKind, Outcome: "not_found", Detail: "too old", Direct: true, CheckedAt: now.Add(-CacheFreshAge - time.Hour)},
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
	// A notice dated in the future and a notice of today find the outcome
	// checked now.
	for _, at := range []time.Time{time.Now().Add(72 * time.Hour), time.Now()} {
		if got, ok := c.Verify(context.Background(), mailengine.DNSFailure{Name: "old.example.jp", QType: "MX"}, "example.jp", at, true); !ok || got != mailengine.DNSOutcomeExists {
			t.Errorf("notice %v: Verify = %q, %v", at, got, ok)
		}
	}
	if st := c.Stats(); st.Reused != 3 || st.Verified != 1 {
		t.Errorf("later notices: stats %+v", st)
	}
}

// TestVerifyFallsBackToLatest checks the stand-in of a live verification
// that gives nothing: the latest stored outcome of the name, whatever its
// age and kind (reused), when no resolver answers or the name may not be
// looked up for the notice; without a stored outcome the notice stays not
// verified and nothing is stored.
func TestVerifyFallsBackToLatest(t *testing.T) {
	db := openTestMasterDB(t)
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("customer.jp MX", data("mx.customer.jp"))
	s.resolve("example.jp MX", answerWith(fakeDrop))
	now := time.Now()
	for _, r := range []*models.DNSCheck{
		{Name: "example.jp", Outcome: "broken", Direct: true, CheckedAt: now.Add(-40 * 24 * time.Hour)},
		{Name: "example.jp", Outcome: "not_found", CheckedAt: now.Add(-3 * 24 * time.Hour)},
		{Name: "other.example", Outcome: "exists", CheckedAt: now.Add(-5 * 24 * time.Hour)},
	} {
		r.QType, r.Detail = VerifyKind, "stored"
		if err := models.InsertDNSCheck(db, r); err != nil {
			t.Fatal(err)
		}
	}
	c := newTestChecker(db, s.port, "127.0.0.1")
	ctx := context.Background()
	// No resolver answers the name: the latest row stands in (of the
	// resolvers alone, though the notice shows mail sent from the sending
	// sources).
	if got, status := c.VerifyStatus(ctx, mailengine.DNSFailure{Name: "example.jp", QType: "MX"}, "example.jp", now, true); got != mailengine.DNSOutcomeNotFound || status != mailengine.DNSStatusReused {
		t.Errorf("no answer: VerifyStatus = %q, %v; want the latest not_found", got, status)
	}
	// A name the notice may not have looked up: the stored row still stands in.
	if got, status := c.VerifyStatus(ctx, mailengine.DNSFailure{Name: "other.example", QType: "MX"}, "customer.jp", now, false); got != mailengine.DNSOutcomeExists || status != mailengine.DNSStatusReused {
		t.Errorf("not applicable: VerifyStatus = %q, %v; want the stored exists", got, status)
	}
	if q := s.receivedFor("other.example"); len(q) != 0 {
		t.Errorf("a name outside the recipient domain was asked: %v", q)
	}
	// Nothing stored: not verified.
	s.resolve("new.example.jp MX", answerWith(fakeDrop))
	if got, status := c.VerifyStatus(ctx, mailengine.DNSFailure{Name: "new.example.jp", QType: "MX"}, "example.jp", now, false); got != "" || status != mailengine.DNSStatusInconclusive {
		t.Errorf("nothing stored: VerifyStatus = %q, %v; want inconclusive", got, status)
	}
	if rows := storedChecks(t, db); len(rows) != 3 {
		t.Errorf("rows %+v: nothing may be stored", rows)
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
// reaches a resolver, a name server or the OS resolver; the source host
// names, the reporting MTAs and the SPF records are not looked up.
func TestVerifyStoredOnly(t *testing.T) {
	db := openTestMasterDB(t)
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("example.jp MX", data("mx1.example.jp"))
	s.resolve("example.jp TXT", data("v=spf1 ip4:192.0.2.0/24"))
	withAuthority(s)
	old, recent := time.Now().Add(-72*time.Hour), time.Now().Add(-time.Hour)
	for name, at := range map[string]time.Time{"old.example.jp": old, "recent.example.jp": recent} {
		if err := models.InsertDNSCheck(db, &models.DNSCheck{Name: name, QType: VerifyKind, Outcome: "exists", Detail: "stored",
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

// TestVerifyRowOfItsWindowLater checks that a stored outcome serves every
// notice while it is fresh and is checked again after: a notice verified
// at T is grouped again at T + CacheFreshAge without a query, and a second
// later the name is verified live again and a new row stored, whatever the
// age of the notice.
func TestVerifyRowOfItsWindowLater(t *testing.T) {
	db := openTestMasterDB(t)
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("example.jp MX", data("mx1.example.jp"))
	mailHost(s, "mx1.example.jp", "192.0.2.1")
	start := time.Now().Add(-48 * time.Hour).UTC().Truncate(time.Second)
	notice := start.Add(-5 * 24 * time.Hour)
	failure := mailengine.DNSFailure{Name: "example.jp", QType: "MX"}
	ctx := context.Background()

	first := newTestChecker(db, s.port, "127.0.0.1")
	first.now = func() time.Time { return start }
	if got, status := first.VerifyStatus(ctx, failure, "example.jp", notice, true); got != mailengine.DNSOutcomeExists || status != mailengine.DNSStatusVerified {
		t.Fatalf("first: VerifyStatus = %q, %v", got, status)
	}
	if rows := storedChecks(t, db); len(rows) != 1 {
		t.Fatalf("rows %+v", rows)
	}
	asked := len(s.received())

	again := newTestChecker(db, s.port, "127.0.0.1")
	again.now = func() time.Time { return start.Add(CacheFreshAge) }
	if got, status := again.VerifyStatus(ctx, failure, "example.jp", notice, true); got != mailengine.DNSOutcomeExists || status != mailengine.DNSStatusReused {
		t.Errorf("still fresh: VerifyStatus = %q, %v; want the stored exists", got, status)
	}
	if q := s.received(); len(q) != asked {
		t.Errorf("queries for a reused outcome: %v", q[asked:])
	}
	later := newTestChecker(db, s.port, "127.0.0.1")
	later.now = func() time.Time { return start.Add(CacheFreshAge + time.Second) }
	if got, status := later.VerifyStatus(ctx, failure, "example.jp", notice, true); got != mailengine.DNSOutcomeExists || status != mailengine.DNSStatusVerified {
		t.Errorf("no longer fresh: VerifyStatus = %q, %v; want a live verification", got, status)
	}
	if rows := storedChecks(t, db); len(rows) != 2 {
		t.Errorf("rows %+v, want a second one", rows)
	}
}

// TestVerifyStoreDatedAtTheWindow checks that a live verification is dated
// when it took its time (the end of the fresh period it looked in), not
// when the check ended.
func TestVerifyStoreDatedAtTheWindow(t *testing.T) {
	db := openTestMasterDB(t)
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("example.jp MX", data("mx1.example.jp"))
	mailHost(s, "mx1.example.jp", "192.0.2.1")
	start := time.Now().UTC().Truncate(time.Second)
	notice := start.Add(-48 * time.Hour)
	c := newTestChecker(db, s.port, "127.0.0.1")
	calls := 0
	c.now = func() time.Time {
		calls++
		if calls == 1 {
			return start // the time of the verification
		}
		return start.Add(5 * time.Second) // the check took five seconds
	}
	failure := mailengine.DNSFailure{Name: "example.jp", QType: "MX"}
	if got, status := c.VerifyStatus(context.Background(), failure, "example.jp", notice, true); got != mailengine.DNSOutcomeExists || status != mailengine.DNSStatusVerified {
		t.Fatalf("VerifyStatus = %q, %v", got, status)
	}
	row, err := models.LatestDNSCheck(db, "example.jp", VerifyKind)
	if err != nil || !row.CheckedAt.Equal(start) || row.Outcome != "exists" {
		t.Errorf("stored row %+v, %v; want one checked at %v", row, err, start)
	}
}

// TestVerifyAnyRecordType checks that the verification does not depend on
// the record type a notice names: a failure without a type (the Sendmail
// forms) is verified and stored as a verification of the name, and a
// failure of the same name with any other type reuses it.
func TestVerifyAnyRecordType(t *testing.T) {
	db := openTestMasterDB(t)
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("example.jp MX", data("mx1.example.jp"))
	mailHost(s, "mx1.example.jp", "192.0.2.1")
	c := newTestChecker(db, s.port, "127.0.0.1")
	if got, ok := verify(t, c, "example.jp", "", "example.jp"); !ok || got != mailengine.DNSOutcomeExists {
		t.Fatalf("Verify = %q, %v", got, ok)
	}
	if rows := storedChecks(t, db); len(rows) != 1 || rows[0].QType != VerifyKind {
		t.Errorf("rows %+v", rows)
	}
	before := len(s.received())
	for _, qtype := range []string{"MX", "A", "AAAA", "TXT"} {
		if got, ok := verify(t, c, "example.jp", qtype, "example.jp"); !ok || got != mailengine.DNSOutcomeExists {
			t.Errorf("%s: Verify = %q, %v", qtype, got, ok)
		}
	}
	if q := s.received(); len(q) != before {
		t.Errorf("other record types made queries: %v", q[before:])
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
			mailHost(s, "mx1.example.jp", "192.0.2.1")
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
// exchange fails is no answer: the only resolver gave none, so the name is
// inconclusive and nothing is stored.
func TestVerifyTruncatedWithoutTCP(t *testing.T) {
	db := openTestMasterDB(t)
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("example.jp MX", answerWith(fakeTruncate, "mx1.example.jp"))
	s.tcp.Close()
	c := newTestChecker(db, s.port, "127.0.0.1")
	if got, ok := c.Verify(context.Background(), mailengine.DNSFailure{Name: "example.jp", QType: "MX"}, "example.jp", time.Now(), false); ok || got != "" {
		t.Errorf("Verify = %q, %v; want inconclusive", got, ok)
	}
	if rows := storedChecks(t, db); len(rows) != 0 {
		t.Errorf("rows %+v", rows)
	}
}

// TestVerifyCanceled checks that a canceled context verifies nothing and
// does not mark the resolver unreachable.
func TestVerifyCanceled(t *testing.T) {
	db := openTestMasterDB(t)
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("example.jp MX", data("mx1.example.jp"))
	mailHost(s, "mx1.example.jp", "192.0.2.1")
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

// TestResolveHost checks the lookup of the source host names: A and AAAA,
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

// TestResolveHostHalfAnswered: a lookup of a source host name is answered
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
	s.resolve("mx1.example.jp A", data("192.0.2.1"))
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
		{"example.jp", mailengine.DNSOutcomeExists, "resolver system: control ok; MX ok (mx1.example.jp); mx1.example.jp: address ok (192.0.2.1)"},
		{"host.example.jp", mailengine.DNSOutcomeExists, "MX not found (NXDOMAIN or no data); address of the name itself ok (192.0.2.10)"},
		{"nosuch.example.jp", mailengine.DNSOutcomeNotFound, "address of the name itself not found (NXDOMAIN or no data)"},
	} {
		got, ok := verify(t, c, tc.name, "MX", "example.jp")
		if !ok || got != tc.want {
			t.Errorf("%s: Verify = %q, %v; want %q", tc.name, got, ok, tc.want)
			continue
		}
		row, err := models.FindDNSCheck(db, tc.name, VerifyKind, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), false)
		if err != nil || !strings.Contains(row.Detail, tc.note) || !strings.Contains(row.Detail, "authoritative: not asked (only the OS resolver is used)") {
			t.Errorf("%s: %v %+v", tc.name, err, row)
		}
	}
	// A notice naming another record type of the same name: the same
	// verification (an IPv4-only host with an AAAA failure exists).
	if got, ok := verify(t, c, "host.example.jp", "AAAA", "example.jp"); !ok || got != mailengine.DNSOutcomeExists {
		t.Errorf("AAAA of an IPv4-only host: Verify = %q, %v", got, ok)
	}
	// Only the OS resolver is contacted: no authoritative server directly,
	// even for a notice that clearly concerns mail from the sending sources.
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

// TestSystemResolverDestinations checks the destinations of a name through
// the OS resolver: a null MX accepts no mail (broken), an MX target with
// only an IPv6 address exists, and so does a name without MX whose only
// address is IPv6 (the implicit MX).
func TestSystemResolverDestinations(t *testing.T) {
	db := openTestMasterDB(t)
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve(controlName+" AAAA", data())
	s.resolve("example.jp MX", data("mx1.example.jp", "mx6.example.jp"))
	s.resolve("mx1.example.jp A", data("192.0.2.1"))
	s.resolve("mx6.example.jp AAAA", data("2001:db8::25"))
	s.resolve("nullmx.example.jp MX", data(""))
	s.resolve("nullmx.example.jp A", data("192.0.2.30"))
	s.resolve("implicit6.example.jp AAAA", data("2001:db8::26"))
	c := New(db, Config{Enabled: true, SystemOnly: true, Port: s.port, Timeout: time.Second})
	c.allowLocal = true
	c.sys = goResolverTo(s)
	skipWithoutGoResolver(t, c, s)

	for _, tc := range []struct {
		name string
		want mailengine.DNSOutcome
		note string
	}{
		{"nullmx.example.jp", mailengine.DNSOutcomeBroken, "(null MX: the domain accepts no mail)"},
		{"mx6.example.jp", mailengine.DNSOutcomeExists, "address ok (2001:db8::25)"},
		{"implicit6.example.jp", mailengine.DNSOutcomeExists, "address of the name itself ok (2001:db8::26)"},
	} {
		got, ok := verify(t, c, tc.name, "MX", "example.jp")
		if !ok || got != tc.want {
			t.Errorf("%s: Verify = %q, %v; want %q", tc.name, got, ok, tc.want)
			continue
		}
		row, err := models.FindDNSCheck(db, tc.name, VerifyKind, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), false)
		if err != nil || !strings.Contains(row.Detail, tc.note) {
			t.Errorf("%s: %v %+v; want the evidence %q", tc.name, err, row, tc.note)
		}
	}
}

// TestNormalize checks the validation of names.
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
		"fec0::1":          false, // fec0::/10
		"feff:ffff::1":     false,
		"::102:304":        false, // ::/96
		"::ffff:ffff":      false,
		"3fff::1":          false, // 3fff::/20
		"3fff:fff:ffff::1": false,
		"5f00::1":          false, // 5f00::/16
		"5f00:ffff::1":     false,
		// The new ranges are no wider than listed.
		"3fff:1000::1": true, // past 3fff::/20
		"5f01::1":      true, // past 5f00::/16
		// The last address of a range counts like the first.
		"2001:0:ffff::1": false, // 2001::/32
		// An IPv4-mapped IPv6 address is judged as the IPv4 address.
		"::ffff:192.0.2.1":  false, // 192.0.2.0/24
		"::ffff:100.64.0.1": false, // 100.64.0.0/10
		"::ffff:198.18.0.1": false, // 198.18.0.0/15
	} {
		if got := c.serverAllowed(netip.MustParseAddr(in)); got != want {
			t.Errorf("serverAllowed(%s) = %v, want %v", in, got, want)
		}
	}
	for _, in := range []string{"fe80::1%eth0", "2606:4700:4700::1111%eth0"} {
		if got := c.serverAllowed(netip.MustParseAddr(in)); got {
			t.Errorf("an address with a zone is allowed: %s", in)
		}
	}
	if got := c.usableServerAddrs([]netip.Addr{netip.MustParseAddr("2001:4860:4860::8888"), netip.MustParseAddr("10.0.0.1"),
		netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("2001:db8::1"), netip.MustParseAddr("8.8.8.8")}); len(got) != 3 ||
		got[0] != netip.MustParseAddr("1.1.1.1") || got[1] != netip.MustParseAddr("8.8.8.8") || got[2] != netip.MustParseAddr("2001:4860:4860::8888") {
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
	if _, err := models.FindDNSCheck(db, "example.jp", "MX", time.Now(), time.Now().Add(time.Hour), false); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("err %v", err)
	}
}
