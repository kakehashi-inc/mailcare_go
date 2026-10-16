package dnscheck

import (
	"context"
	"fmt"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"mailcare/app/models"
	"mailcare/app/modules/mailengine"
)

// TestVerifyStoredBeforeApplicability checks that a stored outcome is
// reused without any query, not even the MX lookup of the recipient domain
// that the applicability of a name outside its organization needs, and
// that a name remembered as inconclusive in the run is not asked again
// either.
func TestVerifyStoredBeforeApplicability(t *testing.T) {
	db := openTestMasterDB(t)
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("customer.jp MX", data("mx.provider.net"))
	s.resolve("mx.provider.net A", answerWith(fakeServFail))
	if err := models.InsertDNSCheck(db, &models.DNSCheck{Name: "mx.provider.net", QType: "AAAA", Outcome: "exists", Detail: "stored",
		Direct: true, CheckedAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	c := newTestChecker(db, s.port, "127.0.0.1")
	if got, ok := verify(t, c, "mx.provider.net", "AAAA", "customer.jp"); !ok || got != mailengine.DNSOutcomeExists {
		t.Fatalf("Verify = %q, %v", got, ok)
	}
	if q := s.received(); len(q) != 0 {
		t.Errorf("a stored outcome made queries: %v", q)
	}

	// Inconclusive (the resolver fails): remembered, so another notice
	// about the name for another recipient domain asks nothing.
	if got, ok := verify(t, c, "mx.provider.net", "A", "customer.jp"); ok || got != "" {
		t.Fatalf("inconclusive: Verify = %q, %v", got, ok)
	}
	before := len(s.received())
	if got, ok := verify(t, c, "mx.provider.net", "A", "other.example"); ok || got != "" {
		t.Fatalf("remembered: Verify = %q, %v", got, ok)
	}
	if q := s.received(); len(q) != before {
		t.Errorf("a remembered name made queries: %v", q[before:])
	}
	if st := c.Stats(); st != (Stats{Reused: 1, Inconclusive: 2}) {
		t.Errorf("stats %+v", st)
	}
}

// TestVerifyMXDomainCap checks the bound on the recipient domains whose MX
// targets one Checker looks up: the domains whose MX queries go unanswered
// are remembered as having no targets (the names stay inconclusive), and
// past the cap a domain is not looked up at all (over budget: a later run
// looks it up).
func TestVerifyMXDomainCap(t *testing.T) {
	db := openTestMasterDB(t)
	s := startFakeServers(t, "127.0.0.1")[0]
	const domains, limit = 6, 3
	for i := range domains {
		s.resolve(fmt.Sprintf("d%d.example MX", i), answerWith(fakeDrop))
	}
	c := newTestChecker(db, s.port, "127.0.0.1")
	if c.maxMXDomains != 200 {
		t.Errorf("maxMXDomains = %d, want 200", c.maxMXDomains)
	}
	c.maxMXDomains = limit
	for i := range domains {
		if got, ok := verify(t, c, "mx.provider.net", "A", fmt.Sprintf("d%d.example", i)); ok || got != "" {
			t.Errorf("d%d: Verify = %q, %v", i, got, ok)
		}
	}
	// The first domain again: its unanswered lookup is remembered.
	if got, ok := verify(t, c, "mx.provider.net", "A", "d0.example"); ok || got != "" {
		t.Errorf("d0 again: Verify = %q, %v", got, ok)
	}
	for i := range domains {
		want := 0
		if i < limit {
			want = 1
		}
		if q := s.receivedFor(fmt.Sprintf("d%d.example", i)); len(q) != want {
			t.Errorf("d%d: %d MX queries, want %d: %v", i, len(q), want, q)
		}
	}
	if q := s.receivedFor("mx.provider.net"); len(q) != 0 {
		t.Errorf("a name that is not applicable was looked up: %v", q)
	}
	if st := c.Stats(); st != (Stats{Inconclusive: limit + 1, OverBudget: domains - limit}) {
		t.Errorf("stats %+v", st)
	}
}

// TestVerifyBudget checks that once the network time budget is used up no
// further name is verified live, no MX target and no reporting MTA is
// looked up, while a stored outcome is still reused.
func TestVerifyBudget(t *testing.T) {
	if got := New(nil, Config{}).budget; got != 10*time.Minute {
		t.Errorf("default budget %v, want 10m", got)
	}
	db := openTestMasterDB(t)
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("a.example.jp A", data("192.0.2.1"))
	s.resolve("b.example.jp A", data("192.0.2.2"))
	s.resolve("customer.jp MX", data("mx.provider.net"))
	s.resolve("relay.example.net A", data("192.0.2.3"))
	if err := models.InsertDNSCheck(db, &models.DNSCheck{Name: "c.example.jp", QType: "A", Outcome: "exists", Detail: "stored",
		CheckedAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	c := New(db, Config{Enabled: true, Resolvers: []string{"127.0.0.1"}, Port: s.port, Timeout: testTimeout, Budget: time.Nanosecond})
	c.allowLocal = true

	// The first live verification starts with budget left and uses it up.
	if got, ok := verify(t, c, "a.example.jp", "A", "example.jp"); !ok || got != mailengine.DNSOutcomeExists {
		t.Fatalf("first: Verify = %q, %v", got, ok)
	}
	before := len(s.received())
	if got, ok := verify(t, c, "b.example.jp", "A", "example.jp"); ok || got != "" {
		t.Errorf("over budget: Verify = %q, %v", got, ok)
	}
	if got, ok := verify(t, c, "mx.provider.net", "A", "customer.jp"); ok || got != "" {
		t.Errorf("MX targets over budget: Verify = %q, %v", got, ok)
	}
	if addrs, answered := c.ResolveReporter(context.Background(), "relay.example.net"); addrs != nil || answered {
		t.Errorf("ResolveReporter over budget = %v, %v; want not answered", addrs, answered)
	}
	if q := s.received(); len(q) != before {
		t.Errorf("queries over budget: %v", q[before:])
	}
	if got, ok := verify(t, c, "c.example.jp", "A", "example.jp"); !ok || got != mailengine.DNSOutcomeExists {
		t.Errorf("stored over budget: Verify = %q, %v", got, ok)
	}
	if st := c.Stats(); st != (Stats{Verified: 1, Reused: 1, OverBudget: 2}) {
		t.Errorf("stats %+v", st)
	}
}

// TestSourceBudgetApart checks that the sending sources (the SPF records and the
// source host names) have a separate time budget: a run whose verification
// budget the notices used up still reads the SPF records and looks the
// source host names up, and once the budget of the sending sources is used up an SPF
// record is incomplete and a source host name not answered, without a
// question, while the verification goes on.
func TestSourceBudgetApart(t *testing.T) {
	if got := New(nil, Config{}).sourceBudget; got != 2*time.Minute {
		t.Errorf("default budget of the sending sources %v, want 2m", got)
	}
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("a.example.jp A", data("192.0.2.1"))
	s.resolve("b.example.jp A", data("192.0.2.2"))
	s.resolve("example.jp TXT", data("v=spf1 a:mail.example.jp"))
	s.resolve("mail.example.jp A", data("192.0.2.25"))
	s.resolve("smtp.example.jp A", data("192.0.2.26"))

	// The verification budget is used up by the first verification.
	c := New(nil, Config{Enabled: true, Resolvers: []string{"127.0.0.1"}, Port: s.port, Timeout: testTimeout, Budget: time.Nanosecond})
	c.allowLocal = true
	if got, ok := verify(t, c, "a.example.jp", "A", "example.jp"); !ok || got != mailengine.DNSOutcomeExists {
		t.Fatalf("first: Verify = %q, %v", got, ok)
	}
	if got, ok := verify(t, c, "b.example.jp", "A", "example.jp"); ok {
		t.Fatalf("the verification budget is not used up: %q", got)
	}
	if got := c.LookupSPF(context.Background(), "example.jp"); !got.Complete || !reflect.DeepEqual(got.Prefixes, prefixes("192.0.2.25/32")) {
		t.Errorf("SPF with the verification budget used up: %+v", got)
	}
	if got, answered := c.ResolveHost(context.Background(), "smtp.example.jp"); len(got) != 1 || !answered {
		t.Errorf("source host with the verification budget used up: %v, %v", got, answered)
	}

	// The budget of the sending sources is used up by the first query.
	c = New(nil, Config{Enabled: true, Resolvers: []string{"127.0.0.1"}, Port: s.port, Timeout: testTimeout, SourceBudget: time.Nanosecond})
	c.allowLocal = true
	if got := c.LookupSPF(context.Background(), "example.jp"); got.Complete || got.Prefixes != nil {
		t.Errorf("SPF over the budget of the sending sources: %+v, want incomplete", got)
	}
	before := len(s.received())
	if got, answered := c.ResolveHost(context.Background(), "smtp.example.jp"); got != nil || answered {
		t.Errorf("source host over the budget of the sending sources: %v, %v; want not answered", got, answered)
	}
	if q := s.received(); len(q) != before {
		t.Errorf("queries over the budget of the sending sources: %v", q[before:])
	}
	if got, ok := verify(t, c, "a.example.jp", "A", "example.jp"); !ok || got != mailengine.DNSOutcomeExists {
		t.Errorf("verification with the budget of the sending sources used up: %q, %v", got, ok)
	}
}

// TestVerifyDeadline checks that the deadline of a verification bounds the
// whole live lookup: name servers that would take one timeout per resolver
// (12 timeouts) are cut off at 4 timeouts, and the outcome of the resolvers,
// which all answered, stands (an authoritative server cut off does not
// vote).
func TestVerifyDeadline(t *testing.T) {
	ips := make([]string, 12)
	for i := range ips {
		ips[i] = fmt.Sprintf("127.0.0.%d", i+1)
	}
	servers := startFakeServers(t, ips...)
	for _, s := range servers {
		s.resolve("example.jp MX", data("mx1.example.jp"))
		s.resolve("example.jp NS", answerWith(fakeDrop))
	}
	const timeout = 100 * time.Millisecond
	db := openTestMasterDB(t)
	c := New(db, Config{Enabled: true, Resolvers: ips, Port: servers[0].port, Timeout: timeout})
	c.allowLocal = true
	start := time.Now()
	got, ok := verify(t, c, "example.jp", "MX", "example.jp")
	elapsed := time.Since(start)
	if !ok || got != mailengine.DNSOutcomeExists {
		t.Fatalf("Verify = %q, %v", got, ok)
	}
	// Without the deadline the NS lookups, one resolver after the other,
	// take 12 timeouts; the deadline cuts them at 4.
	if elapsed < 4*timeout || elapsed >= 9*timeout {
		t.Errorf("the verification took %v, want the deadline of %v", elapsed, 4*timeout)
	}
	rows := storedChecks(t, db)
	if len(rows) != 1 || !strings.Contains(rows[0].Detail, "authoritative: cut short by the deadline of the verification") {
		t.Errorf("rows %+v", rows)
	}
}

// TestCheckCutShortIsInconclusive checks that a check whose context ends
// before every reachable resolver answered gives no outcome: the resolver
// cut off would vote as a failure and turn the answer of the other into
// broken.
func TestCheckCutShortIsInconclusive(t *testing.T) {
	servers := startFakeServers(t, "127.0.0.1", "127.0.0.2")
	servers[0].resolve("example.jp MX", data("mx1.example.jp"))
	servers[1].resolve("example.jp MX", answerWith(fakeDrop))
	c := New(nil, Config{Enabled: true, Resolvers: []string{"127.0.0.1", "127.0.0.2"}, Port: servers[0].port, Timeout: time.Second})
	c.allowLocal = true
	// The controls run first with time to spare, so that the cut falls on
	// the lookups.
	if reachable := c.reachableResolvers(context.Background()); len(reachable) != 2 {
		t.Fatalf("reachable %+v", reachable)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if outcome, ok, _, _ := c.check(ctx, "example.jp", "MX", true); ok || outcome != "" {
		t.Errorf("check cut short = %q, %v", outcome, ok)
	}
	// With time for every resolver the same answers vote broken.
	if outcome, ok, _, _ := c.check(context.Background(), "example.jp", "MX", true); !ok || outcome != mailengine.DNSOutcomeBroken {
		t.Errorf("check = %q, %v", outcome, ok)
	}
}

// deadlineOnly is a context whose deadline may have passed while its timer
// has not marked it done yet.
type deadlineOnly struct {
	context.Context
	deadline time.Time
}

func (d deadlineOnly) Deadline() (time.Time, bool) { return d.deadline, true }

// TestEnded checks that a context counts as ended once its deadline has
// passed, before its timer marks it done (the sockets of the queries carry
// the same deadline).
func TestEnded(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for name, tc := range map[string]struct {
		ctx  context.Context
		want bool
	}{
		"background":       {context.Background(), false},
		"canceled":         {canceled, true},
		"deadline passed":  {deadlineOnly{context.Background(), time.Now().Add(-time.Millisecond)}, true},
		"deadline to come": {deadlineOnly{context.Background(), time.Now().Add(time.Hour)}, false},
	} {
		if got := ended(tc.ctx); got != tc.want {
			t.Errorf("%s: ended = %v, want %v", name, got, tc.want)
		}
	}
}

// TestDefaultsOfTheSpecification checks the bounds of design document "mail
// classification" 4.4 against the values it states: 200 names verified per
// run, 3 seconds per query, 12 seconds per verification, a budget of 10
// minutes, 200 recipient domains and 200 reporting MTAs looked up per run,
// and a budget of 2 minutes for the sending sources (2.7).
func TestDefaultsOfTheSpecification(t *testing.T) {
	c := New(nil, Config{})
	for name, got := range map[string]any{
		"maxLookups":      c.maxLookups,
		"timeout":         c.timeout,
		"verifyDeadline":  c.verifyDeadline(),
		"budget":          c.budget,
		"sourceBudget":    c.sourceBudget,
		"maxMXDomains":    c.maxMXDomains,
		"maxReporters":    c.maxReporters,
		"spfMaxQueries":   c.spfMaxQueries,
		"spfMaxDepth":     c.spfMaxDepth,
		"spfMaxPrefixes":  c.spfMaxPrefixes,
		"spfMaxMXTargets": c.spfMaxMXTargets,
	} {
		want := map[string]any{
			"maxLookups": 200, "timeout": 3 * time.Second, "verifyDeadline": 12 * time.Second,
			"budget": 10 * time.Minute, "sourceBudget": 2 * time.Minute, "maxMXDomains": 200, "maxReporters": 200,
			"spfMaxQueries": 30, "spfMaxDepth": 10, "spfMaxPrefixes": 1000, "spfMaxMXTargets": 10,
		}[name]
		if got != want {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
}

// TestVerifyAsksFourNameServers checks that at most 4 authoritative servers
// of a domain are asked (the first by name), whatever its NS records list.
func TestVerifyAsksFourNameServers(t *testing.T) {
	db := openTestMasterDB(t)
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("example.jp MX", data("mx1.example.jp"))
	s.resolve("example.jp NS", data("ns6.example.jp", "ns5.example.jp", "ns4.example.jp", "ns3.example.jp", "ns2.example.jp", "ns1.example.jp"))
	for i := 1; i <= 6; i++ {
		s.resolve(fmt.Sprintf("ns%d.example.jp A", i), data(s.ip))
	}
	s.authority("example.jp MX", data("mx1.example.jp"))
	c := newTestChecker(db, s.port, "127.0.0.1")
	if got, ok := verify(t, c, "example.jp", "MX", "example.jp"); !ok || got != mailengine.DNSOutcomeExists {
		t.Fatalf("Verify = %q, %v", got, ok)
	}
	rows := storedChecks(t, db)
	if len(rows) != 1 {
		t.Fatalf("rows %+v", rows)
	}
	var asked []string
	for _, line := range strings.Split(rows[0].Detail, "\n") {
		if strings.HasPrefix(line, "authoritative ns") {
			asked = append(asked, strings.Fields(line)[1])
		}
	}
	if strings.Join(asked, ",") != "ns1.example.jp,ns2.example.jp,ns3.example.jp,ns4.example.jp" {
		t.Errorf("authoritative servers asked %v:\n%s", asked, rows[0].Detail)
	}
	if q := s.receivedFor("example.jp"); len(q) != 2+4 { // MX and NS of the resolver, MX of 4 servers
		t.Errorf("queries %v", q)
	}
}

// authAsked returns how many authoritative questions about name a fake
// server received.
func authAsked(s *fakeServer, name string) int {
	n := 0
	for _, q := range s.receivedFor(name) {
		if strings.HasPrefix(q, "auth ") {
			n++
		}
	}
	return n
}

// TestVerifyAuthoritativeBlocked checks when the authoritative stage goes
// off for the rest of the run: after the servers of 3 different
// organization domains left every query unanswered, not after one domain
// with 8 silent addresses (4 name servers with 2 addresses each, the most a
// verification asks), and a domain that answered in between resets nothing.
// The evidence of a skipped stage says so.
func TestVerifyAuthoritativeBlocked(t *testing.T) {
	// 127.0.0.1 is the resolver and the authoritative server of ok.example,
	// 127.0.0.2-9 the 8 addresses of the servers of hostile.example, and
	// 127.0.0.10-13 the only server of s1.example ... s4.example; all of
	// them except 127.0.0.1 drop every authoritative question.
	ips := make([]string, 13)
	for i := range ips {
		ips[i] = fmt.Sprintf("127.0.0.%d", i+1)
	}
	servers := startFakeServers(t, ips...)
	resolver := servers[0]
	resolver.resolve("hostile.example MX", data("mx.hostile.example"))
	resolver.resolve("hostile.example NS", data("ns1.hostile.example", "ns2.hostile.example", "ns3.hostile.example", "ns4.hostile.example"))
	for n := 1; n <= 4; n++ {
		a, b := servers[2*n-1], servers[2*n]
		resolver.resolve(fmt.Sprintf("ns%d.hostile.example A", n), data(a.ip, b.ip))
		a.authority("hostile.example MX", answerWith(fakeDrop))
		b.authority("hostile.example MX", answerWith(fakeDrop))
	}
	for n := 1; n <= 4; n++ {
		name, s := fmt.Sprintf("s%d.example", n), servers[8+n]
		resolver.resolve(name+" MX", data("mx."+name))
		resolver.resolve(name+" NS", data("ns."+name))
		resolver.resolve("ns."+name+" A", data(s.ip))
		s.authority(name+" MX", answerWith(fakeDrop))
	}
	resolver.resolve("ok.example NS", data("ns.ok.example"))
	resolver.resolve("ns.ok.example A", data(resolver.ip))
	for _, name := range []string{"ok.example", "www.ok.example", "mail.ok.example"} {
		resolver.resolve(name+" MX", data("mx.ok.example"))
		resolver.authority(name+" MX", data("mx.ok.example"))
	}
	hostile := servers[1:9]
	silent := map[string]*fakeServer{"s1.example": servers[9], "s2.example": servers[10], "s3.example": servers[11], "s4.example": servers[12]}
	const skipped = "authoritative: skipped (the authoritative servers of 3 domains did not answer in this run)"
	const timeout = 150 * time.Millisecond

	// step verifies a name and checks whether its authoritative servers
	// were asked and whether the evidence says the stage was skipped.
	type step struct {
		name  string
		asked bool
	}
	run := func(t *testing.T, steps []step) {
		t.Helper()
		db := openTestMasterDB(t)
		c := New(db, Config{Enabled: true, Resolvers: []string{"127.0.0.1"}, Port: resolver.port, Timeout: timeout})
		c.allowLocal = true
		for _, st := range steps {
			count := func() int {
				if st.name == "hostile.example" {
					n := 0
					for _, s := range hostile {
						n += authAsked(s, st.name)
					}
					return n
				}
				if s, ok := silent[st.name]; ok {
					return authAsked(s, st.name)
				}
				return authAsked(resolver, st.name)
			}
			before := count()
			if got, ok := verify(t, c, st.name, "MX", st.name); !ok || got != mailengine.DNSOutcomeExists {
				t.Fatalf("%s: Verify = %q, %v", st.name, got, ok)
			}
			asked := count() - before
			want := 0
			if st.asked {
				want = 1
				if st.name == "hostile.example" {
					want = 8 // every address of the 4 servers once
				}
			}
			if asked != want {
				t.Errorf("%s: %d authoritative queries, want %d", st.name, asked, want)
			}
			row, err := models.FindDNSCheck(db, st.name, "MX", time.Now().Add(-time.Hour), time.Now().Add(time.Hour), false)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(row.Detail, skipped); got != !st.asked {
				t.Errorf("%s: skipped line %v:\n%s", st.name, got, row.Detail)
			}
		}
	}

	t.Run("one domain with 8 silent addresses", func(t *testing.T) {
		run(t, []step{{"hostile.example", true}, {"ok.example", true}, {"s1.example", true}, {"www.ok.example", true}})
	})
	t.Run("three silent domains", func(t *testing.T) {
		run(t, []step{{"s1.example", true}, {"s2.example", true}, {"hostile.example", true}, {"s3.example", false}, {"ok.example", false}})
	})
	t.Run("an answer resets nothing", func(t *testing.T) {
		run(t, []step{{"s1.example", true}, {"ok.example", true}, {"s2.example", true}, {"www.ok.example", true},
			{"s3.example", true}, {"mail.ok.example", false}, {"s4.example", false}})
	})
}

// TestVerifyCanceledAuthoritativeNotSilent checks that an authoritative
// query cut off by a cancellation of the run does not mark the server as
// silent: it is asked again by the next verification and counts nothing
// toward the blocking.
func TestVerifyCanceledAuthoritativeNotSilent(t *testing.T) {
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("example.jp MX", data("mx1.example.jp"))
	withAuthority(s)
	s.authority("example.jp MX", answerWith(fakeDrop))
	c := newTestChecker(nil, s.port, "127.0.0.1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.onQuery(func(q string) {
		if q == "auth example.jp MX udp" {
			cancel()
		}
	})
	if got, ok := c.Verify(ctx, mailengine.DNSFailure{Name: "example.jp", QType: "MX"}, "example.jp", time.Now(), true); ok || got != "" {
		t.Errorf("canceled: Verify = %q, %v", got, ok)
	}
	if q := s.receivedFor("example.jp"); !containsQuery(q, "auth example.jp MX udp") {
		t.Fatalf("the cancellation did not fall on the authoritative query: %v", q)
	}
	server := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(s.port))
	c.mu.Lock()
	silent, domains := c.silent[server], len(c.authDomains)
	c.mu.Unlock()
	if silent || domains != 0 {
		t.Errorf("after the cancellation: silent %v, %d domains recorded", silent, domains)
	}
	s.onQuery(nil)
	before := len(s.receivedFor("example.jp"))
	if got, ok := verify(t, c, "example.jp", "MX", "example.jp"); !ok || got != mailengine.DNSOutcomeExists {
		t.Fatalf("Verify = %q, %v", got, ok)
	}
	if q := s.receivedFor("example.jp")[before:]; !containsQuery(q, "auth example.jp MX udp") {
		t.Errorf("the authoritative server was not asked again: %v", q)
	}
}

// TestReuseRange checks the reuse window: from 24 hours before the notice
// to 48 hours after it, a notice time that is zero or after now counting as
// now.
func TestReuseRange(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for name, tc := range map[string]struct{ notice, from, to time.Time }{
		"past":   {now.Add(-72 * time.Hour), now.Add(-96 * time.Hour), now.Add(-24 * time.Hour)},
		"now":    {now, now.Add(-24 * time.Hour), now.Add(48 * time.Hour)},
		"future": {now.Add(time.Hour), now.Add(-24 * time.Hour), now.Add(48 * time.Hour)},
		"zero":   {time.Time{}, now.Add(-24 * time.Hour), now.Add(48 * time.Hour)},
	} {
		if from, to := ReuseRange(tc.notice, now); !from.Equal(tc.from) || !to.Equal(tc.to) {
			t.Errorf("%s: ReuseRange = %v - %v, want %v - %v", name, from, to, tc.from, tc.to)
		}
	}
	if ReuseWindow != 24*time.Hour {
		t.Errorf("ReuseWindow = %v, want 24h", ReuseWindow)
	}
}

// TestVerifyReuseBoundary checks the edges of the window: an outcome
// checked 23h30m before the notice is reused without a query, one checked
// 24h01m before it is not (the notice, recent enough, is verified live).
func TestVerifyReuseBoundary(t *testing.T) {
	db := openTestMasterDB(t)
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("outside.example.jp MX", data("mx1.example.jp"))
	notice := time.Now().Add(-12 * time.Hour)
	for name, age := range map[string]time.Duration{"inside.example.jp": 23*time.Hour + 30*time.Minute, "outside.example.jp": 24*time.Hour + time.Minute} {
		if err := models.InsertDNSCheck(db, &models.DNSCheck{Name: name, QType: "MX", Outcome: "broken", Detail: "stored", Direct: true, CheckedAt: notice.Add(-age)}); err != nil {
			t.Fatal(err)
		}
	}
	c := newTestChecker(db, s.port, "127.0.0.1")
	if got, ok := c.Verify(context.Background(), mailengine.DNSFailure{Name: "inside.example.jp", QType: "MX"}, "example.jp", notice, true); !ok || got != mailengine.DNSOutcomeBroken {
		t.Errorf("23h30m before: Verify = %q, %v; want the stored broken", got, ok)
	}
	if q := s.receivedFor("inside.example.jp"); len(q) != 0 {
		t.Errorf("a reused outcome made queries: %v", q)
	}
	if got, ok := c.Verify(context.Background(), mailengine.DNSFailure{Name: "outside.example.jp", QType: "MX"}, "example.jp", notice, true); !ok || got != mailengine.DNSOutcomeExists {
		t.Errorf("24h01m before: Verify = %q, %v; want a new exists", got, ok)
	}
	if st := c.Stats(); st != (Stats{Verified: 1, Reused: 1}) {
		t.Errorf("stats %+v", st)
	}
}

// TestAuthDomainAnsweredStaysAnswered: an organization domain one of whose
// authoritative servers replied once stays answered for the rest of the run,
// so a later verification it leaves unanswered does not count it among the
// silent domains; the stage goes off only at the third silent domain.
func TestAuthDomainAnsweredStaysAnswered(t *testing.T) {
	c := New(nil, Config{})
	note := func(org string, answered bool) {
		var tally authTally
		tally.note(answered)
		c.noteAuthDomain(org, &tally)
	}
	note("answered.example.jp", true)
	note("answered.example.jp", false)
	note("silent1.example.jp", false)
	note("silent2.example.jp", false)
	if c.authBlocked() {
		t.Fatalf("blocked with two silent domains and one that answered earlier: %v", c.authDomains)
	}
	c.noteAuthDomain("unasked.example.jp", &authTally{})
	if c.authBlocked() {
		t.Fatalf("blocked by a domain whose servers were not asked: %v", c.authDomains)
	}
	note("silent3.example.jp", false)
	if !c.authBlocked() {
		t.Errorf("not blocked with three silent domains: %v", c.authDomains)
	}
}

// TestRememberedWindow: what the run found for a name counts for a notice
// whose window holds the moment it was found, both ends of the window
// included, and an outcome comes before an inconclusive verification of
// the window even when the inconclusive one is earlier.
func TestRememberedWindow(t *testing.T) {
	key := checkKey{name: "nowhere.example.jp", qtype: "MX"}
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(ReuseWindow + mailengine.DNSLiveMaxAge)
	for _, at := range []time.Time{from, to} {
		c := New(nil, Config{})
		c.remember(key, memo{outcome: mailengine.DNSOutcomeNotFound, ok: true, at: at})
		if m, ok := c.remembered(key, from, to, false); !ok || m.outcome != mailengine.DNSOutcomeNotFound {
			t.Errorf("memo at %s: %+v, %v; want found within [%s, %s]", at, m, ok, from, to)
		}
	}
	c := New(nil, Config{})
	c.remember(key, memo{at: from.Add(time.Hour), direct: true})
	c.remember(key, memo{outcome: mailengine.DNSOutcomeExists, ok: true, at: from.Add(2 * time.Hour)})
	if m, ok := c.remembered(key, from, to, false); !ok || !m.ok || m.outcome != mailengine.DNSOutcomeExists {
		t.Errorf("an earlier inconclusive memo and a later outcome: %+v, %v; want the outcome", m, ok)
	}
	if m, ok := c.remembered(key, from, to, true); !ok || m.ok {
		t.Errorf("direct only: %+v, %v; want the inconclusive direct memo", m, ok)
	}
}

// TestBudgetLeftBoundary: the budget of the names and the budget of the
// sending sources are used up once the time spent reaches them.
func TestBudgetLeftBoundary(t *testing.T) {
	c := New(nil, Config{Budget: time.Minute})
	c.spend(time.Minute - time.Nanosecond)
	if !c.budgetLeft() {
		t.Error("budget used up before it was reached")
	}
	c.spend(time.Nanosecond)
	if c.budgetLeft() {
		t.Error("budget left once it was reached")
	}
	c.spendSource(c.sourceBudget)
	if c.sourceBudgetLeft() {
		t.Error("budget of the sending sources left once it was reached")
	}
}
