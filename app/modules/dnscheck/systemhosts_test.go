package dnscheck

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"reflect"
	"sync"
	"testing"
	"time"
)

// stubSystem is an OS resolver for the tests that answers LookupNetIP from
// a table, the addresses of the family asked (a name missing from it, or
// without an address of the family, is not found; one in fail fails for
// every family, one in fail6 for IPv6 and one in fail4 for IPv4), answers
// the control query, and records the names asked with their network.
type stubSystem struct {
	mu        sync.Mutex
	addrs     map[string][]netip.Addr
	fail      map[string]bool
	fail4     map[string]bool
	fail6     map[string]bool
	asked     []string
	deadlines []time.Duration
}

func (s *stubSystem) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asked = append(s.asked, network+" "+host)
	if d, ok := ctx.Deadline(); ok {
		s.deadlines = append(s.deadlines, time.Until(d))
	}
	if s.fail[host] || (network == "ip4" && s.fail4[host]) || (network == "ip6" && s.fail6[host]) {
		return nil, &net.DNSError{Err: "server misbehaving", Name: host, IsTemporary: true}
	}
	var out []netip.Addr
	for _, a := range s.addrs[host] {
		if network == "ip" || (network == "ip4") == a.Unmap().Is4() {
			out = append(out, a)
		}
	}
	if len(out) == 0 {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	return out, nil
}

func (s *stubSystem) LookupHost(_ context.Context, host string) ([]string, error) {
	if host == controlName+"." {
		return []string{controlAddress}, nil
	}
	return nil, errors.New("not used")
}

func (s *stubSystem) LookupMX(context.Context, string) ([]*net.MX, error) {
	return nil, errors.New("not used")
}

func (s *stubSystem) LookupNS(context.Context, string) ([]*net.NS, error) {
	return nil, errors.New("not used")
}

func (s *stubSystem) LookupTXT(context.Context, string) ([]string, error) {
	return nil, errors.New("not used")
}

// TestSystemHostResolver checks the resolver of the source host names while
// the DNS check is off: a name of two labels or more is asked of the OS
// resolver as an absolute name, a single-label one as written, each family
// separately with the per-query timeout, once per run (a failure too);
// IPv4-mapped addresses come back unmapped, an invalid name is never asked,
// and answered tells a failure from a name without addresses.
func TestSystemHostResolver(t *testing.T) {
	stub := &stubSystem{
		addrs: map[string][]netip.Addr{
			"mx1.example.jp.": {netip.MustParseAddr("::ffff:192.0.2.25"), netip.MustParseAddr("2001:db8::25")},
			"mailhost":        {netip.MustParseAddr("10.0.0.5")},
		},
		fail: map[string]bool{"broken.example.jp.": true},
	}
	r := NewSystemHostResolver()
	r.sys = stub
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if got, ok := r.ResolveHost(ctx, "MX1.Example.JP."); !ok || !reflect.DeepEqual(got, []netip.Addr{netip.MustParseAddr("192.0.2.25"), netip.MustParseAddr("2001:db8::25")}) {
			t.Errorf("mx1: %v, %v", got, ok)
		}
		if got, ok := r.ResolveHost(ctx, "mailhost"); !ok || !reflect.DeepEqual(got, []netip.Addr{netip.MustParseAddr("10.0.0.5")}) {
			t.Errorf("mailhost: %v, %v", got, ok)
		}
		if got, ok := r.ResolveHost(ctx, "nosuch.example.jp"); got != nil || !ok {
			t.Errorf("not found: %v, %v (want answered without addresses)", got, ok)
		}
		if got, ok := r.ResolveHost(ctx, "broken.example.jp"); got != nil || ok {
			t.Errorf("failure: %v, %v (want not answered)", got, ok)
		}
		if got, ok := r.ResolveHost(ctx, "bad_host.example.jp"); got != nil || ok {
			t.Errorf("invalid name: %v, %v", got, ok)
		}
	}
	want := []string{"ip4 mx1.example.jp.", "ip6 mx1.example.jp.", "ip4 mailhost", "ip6 mailhost",
		"ip4 nosuch.example.jp.", "ip6 nosuch.example.jp.", "ip4 broken.example.jp.", "ip6 broken.example.jp."}
	if !reflect.DeepEqual(stub.asked, want) {
		t.Errorf("asked %v, want %v once each", stub.asked, want)
	}
	for _, d := range stub.deadlines {
		if d <= 0 || d > DefaultTimeout {
			t.Errorf("a lookup with the deadline %v, want within %v", d, DefaultTimeout)
		}
	}
	// A canceled lookup is not remembered.
	ctx2, cancel := context.WithCancel(context.Background())
	cancel()
	r2 := NewSystemHostResolver()
	r2.sys = stub
	if _, ok := r2.ResolveHost(ctx2, "mailhost"); ok {
		t.Error("a canceled lookup counts as answered")
	}
	if got, ok := r2.ResolveHost(context.Background(), "mailhost"); !ok || len(got) != 1 {
		t.Errorf("after the cancellation: %v, %v", got, ok)
	}
}

