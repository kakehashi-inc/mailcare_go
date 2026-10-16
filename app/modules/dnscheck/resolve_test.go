package dnscheck

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"mailcare/app/modules/mailengine"
)

// The Checker resolves host names for mailengine to tell which reporting
// MTAs are sending servers.
var _ mailengine.HostResolver = (*Checker)(nil)

// recordingResolver passes every lookup to a Go resolver and records the
// names asked.
type recordingResolver struct {
	r     *net.Resolver
	mu    sync.Mutex
	names []string
}

func (rr *recordingResolver) record(name string) {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	rr.names = append(rr.names, name)
}

func (rr *recordingResolver) asked() []string {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	return append([]string(nil), rr.names...)
}

func (rr *recordingResolver) LookupHost(ctx context.Context, host string) ([]string, error) {
	rr.record(host)
	return rr.r.LookupHost(ctx, host)
}

func (rr *recordingResolver) LookupMX(ctx context.Context, name string) ([]*net.MX, error) {
	rr.record(name)
	return rr.r.LookupMX(ctx, name)
}

func (rr *recordingResolver) LookupNS(ctx context.Context, name string) ([]*net.NS, error) {
	rr.record(name)
	return rr.r.LookupNS(ctx, name)
}

func (rr *recordingResolver) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	rr.record(host)
	return rr.r.LookupNetIP(ctx, network, host)
}

func (rr *recordingResolver) LookupTXT(ctx context.Context, name string) ([]string, error) {
	rr.record(name)
	return rr.r.LookupTXT(ctx, name)
}

// skipWithoutGoResolver skips a test on the systems where the Go resolver
// cannot be pointed at a fake server, and where the host's resolver
// configuration keeps it from asking DNS at all (the fake server s then
// receives no question). A control query that reaches s and still fails is
// a failure of the test, not of the environment.
func skipWithoutGoResolver(t *testing.T, c *Checker, s *fakeServer) {
	t.Helper()
	if runtime.GOOS == "windows" || runtime.GOOS == "plan9" {
		t.Logf("skipped on %s: the Go resolver cannot be pointed at a fake server", runtime.GOOS)
		t.Skip("the Go resolver cannot be pointed at a fake server here")
	}
	before := len(s.received())
	if ok, note := c.systemControl(context.Background()); !ok {
		if len(s.received()) == before {
			t.Logf("skipped: the Go resolver sent no question to the fake server (%s); check nsswitch.conf / resolv.conf of the host", note)
			t.Skipf("the Go resolver does not reach the fake server here: %s", note)
		}
		t.Fatalf("the control query reached the fake server and failed: %s (%v)", note, s.received()[before:])
	}
}

// TestSystemResolverAbsoluteNames checks that every name the verification
// asks of the OS resolver is absolute, so that the resolver never tries it
// under a search domain of the host (a wildcard record there would make a
// missing name exist). The recorded names prove it on any host; on the
// wire, every question must be a name that was asked (the check only bites
// on a host whose resolv.conf has a search list).
func TestSystemResolverAbsoluteNames(t *testing.T) {
	db := openTestMasterDB(t)
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("example.jp MX", data("mx1.example.jp"))
	s.resolve("customer.jp MX", data("mx.provider.net"))
	s.resolve("mx.provider.net A", data("192.0.2.25"))
	s.resolve("relay.example.net A", data("192.0.2.26"))
	withAuthority(s)
	rec := &recordingResolver{r: goResolverTo(s)}
	c := New(db, Config{Enabled: true, SystemOnly: true, Port: s.port, Timeout: time.Second})
	c.allowLocal = true
	c.sys = rec
	skipWithoutGoResolver(t, c, s)

	for _, tc := range []struct {
		name, qtype, recipient string
		want                   mailengine.DNSOutcome
	}{
		{"example.jp", "MX", "example.jp", mailengine.DNSOutcomeExists},
		{"nosuch.example.jp", "MX", "example.jp", mailengine.DNSOutcomeNotFound},
		{"nosuch.example.jp", "AAAA", "example.jp", mailengine.DNSOutcomeNotFound},
		{"mx.provider.net", "A", "customer.jp", mailengine.DNSOutcomeExists},
	} {
		if got, ok := verify(t, c, tc.name, tc.qtype, tc.recipient); !ok || got != tc.want {
			t.Errorf("%s %s: Verify = %q, %v; want %q", tc.name, tc.qtype, got, ok, tc.want)
		}
	}
	if addrs, answered := c.ResolveReporter(context.Background(), "relay.example.net"); len(addrs) != 1 || !answered {
		t.Errorf("ResolveReporter = %v, %v", addrs, answered)
	}
	if addrs, answered := c.ResolveHost(context.Background(), "mx1.example.jp"); addrs != nil || !answered {
		t.Errorf("ResolveHost = %v, %v; want answered without addresses", addrs, answered)
	}

	asked := map[string]bool{}
	for _, n := range rec.asked() {
		if !strings.HasSuffix(n, ".") {
			t.Errorf("the OS resolver was asked the relative name %q", n)
		}
		asked[strings.ToLower(strings.TrimSuffix(n, "."))] = true
	}
	for _, want := range []string{controlName, "example.jp", "nosuch.example.jp", "customer.jp", "mx.provider.net", "relay.example.net"} {
		if !asked[want] {
			t.Errorf("%s was not asked of the OS resolver: %v", want, rec.asked())
		}
	}
	for _, q := range s.received() {
		if f := strings.Fields(q); f[0] == "rd" && !asked[f[1]] {
			t.Errorf("question %q on the wire: a name under a search domain", q)
		}
		if strings.HasPrefix(q, "auth ") {
			t.Errorf("an authoritative server was asked in the OS resolver mode: %q", q)
		}
	}
	if asked["ns1.example.jp"] {
		t.Error("the name servers were looked up in the OS resolver mode")
	}
}

