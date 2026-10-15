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
		CheckedAt: time.Now().Add(-time.Hour)}); err != nil {
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

// TestOwnBudgetApart checks that the own sources (the SPF records and the
// own host names) have a time budget of their own: a run whose verification
// budget the notices used up still reads the SPF records and looks the own
// host names up, and once the budget of the own sources is used up an SPF
// record is incomplete and an own host name not answered, without a
// question, while the verification goes on.
func TestOwnBudgetApart(t *testing.T) {
	if got := New(nil, Config{}).ownBudget; got != 2*time.Minute {
		t.Errorf("default budget of the own sources %v, want 2m", got)
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
		t.Errorf("own host with the verification budget used up: %v, %v", got, answered)
	}

	// The budget of the own sources is used up by the first query.
	c = New(nil, Config{Enabled: true, Resolvers: []string{"127.0.0.1"}, Port: s.port, Timeout: testTimeout, OwnBudget: time.Nanosecond})
	c.allowLocal = true
	if got := c.LookupSPF(context.Background(), "example.jp"); got.Complete || got.Prefixes != nil {
		t.Errorf("SPF over the budget of the own sources: %+v, want incomplete", got)
	}
	before := len(s.received())
	if got, answered := c.ResolveHost(context.Background(), "smtp.example.jp"); got != nil || answered {
		t.Errorf("own host over the budget of the own sources: %v, %v; want not answered", got, answered)
	}
	if q := s.received(); len(q) != before {
		t.Errorf("queries over the budget of the own sources: %v", q[before:])
	}
	if got, ok := verify(t, c, "a.example.jp", "A", "example.jp"); !ok || got != mailengine.DNSOutcomeExists {
		t.Errorf("verification with the budget of the own sources used up: %q, %v", got, ok)
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
	if outcome, ok, _ := c.check(ctx, "example.jp", "MX", true); ok || outcome != "" {
		t.Errorf("check cut short = %q, %v", outcome, ok)
	}
	// With time for every resolver the same answers vote broken.
	if outcome, ok, _ := c.check(context.Background(), "example.jp", "MX", true); !ok || outcome != mailengine.DNSOutcomeBroken {
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
// and a budget of 2 minutes for the own sources (2.7).
func TestDefaultsOfTheSpecification(t *testing.T) {
	c := New(nil, Config{})
	for name, got := range map[string]any{
		"maxLookups":     c.maxLookups,
		"timeout":        c.timeout,
		"verifyDeadline": c.verifyDeadline(),
		"budget":         c.budget,
		"ownBudget":      c.ownBudget,
		"maxMXDomains":   c.maxMXDomains,
		"maxReporters":   c.maxReporters,
	} {
		want := map[string]any{
			"maxLookups": 200, "timeout": 3 * time.Second, "verifyDeadline": 12 * time.Second,
			"budget": 10 * time.Minute, "ownBudget": 2 * time.Minute, "maxMXDomains": 200, "maxReporters": 200,
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

// silentDomains sets up the fake servers for n names of their own
// registrable domains (n1.example ...), the resolver at servers[0] and the
// only authoritative server of name i at servers[i], which drops the
// question.
func silentDomains(servers []*fakeServer, n int) {
	for i := 1; i <= n; i++ {
		name := fmt.Sprintf("n%d.example", i)
		servers[0].resolve(name+" MX", data("mx."+name))
		servers[0].resolve(name+" NS", data("ns."+name))
		servers[0].resolve("ns."+name+" A", data(servers[i].ip))
		servers[i].authority(name+" MX", answerWith(fakeDrop))
	}
}

// TestVerifyAuthoritativeBlocked checks the network that blocks DNS to
// anything but the resolvers: after 8 authoritative queries left unanswered
// while none was answered in the run, no authoritative server is asked any
// more and the evidence says so; at 7 the next one is still asked. An
// answer earlier in the run keeps them asked.
func TestVerifyAuthoritativeBlocked(t *testing.T) {
	ips := make([]string, 11)
	for i := range ips {
		ips[i] = fmt.Sprintf("127.0.0.%d", i+1)
	}
	servers := startFakeServers(t, ips...)
	silentDomains(servers, 10)
	asked := func(i int) int {
		return len(servers[i].receivedFor(fmt.Sprintf("n%d.example", i)))
	}
	const skipped = "authoritative: skipped (no authoritative server answered in this run)"
	const timeout = 100 * time.Millisecond

	t.Run("blocked after 8", func(t *testing.T) {
		db := openTestMasterDB(t)
		c := New(db, Config{Enabled: true, Resolvers: []string{"127.0.0.1"}, Port: servers[0].port, Timeout: timeout})
		c.allowLocal = true
		for i := 1; i <= 10; i++ {
			before := asked(i)
			name := fmt.Sprintf("n%d.example", i)
			if got, ok := verify(t, c, name, "MX", name); !ok || got != mailengine.DNSOutcomeExists {
				t.Fatalf("%s: Verify = %q, %v", name, got, ok)
			}
			want := 1 // the 8th silent query is still made (7 before it)
			if i > 8 {
				want = 0
			}
			if got := asked(i) - before; got != want {
				t.Errorf("%s: %d authoritative queries, want %d", name, got, want)
			}
			row, err := models.FindDNSCheck(db, name, "MX", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(row.Detail, skipped); got != (i > 8) {
				t.Errorf("%s: skipped line %v:\n%s", name, got, row.Detail)
			}
		}
	})

	t.Run("an answer keeps them asked", func(t *testing.T) {
		db := openTestMasterDB(t)
		servers[0].resolve("n0.example MX", data("mx.n0.example"))
		servers[0].resolve("n0.example NS", data("ns.n0.example"))
		servers[0].resolve("ns.n0.example A", data(servers[0].ip))
		servers[0].authority("n0.example MX", data("mx.n0.example"))
		c := New(db, Config{Enabled: true, Resolvers: []string{"127.0.0.1"}, Port: servers[0].port, Timeout: timeout})
		c.allowLocal = true
		for i := 0; i <= 10; i++ {
			before := asked(i)
			name := fmt.Sprintf("n%d.example", i)
			if got, ok := verify(t, c, name, "MX", name); !ok || got != mailengine.DNSOutcomeExists {
				t.Fatalf("%s: Verify = %q, %v", name, got, ok)
			}
			if i > 0 && asked(i)-before != 1 {
				t.Errorf("%s: %d authoritative queries, want 1", name, asked(i)-before)
			}
		}
		for _, r := range storedChecks(t, db) {
			if strings.Contains(r.Detail, skipped) {
				t.Errorf("%s skipped the authoritative servers:\n%s", r.Name, r.Detail)
			}
		}
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
	silent, count := c.silent[server], c.authSilent
	c.mu.Unlock()
	if silent || count != 0 {
		t.Errorf("after the cancellation: silent %v, %d silent queries", silent, count)
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
		if err := models.InsertDNSCheck(db, &models.DNSCheck{Name: name, QType: "MX", Outcome: "broken", Detail: "stored", CheckedAt: notice.Add(-age)}); err != nil {
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