// TestSourceHostsHalfAnsweredBySystem: through the OS resolver (the DNS check
// off, or on with the OS resolver only), a lookup of a source host name is
// answered only when both families are; when one of them fails, the
// addresses of the other come back unanswered. A name of the hosts file
// with addresses of one family only is answered (Go reports the other
// family as an address error, not as not found).
func TestSourceHostsHalfAnsweredBySystem(t *testing.T) {
	both := []netip.Addr{netip.MustParseAddr("192.0.2.25"), netip.MustParseAddr("2001:db8::25")}
	stub := &stubSystem{
		addrs: map[string][]netip.Addr{"v4fail.example.jp.": both, "v6fail.example.jp.": both, "ok.example.jp.": both},
		fail4: map[string]bool{"v4fail.example.jp.": true},
		fail6: map[string]bool{"v6fail.example.jp.": true},
	}
	sysOnly := New(nil, Config{Enabled: true, SystemOnly: true, Timeout: testTimeout})
	sysOnly.sys = stub
	resolver := NewSystemHostResolver()
	resolver.sys = stub
	for name, resolve := range map[string]func(context.Context, string) ([]netip.Addr, bool){
		"system host resolver":      resolver.ResolveHost,
		"checker, OS resolver only": sysOnly.ResolveHost,
	} {
		for _, c := range []struct {
			host     string
			want     []netip.Addr
			answered bool
		}{
			{"ok.example.jp", both, true},
			{"v6fail.example.jp", both[:1], false},
			{"v4fail.example.jp", both[1:], false},
		} {
			if got, answered := resolve(context.Background(), c.host); !reflect.DeepEqual(got, c.want) || answered != c.answered {
				t.Errorf("%s: %s = %v, %v; want %v, %v", name, c.host, got, answered, c.want, c.answered)
			}
		}
	}

	// The hosts file: Go filters the addresses of the other family out and
	// reports an address error.
	hosts := &hostsFileSystem{stubSystem: stub, v4: map[string][]netip.Addr{"mailhost": {netip.MustParseAddr("10.0.0.5")}}}
	r := NewSystemHostResolver()
	r.sys = hosts
	if got, answered := r.ResolveHost(context.Background(), "mailhost"); !answered || !reflect.DeepEqual(got, []netip.Addr{netip.MustParseAddr("10.0.0.5")}) {
		t.Errorf("IPv4-only name of the hosts file: %v, %v", got, answered)
	}
}

// blackholeSystem is an OS resolver that never answers: every question
// waits for the deadline of its context and times out. It records the names
// asked.
type blackholeSystem struct {
	stubSystem
}

func (b *blackholeSystem) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	b.mu.Lock()
	b.asked = append(b.asked, network+" "+host)
	b.mu.Unlock()
	<-ctx.Done()
	return nil, &net.DNSError{Err: "i/o timeout", Name: host, IsTimeout: true}
}

// TestSystemHostResolverBudget: the lookups of the source host names through
// the OS resolver count against the budget of the run, those made at the
// same time each; once it is used up a name not looked up yet is not
// answered without asking (and not remembered), while the names looked up
// before keep their result.
func TestSystemHostResolverBudget(t *testing.T) {
	const timeout = 40 * time.Millisecond
	sys := &blackholeSystem{}
	// Each name waits for two timeouts (A and AAAA): the budget lets two
	// names in turn be asked, the third is not.
	r := newSystemHostResolver(sys, timeout, 3*timeout)
	ctx := context.Background()
	for _, host := range []string{"a.example.jp", "b.example.jp", "c.example.jp"} {
		if got, answered := r.ResolveHost(ctx, host); got != nil || answered {
			t.Errorf("%s: %v, %v; want not answered", host, got, answered)
		}
	}
	want := []string{"ip4 a.example.jp.", "ip6 a.example.jp.", "ip4 b.example.jp.", "ip6 b.example.jp."}
	if !reflect.DeepEqual(sys.asked, want) {
		t.Errorf("asked %v, want %v", sys.asked, want)
	}
	// A name looked up before is answered from the run; one never looked
	// up is still refused.
	start := time.Now()
	r.ResolveHost(ctx, "a.example.jp")
	r.ResolveHost(ctx, "c.example.jp")
	if elapsed := time.Since(start); len(sys.asked) != len(want) || elapsed > timeout {
		t.Errorf("after the budget: %d questions, %v; want no more", len(sys.asked), elapsed)
	}

	// Four names at once: each counts in full, so the budget of two names'
	// time ends the run after them.
	sys = &blackholeSystem{}
	r = newSystemHostResolver(sys, timeout, 4*timeout)
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Go(func() { r.ResolveHost(ctx, fmt.Sprintf("p%d.example.jp", i)) })
	}
	wg.Wait()
	if got, answered := r.ResolveHost(ctx, "late.example.jp"); got != nil || answered || len(sys.asked) != 8 {
		t.Errorf("after four names at once: %v, %v, %d questions; want refused after 8", got, answered, len(sys.asked))
	}
	if NewSystemHostResolver().budget != DefaultSourceBudget {
		t.Error("the budget of a run is not the budget of the sending sources")
	}
}

// hostsFileSystem answers names of the hosts file with IPv4 addresses only,
// as Go does: an address error for the IPv6 question.
type hostsFileSystem struct {
	*stubSystem
	v4 map[string][]netip.Addr
}

func (h *hostsFileSystem) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	a, ok := h.v4[host]
	if !ok {
		return h.stubSystem.LookupNetIP(ctx, network, host)
	}
	if network == "ip6" {
		return nil, &net.AddrError{Err: "no suitable address found", Addr: host}
	}
	return a, nil
}