// TestResolveHostSingleLabel checks that a single-label host name of the
// settings is asked of the OS resolver as written (its search list and
// hosts file apply), never of the configured resolvers, and is remembered.
func TestResolveHostSingleLabel(t *testing.T) {
	servers := startFakeServers(t, "127.0.0.1", "127.0.0.2")
	osDNS, configured := servers[0], servers[1]
	osDNS.resolve("mailhost A", data("192.0.2.25"))
	rec := &recordingResolver{r: goResolverTo(osDNS)}
	c := newTestChecker(nil, osDNS.port, "127.0.0.2")
	c.sys = rec
	skipWithoutGoResolver(t, c, osDNS)

	for i := 0; i < 2; i++ {
		if got, answered := c.ResolveHost(context.Background(), "MailHost"); len(got) != 1 || got[0] != netip.MustParseAddr("192.0.2.25") || !answered {
			t.Errorf("call %d: ResolveHost = %v, %v", i, got, answered)
		}
	}
	if q := configured.receivedFor("mailhost"); len(q) != 0 {
		t.Errorf("the configured resolver was asked: %v", q)
	}
	var lookups []string
	for _, n := range rec.asked() {
		if n != controlName+"." {
			lookups = append(lookups, n)
		}
	}
	// One question per family, once for the run.
	if len(lookups) != 2 || lookups[0] != "mailhost" || lookups[1] != "mailhost" {
		t.Errorf("names asked of the OS resolver: %v, want [mailhost mailhost] (A, then AAAA) once", lookups)
	}
	// A reporting MTA must have two labels or more: refused, never looked
	// up, so not answered (whose server it is stays unknown).
	if got, answered := c.ResolveReporter(context.Background(), "mailhost"); got != nil || answered {
		t.Errorf("ResolveReporter(mailhost) = %v, %v", got, answered)
	}
}

