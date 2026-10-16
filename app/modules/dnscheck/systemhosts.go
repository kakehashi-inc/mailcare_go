package dnscheck

import (
	"context"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// SystemHostResolver resolves the host names of the sending sources
// (mailengine.SourceHostResolver: the setting sending_source_hosts and the SMTP
// hosts of the monitored mail addresses, written by the administrator and
// trusted) through the OS resolver alone. It serves the runs in which the
// verification of DNS lookup failures is switched off: the backscatter
// rule still compares the addresses of those names, but no public resolver
// is asked. A name of two labels or more is asked as an absolute name (no
// search domain is tried), a single-label name (an internal MTA such as
// "mailhost") as written, so that the search list and the hosts file of the
// host apply, as Checker.ResolveHost does. The A and the AAAA addresses are
// asked apart (lookupSourceSystem), each call bounded by the per-query
// timeout times queryAttempts (systemCallTimeout); a lookup
// is remembered for the run, an unanswered one too unless ctx ended. The
// grouping asks for several names at once (mailengine sourceLookups).
// Create one per run with NewSystemHostResolver; it is safe for concurrent
// use.
type SystemHostResolver struct {
	sys     systemResolver
	timeout time.Duration

	mu    sync.Mutex
	hosts map[string]hostLookup // results
}

// NewSystemHostResolver returns a SystemHostResolver for one run. It makes
// no query.
func NewSystemHostResolver() *SystemHostResolver {
	return newSystemHostResolver(defaultSystemResolver(), DefaultTimeout)
}

// newSystemHostResolver returns a SystemHostResolver asking sys with the
// per-query timeout (tests shorten it).
func newSystemHostResolver(sys systemResolver, timeout time.Duration) *SystemHostResolver {
	return &SystemHostResolver{sys: sys, timeout: timeout, hosts: map[string]hostLookup{}}
}

// ResolveHost returns the A and AAAA addresses of one of the source host names
// (unmapped) and whether the OS resolver answered both the IPv4 and the
// IPv6 question, with addresses or without any (not found): answered is
// false when the name is no valid host name, the lookup of either family
// failed (the addresses the other gave are still returned) or ctx ended.
func (r *SystemHostResolver) ResolveHost(ctx context.Context, host string) (addrs []netip.Addr, answered bool) {
	name, ok := normalizeHost(host, 1)
	if !ok {
		return nil, false
	}
	r.mu.Lock()
	res, ok := r.hosts[name]
	r.mu.Unlock()
	if ok {
		return res.addrs, res.answered
	}
	query := name
	if strings.Contains(name, ".") {
		query = absolute(name)
	}
	res.addrs, res.answered = lookupSourceSystem(ctx, r.sys, r.timeout, query)
	r.mu.Lock()
	defer r.mu.Unlock()
	if ended(ctx) {
		return res.addrs, false
	}
	r.hosts[name] = res
	return res.addrs, res.answered
}
