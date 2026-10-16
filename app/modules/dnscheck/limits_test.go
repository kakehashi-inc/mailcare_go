package dnscheck

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"time"

	"mailcare/app/models"
	"mailcare/app/modules/mailengine"
)

// TestVerifyStoredBeforeApplicability checks that a stored outcome is
// reused without any query, not even the MX lookup of the recipient domain
// that the applicability of a name outside its organization needs; that a
// row of the earlier verification per record type is not used; and that a
// run without a reachable resolver leaves the names inconclusive without
// asking the resolver again.
func TestVerifyStoredBeforeApplicability(t *testing.T) {
	db := openTestMasterDB(t)
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("customer.jp MX", data("mx.provider.net"))
	now := time.Now()
	if err := models.InsertDNSCheck(db, &models.DNSCheck{Name: "mx.provider.net", QType: "AAAA", Outcome: "not_found", Detail: "per type",
		Direct: true, CheckedAt: now.Add(-2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := models.InsertDNSCheck(db, &models.DNSCheck{Name: "mx.provider.net", QType: VerifyKind, Outcome: "exists", Detail: "stored",
		Direct: true, CheckedAt: now.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	c := newTestChecker(db, s.port, "127.0.0.1")
	if got, ok := verify(t, c, "mx.provider.net", "AAAA", "customer.jp"); !ok || got != mailengine.DNSOutcomeExists {
		t.Fatalf("Verify = %q, %v", got, ok)
	}
	if q := s.received(); len(q) != 0 {
		t.Errorf("a stored outcome made queries: %v", q)
	}

	// No reachable resolver (the control fails): inconclusive, and the
	// resolver is not asked again in the run.
	s.resolve(controlName+" A", answerWith(fakeServFail))
	if got, ok := verify(t, c, "mx9.provider.net", "A", "customer.jp"); ok || got != "" {
		t.Fatalf("no resolver: Verify = %q, %v", got, ok)
	}
	before := len(s.received())
	if got, ok := verify(t, c, "mx9.provider.net", "A", "other.example"); ok || got != "" {
		t.Fatalf("no resolver again: Verify = %q, %v", got, ok)
	}
	if q := s.received(); len(q) != before {
		t.Errorf("a failed resolver was asked again: %v", q[before:])
	}
	if st := c.Stats(); st != (Stats{Reused: 1, Inconclusive: 2}) {
		t.Errorf("stats %+v", st)
	}
}

// TestVerifyNoRunLimits checks that one run verifies every notice: more
// names, more recipient domains (whose MX targets the applicability looks
// up) and more reporting MTAs than any count a run was once limited to,
// each verified live or looked up.
func TestVerifyNoRunLimits(t *testing.T) {
	s := startFakeServers(t, "127.0.0.1")[0]
	const n = 250
	for i := range n {
		domain := fmt.Sprintf("d%d.example", i)
		s.resolve(domain+" MX", data("mx."+domain))
		s.resolve("mx."+domain+" A", data(fmt.Sprintf("192.0.2.%d", i%250+1)))
		s.resolve(fmt.Sprintf("relay%d.example.net A", i), data("198.51.100.1"))
	}
	c := newTestChecker(nil, s.port, "127.0.0.1")
	ctx := context.Background()
	for i := range n {
		domain := fmt.Sprintf("d%d.example", i)
		for _, name := range []string{domain, "mx." + domain} {
			if got, ok := c.Verify(ctx, mailengine.DNSFailure{Name: name, QType: "MX"}, domain, time.Now(), false); !ok || got != mailengine.DNSOutcomeExists {
				t.Fatalf("%s: Verify = %q, %v", name, got, ok)
			}
		}
		if addrs, answered := c.ResolveReporter(ctx, fmt.Sprintf("relay%d.example.net", i)); !answered || len(addrs) != 1 {
			t.Fatalf("relay%d: ResolveReporter = %v, %v", i, addrs, answered)
		}
	}
	if st := c.Stats(); st != (Stats{Verified: 2 * n}) {
		t.Errorf("stats %+v", st)
	}
}

// TestVerifyNoWholeDeadline checks that a verification as a whole has no
// time limit and asks one question at a time: a domain with many MX targets
// whose servers answer slowly (each reply well within the per-query
// timeout) is verified completely, every target asked for A and AAAA, one
// question after the reply to the one before, although the whole takes far
// longer than several per-query timeouts.
func TestVerifyNoWholeDeadline(t *testing.T) {
	s := startFakeServers(t, "127.0.0.1")[0]
	const timeout = 300 * time.Millisecond
	const delay = 60 * time.Millisecond
	var targets []string
	for i := range 12 {
		targets = append(targets, fmt.Sprintf("mx%02d.example.jp", i))
	}
	s.resolve("example.jp MX", data(targets...))
	s.resolve(targets[0]+" A", data("192.0.2.1"))
	c := New(nil, Config{Enabled: true, Resolvers: []string{"127.0.0.1"}, Port: s.port, Timeout: timeout})
	c.allowLocal = true
	if !c.reachableResolvers(context.Background())[0].addr.IsValid() {
		t.Fatal("no reachable resolver")
	}
	s.slow(delay)
	start := time.Now()
	got, ok := c.Verify(context.Background(), mailengine.DNSFailure{Name: "example.jp", QType: "MX"}, "example.jp", time.Now(), false)
	elapsed := time.Since(start)
	if !ok || got != mailengine.DNSOutcomeExists {
		t.Fatalf("Verify = %q, %v", got, ok)
	}
	for _, h := range targets {
		if q := s.receivedFor(h); len(q) != 2 {
			t.Errorf("%s asked %v", h, q)
		}
	}
	if most := s.mostAtOnce(); most != 1 {
		t.Errorf("%d questions waited at the same time, want one at a time", most)
	}
	// 25 questions one after another, each answered after the delay: well
	// past four per-query timeouts (the deadline a verification once had).
	if elapsed < 4*timeout {
		t.Errorf("the verification took %v, want longer than %v (no shortcut)", elapsed, 4*timeout)
	}
}

// TestQueryRetransmit checks that a UDP query whose first datagram goes
// unanswered is sent once more and the answer to it counts: the name is
// verified, and the server was asked twice.
func TestQueryRetransmit(t *testing.T) {
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("example.jp MX", answerWith(fakeDropOnce, "mx1.example.jp"))
	s.resolve("mx1.example.jp A", data("192.0.2.1"))
	c := newTestChecker(nil, s.port, "127.0.0.1")
	if got, ok := c.Verify(context.Background(), mailengine.DNSFailure{Name: "example.jp", QType: "MX"}, "example.jp", time.Now(), false); !ok ||
		got != mailengine.DNSOutcomeExists {
		t.Fatalf("Verify = %q, %v", got, ok)
	}
	n := 0
	for _, q := range s.receivedFor("example.jp") {
		if q == "rd example.jp MX udp" {
			n++
		}
	}
	if n != 2 {
		t.Errorf("MX asked %d times over UDP, want 2 (one retransmission)", n)
	}
}

// TestVerifyNoAnswerIsInconclusive checks that a name no resolver answers at
// all (every query and its retransmission time out) is inconclusive, not
// broken: nothing is stored, and the next notice of the name (in the run or
// the next grouping) verifies it again; while resolvers that do reply with
// an error (SERVFAIL) make it broken.
func TestVerifyNoAnswerIsInconclusive(t *testing.T) {
	servers := startFakeServers(t, "127.0.0.1", "127.0.0.2")
	for _, s := range servers {
		s.resolve("silent.example.jp MX", answerWith(fakeDrop))
		s.resolve("failing.example.jp MX", answerWith(fakeServFail))
	}
	servers[0].resolve("mixed.example.jp MX", answerWith(fakeServFail))
	servers[1].resolve("mixed.example.jp MX", answerWith(fakeDrop))
	db := openTestMasterDB(t)
	c := newTestChecker(db, servers[0].port, "127.0.0.1", "127.0.0.2")
	verifyName := func(c *Checker, name string) (mailengine.DNSOutcome, mailengine.DNSVerifyStatus) {
		return c.VerifyStatus(context.Background(), mailengine.DNSFailure{Name: name, QType: "MX"}, "example.jp", time.Now(), false)
	}
	if got, st := verifyName(c, "silent.example.jp"); got != "" || st != mailengine.DNSStatusInconclusive {
		t.Errorf("silent: %q, %v; want inconclusive", got, st)
	}
	asked := len(servers[0].receivedFor("silent.example.jp"))
	if asked != 2 {
		t.Errorf("silent: asked %d times, want the query and one retransmission", asked)
	}
	if got, st := verifyName(c, "silent.example.jp"); got != "" || st != mailengine.DNSStatusInconclusive || len(servers[0].receivedFor("silent.example.jp")) != 2*asked {
		t.Errorf("silent again in the run: %q, %v, asked %d times; want inconclusive, asked again", got, st,
			len(servers[0].receivedFor("silent.example.jp")))
	}
	for name, want := range map[string]mailengine.DNSOutcome{"failing.example.jp": mailengine.DNSOutcomeBroken, "mixed.example.jp": mailengine.DNSOutcomeBroken} {
		if got, st := verifyName(c, name); got != want || st != mailengine.DNSStatusVerified {
			t.Errorf("%s: %q, %v; want %q", name, got, st, want)
		}
	}
	for _, r := range storedChecks(t, db) {
		if r.Name == "silent.example.jp" {
			t.Errorf("an inconclusive name was stored: %+v", r)
		}
	}
	// The next run asks again; the name now answers.
	for _, s := range servers {
		s.resolve("silent.example.jp MX", data("mx1.example.jp"))
		s.resolve("mx1.example.jp A", data("192.0.2.1"))
	}
	next := newTestChecker(db, servers[0].port, "127.0.0.1", "127.0.0.2")
	if got, st := verifyName(next, "silent.example.jp"); got != mailengine.DNSOutcomeExists || st != mailengine.DNSStatusVerified {
		t.Errorf("next run: %q, %v; want verified exists", got, st)
	}
}

// TestCheckCutShortIsInconclusive checks that a check whose context ends
// before every reachable resolver answered gives no outcome: what was cut
// off tells nothing about the name.
func TestCheckCutShortIsInconclusive(t *testing.T) {
	servers := startFakeServers(t, "127.0.0.1", "127.0.0.2")
	servers[0].resolve("example.jp MX", data("mx1.example.jp"))
	servers[0].resolve("mx1.example.jp A", data("192.0.2.1"))
	servers[1].resolve("example.jp MX", answerWith(fakeDrop))
	c := New(nil, Config{Enabled: true, Resolvers: []string{"127.0.0.1", "127.0.0.2"}, Port: servers[0].port, Timeout: 300 * time.Millisecond})
	c.allowLocal = true
	// The controls run first with time to spare, so that the cut (a
	// cancellation of the run) falls on the lookups.
	if reachable := c.reachableResolvers(context.Background()); len(reachable) != 2 {
		t.Fatalf("reachable %+v", reachable)
	}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(150*time.Millisecond, cancel)
	if outcome, ok, _, _ := c.check(ctx, "example.jp", false, false); ok || outcome != "" {
		t.Errorf("check cut short = %q, %v", outcome, ok)
	}
	// With time for every resolver the one that answered decides: the
	// one that did not reply is left out.
	if outcome, ok, _, _ := c.check(context.Background(), "example.jp", false, false); !ok || outcome != mailengine.DNSOutcomeExists {
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

// TestDefaultsOfTheSpecification checks the values design document "mail
// classification" 4.4 and 2.7 state: 5 seconds per query attempt and 2
// attempts (the default of the OS resolver), a silent authoritative address
// left alone for 5 minutes (RFC 2308 7.2), stored results fresh for 12
// hours, a window of 24 hours for the stored-only mode, and the limits of RFC
// 7208 4.6.4 for the SPF records (10 terms that query DNS, 2 void lookups,
// 10 MX targets per mx term) with a nesting depth of 10.
func TestDefaultsOfTheSpecification(t *testing.T) {
	c := New(nil, Config{})
	for name, got := range map[string]any{
		"timeout":         c.timeout,
		"queryAttempts":   queryAttempts,
		"systemCall":      systemCallTimeout(c.timeout),
		"failureTTL":      failureTTL,
		"reuseWindow":     ReuseWindow,
		"cacheFreshAge":   CacheFreshAge,
		"spfMaxQueries":   c.spfMaxQueries,
		"spfMaxVoid":      c.spfMaxVoid,
		"spfMaxDepth":     c.spfMaxDepth,
		"spfMaxMXTargets": c.spfMaxMXTargets,
	} {
		want := map[string]any{
			"timeout": 5 * time.Second, "queryAttempts": 2, "systemCall": 10 * time.Second, "failureTTL": 5 * time.Minute, "reuseWindow": 24 * time.Hour,
			"cacheFreshAge": 12 * time.Hour,
			"spfMaxQueries": 10, "spfMaxVoid": 2, "spfMaxDepth": 10, "spfMaxMXTargets": 10,
		}[name]
		if got != want {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
}

// TestVerifyAsksEveryNameServer checks that every authoritative server of a
// domain is asked, whatever number its NS records list.
func TestVerifyAsksEveryNameServer(t *testing.T) {
	db := openTestMasterDB(t)
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("example.jp MX", data("mx1.example.jp"))
	s.resolve("mx1.example.jp A", data("192.0.2.1"))
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
	if strings.Join(asked, ",") != "ns1.example.jp,ns2.example.jp,ns3.example.jp,ns4.example.jp,ns5.example.jp,ns6.example.jp" {
		t.Errorf("authoritative servers asked %v:\n%s", asked, rows[0].Detail)
	}
	if q := s.receivedFor("example.jp"); len(q) != 2+6 { // MX and NS of the resolver, MX of 6 servers
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

// TestVerifyAuthoritativeNeverOff checks that the authoritative stage is
// never switched off: after the servers of several organization domains
// (one of them with 8 silent addresses) left every query unanswered, the
// servers of the next domains are still asked, and every verification
// still gets its outcome.
func TestVerifyAuthoritativeNeverOff(t *testing.T) {
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
	resolver.resolve("mx.hostile.example A", data("192.0.2.10"))
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
		resolver.resolve("mx."+name+" A", data("192.0.2.11"))
		resolver.resolve(name+" NS", data("ns."+name))
		resolver.resolve("ns."+name+" A", data(s.ip))
		s.authority(name+" MX", answerWith(fakeDrop))
	}
	resolver.resolve("ok.example NS", data("ns.ok.example"))
	resolver.resolve("mx.ok.example A", data("192.0.2.12"))
	resolver.resolve("ns.ok.example A", data(resolver.ip))
	resolver.resolve("ok.example MX", data("mx.ok.example"))
	resolver.authority("ok.example MX", data("mx.ok.example"))
	hostile := servers[1:9]
	silent := map[string]*fakeServer{"s1.example": servers[9], "s2.example": servers[10], "s3.example": servers[11], "s4.example": servers[12]}

	db := openTestMasterDB(t)
	c := New(db, Config{Enabled: true, Resolvers: []string{"127.0.0.1"}, Port: resolver.port, Timeout: 150 * time.Millisecond})
	c.allowLocal = true
	for _, name := range []string{"hostile.example", "s1.example", "s2.example", "s3.example", "s4.example", "ok.example"} {
		count := func() int {
			if name == "hostile.example" {
				n := 0
				for _, s := range hostile {
					n += authAsked(s, name)
				}
				return n
			}
			if s, ok := silent[name]; ok {
				return authAsked(s, name)
			}
			return authAsked(resolver, name)
		}
		before := count()
		if got, ok := verify(t, c, name, "MX", name); !ok || got != mailengine.DNSOutcomeExists {
			t.Fatalf("%s: Verify = %q, %v", name, got, ok)
		}
		want := queryAttempts // a silent address is asked once more
		switch name {
		case "hostile.example":
			want = 8 * queryAttempts // every address of the 4 servers
		case "ok.example":
			want = 1
		}
		if asked := count() - before; asked != want {
			t.Errorf("%s: %d authoritative queries, want %d", name, asked, want)
		}
	}
}

// TestVerifyCanceledAuthoritativeNotSilent checks that an authoritative
// query cut off by a cancellation of the run does not mark the server as
// silent: it is asked again by the next verification.
func TestVerifyCanceledAuthoritativeNotSilent(t *testing.T) {
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("example.jp MX", data("mx1.example.jp"))
	s.resolve("mx1.example.jp A", data("192.0.2.1"))
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
	_, silent := c.silent[server]
	c.mu.Unlock()
	if silent {
		t.Error("after the cancellation: the server is marked silent")
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

// TestReuseRange checks the window of the stored-only mode: from 24 hours
// before the notice up to now, whatever the age of the notice; a notice
// time that is zero or after now counts as now.
func TestReuseRange(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for name, tc := range map[string]struct{ notice, from, to time.Time }{
		"thirty days": {now.Add(-30 * 24 * time.Hour), now.Add(-31 * 24 * time.Hour), now},
		"past":        {now.Add(-72 * time.Hour), now.Add(-96 * time.Hour), now},
		"now":         {now, now.Add(-24 * time.Hour), now},
		"future":      {now.Add(time.Hour), now.Add(-24 * time.Hour), now},
		"zero":        {time.Time{}, now.Add(-24 * time.Hour), now},
	} {
		if from, to := ReuseRange(tc.notice, now); !from.Equal(tc.from) || !to.Equal(tc.to) {
			t.Errorf("%s: ReuseRange = %v - %v, want %v - %v", name, from, to, tc.from, tc.to)
		}
	}
	if ReuseWindow != 24*time.Hour {
		t.Errorf("ReuseWindow = %v, want 24h", ReuseWindow)
	}
}

// TestVerifyReuseBoundary checks the edges of the fresh period, whatever
// the age of the notice: an outcome checked exactly CacheFreshAge before
// now is reused without a query, one checked a minute earlier is not (the
// name is verified live and the new row stored).
func TestVerifyReuseBoundary(t *testing.T) {
	db := openTestMasterDB(t)
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("outside.example.jp MX", data("mx1.example.jp"))
	s.resolve("mx1.example.jp A", data("192.0.2.1"))
	now := time.Now().UTC().Truncate(time.Second)
	notice := now.Add(-10 * 24 * time.Hour)
	for name, age := range map[string]time.Duration{"inside.example.jp": CacheFreshAge, "outside.example.jp": CacheFreshAge + time.Minute} {
		if err := models.InsertDNSCheck(db, &models.DNSCheck{Name: name, QType: VerifyKind, Outcome: "broken", Detail: "stored", Direct: true, CheckedAt: now.Add(-age)}); err != nil {
			t.Fatal(err)
		}
	}
	c := newTestChecker(db, s.port, "127.0.0.1")
	c.now = func() time.Time { return now }
	if got, ok := c.Verify(context.Background(), mailengine.DNSFailure{Name: "inside.example.jp", QType: "MX"}, "example.jp", notice, true); !ok || got != mailengine.DNSOutcomeBroken {
		t.Errorf("checked CacheFreshAge ago: Verify = %q, %v; want the stored broken", got, ok)
	}
	if q := s.receivedFor("inside.example.jp"); len(q) != 0 {
		t.Errorf("a reused outcome made queries: %v", q)
	}
	if got, ok := c.Verify(context.Background(), mailengine.DNSFailure{Name: "outside.example.jp", QType: "MX"}, "example.jp", notice, true); !ok || got != mailengine.DNSOutcomeExists {
		t.Errorf("a minute older: Verify = %q, %v; want a new exists", got, ok)
	}
	if st := c.Stats(); st != (Stats{Verified: 1, Reused: 1}) {
		t.Errorf("stats %+v", st)
	}
	if rows := storedChecks(t, db); len(rows) != 3 {
		t.Errorf("rows %+v, want the new one beside the stale one", rows)
	}
}

// TestVerifyOldNotices checks that a notice of any age is verified live
// when nothing is stored for it (stored at the time of the check), and that
// the notices of the same name, older or recent, then reuse that check
// without a query while it is fresh.
func TestVerifyOldNotices(t *testing.T) {
	db := openTestMasterDB(t)
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("example.jp MX", data("mx1.example.jp"))
	s.resolve("mx1.example.jp A", data("192.0.2.1"))
	c := newTestChecker(db, s.port, "127.0.0.1")
	now := time.Now()
	at := func(age time.Duration) mailengine.DNSOutcome {
		got, ok := c.Verify(context.Background(), mailengine.DNSFailure{Name: "example.jp", QType: "MX"}, "example.jp", now.Add(-age), false)
		if !ok {
			t.Fatalf("%v old: not verified", age)
		}
		return got
	}
	if got := at(30 * 24 * time.Hour); got != mailengine.DNSOutcomeExists {
		t.Fatalf("30 days old: %q", got)
	}
	rows := storedChecks(t, db)
	if len(rows) != 1 || rows[0].QType != VerifyKind {
		t.Fatalf("rows %+v", rows)
	}
	before := len(s.received())
	for _, age := range []time.Duration{20 * 24 * time.Hour, 40 * 24 * time.Hour, time.Hour} {
		if got := at(age); got != mailengine.DNSOutcomeExists {
			t.Errorf("%v old: %q", age, got)
		}
	}
	if q := s.received(); len(q) != before {
		t.Errorf("older notices of a checked name made queries: %v", q[before:])
	}
	if st := c.Stats(); st != (Stats{Verified: 1, Reused: 3}) {
		t.Errorf("stats %+v", st)
	}
}

// TestSilentServerForgotten: an authoritative address that left a query
// unanswered is silent for failureTTL, and at once no longer when it
// replies.
func TestSilentServerForgotten(t *testing.T) {
	c := New(nil, Config{})
	start := time.Now()
	now := start
	c.now = func() time.Time { return now }
	server := netip.MustParseAddrPort("192.0.2.53:53")
	c.noteAuthReply(server, false)
	if !c.isSilent(server) {
		t.Fatal("not silent after an unanswered query")
	}
	now = start.Add(failureTTL - time.Nanosecond)
	if !c.isSilent(server) {
		t.Error("forgotten before the TTL")
	}
	now = start.Add(failureTTL)
	if c.isSilent(server) {
		t.Error("still silent after the TTL")
	}
	c.noteAuthReply(server, false)
	c.noteAuthReply(server, true)
	if c.isSilent(server) {
		t.Error("still silent after a reply")
	}
}

// TestVerifyLongMXList checks that every MX target of a domain is looked up,
// however many there are (a few at a time): a domain whose 40 targets have
// no address but the last exists, and the evidence names every target.
func TestVerifyLongMXList(t *testing.T) {
	s := startFakeServers(t, "127.0.0.1")[0]
	var targets []string
	for i := range 40 {
		targets = append(targets, fmt.Sprintf("mx%02d.example.jp", i))
	}
	s.resolve("example.jp MX", data(targets...))
	s.resolve(targets[39]+" AAAA", data("2001:db8::39"))
	db := openTestMasterDB(t)
	c := newTestChecker(db, s.port, "127.0.0.1")
	if got, ok := c.Verify(context.Background(), mailengine.DNSFailure{Name: "example.jp", QType: "MX"}, "example.jp", time.Now(), false); !ok ||
		got != mailengine.DNSOutcomeExists {
		t.Fatalf("Verify = %q, %v", got, ok)
	}
	rows := storedChecks(t, db)
	for _, h := range targets {
		if len(s.receivedFor(h)) != 2 || len(rows) != 1 || !strings.Contains(rows[0].Detail, h+": A ") {
			t.Errorf("%s: queries %v", h, s.receivedFor(h))
		}
	}
}