// TestResolveReporter checks the lookup of the reporting MTA names of the
// notices: remembered for the run (a lookup nobody answered too), refused
// without a query, and not answered, for what is no host name of two labels
// or more, and bounded to 200 names per Checker. A name over the cap is not
// answered; one that does not exist is answered without addresses.
func TestResolveReporter(t *testing.T) {
	s := startFakeServers(t, "127.0.0.1")[0]
	s.resolve("mx1.example.jp A", data("192.0.2.25"))
	s.resolve("mx1.example.jp AAAA", data("2001:db8::25"))
	s.resolve("silent.example.jp A", answerWith(fakeDrop))
	s.resolve("silent.example.jp AAAA", answerWith(fakeDrop))
	c := newTestChecker(nil, s.port, "127.0.0.1")
	if c.maxReporters != 200 {
		t.Fatalf("maxReporters = %d, want 200", c.maxReporters)
	}
	ctx := context.Background()
	want := []netip.Addr{netip.MustParseAddr("192.0.2.25"), netip.MustParseAddr("2001:db8::25")}
	for i := 0; i < 2; i++ {
		if got, answered := c.ResolveReporter(ctx, "MX1.Example.JP."); len(got) != 2 || got[0] != want[0] || got[1] != want[1] || !answered {
			t.Errorf("call %d: ResolveReporter = %v, %v", i, got, answered)
		}
	}
	if q := s.receivedFor("mx1.example.jp"); len(q) != 2 {
		t.Errorf("queries %v", q)
	}
	for _, host := range []string{"192.0.2.25", "[192.0.2.25]", "2001:db8::25", "mailhost", "", "bad host", "<script>"} {
		if got, answered := c.ResolveReporter(ctx, host); got != nil || answered {
			t.Errorf("ResolveReporter(%q) = %v, %v; want refused (no address, not answered)", host, got, answered)
		}
	}
	// An unknown name (NXDOMAIN): answered without addresses, remembered.
	for i := 0; i < 2; i++ {
		if got, answered := c.ResolveReporter(ctx, "unknown.example.jp"); got != nil || !answered {
			t.Errorf("unknown: ResolveReporter = %v, %v", got, answered)
		}
	}
	if q := s.receivedFor("unknown.example.jp"); len(q) != 2 {
		t.Errorf("unknown: queries %v", q)
	}
	// A name no resolver answers: not answered, remembered.
	for i := 0; i < 2; i++ {
		if got, answered := c.ResolveReporter(ctx, "silent.example.jp"); got != nil || answered {
			t.Errorf("silent: ResolveReporter = %v, %v; want not answered", got, answered)
		}
	}
	if q := s.receivedFor("silent.example.jp"); len(q) != 2 {
		t.Errorf("silent: queries %v", q)
	}
	// Up to the cap (three names used so far), then nothing more is asked.
	for i := 3; i < 200; i++ {
		c.ResolveReporter(ctx, fmt.Sprintf("r%d.example.jp", i))
	}
	before := len(s.received())
	if got, answered := c.ResolveReporter(ctx, "over.example.jp"); got != nil || answered {
		t.Errorf("over the cap: ResolveReporter = %v, %v; want not answered", got, answered)
	}
	if q := s.received(); len(q) != before {
		t.Errorf("a name over the cap was looked up: %v", q[before:])
	}
	if got, answered := c.ResolveReporter(ctx, "mx1.example.jp"); len(got) != 2 || !answered {
		t.Errorf("a remembered name over the cap: ResolveReporter = %v, %v", got, answered)
	}
	// A canceled lookup is not answered and not remembered.
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	c2 := newTestChecker(nil, s.port, "127.0.0.1")
	if got, answered := c2.ResolveReporter(canceled, "mx1.example.jp"); got != nil || answered {
		t.Errorf("canceled: ResolveReporter = %v, %v; want not answered", got, answered)
	}
	if got, answered := c2.ResolveReporter(ctx, "mx1.example.jp"); len(got) != 2 || !answered {
		t.Errorf("after the cancellation: ResolveReporter = %v, %v", got, answered)
	}
}

// TestResolveHostOSFirst checks the order of the lookups of the source host
// names with the check on: the OS resolver first (a name of a split-horizon
// DNS resolves only inside); its addresses are taken whenever it gives any,
// even half answered, and the configured resolvers are then not asked. Only
// when it gives none are they asked, and they decide when one of them
// answered or gave an address; otherwise the OS resolver's answer without
// addresses stands, and nobody answering leaves the name unanswered. In
// the SystemOnly mode the OS resolver is asked once.
func TestResolveHostOSFirst(t *testing.T) {
	inside := addrs("10.0.0.25", "fd00::25")
	public := addrs("192.0.2.25", "2001:db8::25")
	for _, c := range []struct {
		name       string
		os         *stubSystem
		resolver   bool // the configured resolver answers both questions (else it drops them)
		want       []netip.Addr
		answered   bool
		publicAsks bool
	}{
		{"OS answers with addresses", &stubSystem{addrs: map[string][]netip.Addr{"mail.example.jp.": inside}}, true, inside, true, false},
		{"OS half answered with an address", &stubSystem{addrs: map[string][]netip.Addr{"mail.example.jp.": inside},
			fail6: map[string]bool{"mail.example.jp.": true}}, true, inside[:1], false, false},
		{"OS without addresses, resolver answers", &stubSystem{}, true, public, true, true},
		{"OS fails, resolver answers", &stubSystem{fail: map[string]bool{"mail.example.jp.": true}}, true, public, true, true},
		{"OS without addresses, resolver silent", &stubSystem{}, false, nil, true, true},
		{"OS fails, resolver silent", &stubSystem{fail: map[string]bool{"mail.example.jp.": true}}, false, nil, false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := startFakeServers(t, "127.0.0.1")[0]
			if c.resolver {
				s.resolve("mail.example.jp A", data("192.0.2.25"))
				s.resolve("mail.example.jp AAAA", data("2001:db8::25"))
			} else {
				s.resolve("mail.example.jp A", answerWith(fakeDrop))
				s.resolve("mail.example.jp AAAA", answerWith(fakeDrop))
			}
			chk := newTestChecker(nil, s.port, "127.0.0.1")
			chk.sys = c.os
			got, answered := chk.ResolveHost(context.Background(), "mail.example.jp")
			if fmt.Sprint(got) != fmt.Sprint(c.want) || answered != c.answered {
				t.Errorf("ResolveHost = %v, %v; want %v, %v", got, answered, c.want, c.answered)
			}
			if asked := len(s.receivedFor("mail.example.jp")) > 0; asked != c.publicAsks {
				t.Errorf("the configured resolver asked: %v, want %v", s.receivedFor("mail.example.jp"), c.publicAsks)
			}
			if want := []string{"ip4 mail.example.jp.", "ip6 mail.example.jp."}; fmt.Sprint(c.os.asked) != fmt.Sprint(want) {
				t.Errorf("OS resolver asked %v, want %v", c.os.asked, want)
			}
		})
	}
	// The OS resolver only: asked once, with nothing to fall back on.
	stub := &stubSystem{}
	sysOnly := New(nil, Config{Enabled: true, SystemOnly: true, Timeout: testTimeout})
	sysOnly.sys = stub
	if got, answered := sysOnly.ResolveHost(context.Background(), "mail.example.jp"); got != nil || !answered {
		t.Errorf("OS resolver only: %v, %v; want answered without addresses", got, answered)
	}
	if want := []string{"ip4 mail.example.jp.", "ip6 mail.example.jp."}; fmt.Sprint(stub.asked) != fmt.Sprint(want) {
		t.Errorf("OS resolver only: asked %v, want %v once", stub.asked, want)
	}
}

// TestResolveHostConcurrent resolves several source host names at the same
// time, as the grouping does (up to four at once): every name gets its
// addresses, each is asked once, and the shared state of the Checker (the
// control queries, the names remembered, the budget of the sending sources)
// stays consistent (run with -race).
func TestResolveHostConcurrent(t *testing.T) {
	s := startFakeServers(t, "127.0.0.1")[0]
	const n = 8
	for i := range n {
		s.resolve(fmt.Sprintf("mx%d.example.jp A", i), data(fmt.Sprintf("192.0.2.%d", i+1)))
		s.resolve(fmt.Sprintf("mx%d.example.jp AAAA", i), data(fmt.Sprintf("2001:db8::%d", i+1)))
	}
	c := newTestChecker(nil, s.port, "127.0.0.1")
	got := make([][]netip.Addr, n)
	answered := make([]bool, n)
	var wg sync.WaitGroup
	for round := 0; round < 2; round++ {
		for i := range n {
			wg.Go(func() { got[i], answered[i] = c.ResolveHost(context.Background(), fmt.Sprintf("mx%d.example.jp", i)) })
		}
		wg.Wait()
	}
	for i := range n {
		want := addrs(fmt.Sprintf("192.0.2.%d", i+1), fmt.Sprintf("2001:db8::%d", i+1))
		if fmt.Sprint(got[i]) != fmt.Sprint(want) || !answered[i] {
			t.Errorf("mx%d: %v, %v; want %v, answered", i, got[i], answered[i], want)
		}
		if q := s.receivedFor(fmt.Sprintf("mx%d.example.jp", i)); len(q) != 2 {
			t.Errorf("mx%d asked %d times: %v", i, len(q), q)
		}
	}
	if c.sourceSpent <= 0 {
		t.Error("the lookups were not counted against the budget of the sending sources")
	}
}
