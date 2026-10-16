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
// asked apart (lookupSourceSystem), each bounded by DefaultTimeout; a lookup
// is remembered for the run, an unanswered one too unless ctx ended. Like
// the Checker, it bounds the network time of the run (DefaultSourceBudget, the
// budget of the sending sources): once used up, a name not looked up yet is
// not answered (and not remembered), so a long list of names an OS resolver
// leaves unanswered cannot hold the run. The grouping asks for several
// names at once (mailengine sourceLookups), and the time of every lookup
// counts, those made at the same time each. Create one per run with
// NewSystemHostResolver; it is safe for concurrent use.
type SystemHostResolver struct {
	sys     systemResolver
	timeout time.Duration
	budget  time.Duration

	mu    sync.Mutex
	spent time.Duration         // network time counted against budget
	hosts map[string]hostLookup // results
}

// NewSystemHostResolver returns a SystemHostResolver for one run. It makes
// no query.
func NewSystemHostResolver() *SystemHostResolver {
	return newSystemHostResolver(defaultSystemResolver(), DefaultTimeout, DefaultSourceBudget)
}

// newSystemHostResolver returns a SystemHostResolver asking sys with the
// per-query timeout and the budget of the run (tests shorten both).
func newSystemHostResolver(sys systemResolver, timeout, budget time.Duration) *SystemHostResolver {
	return &SystemHostResolver{sys: sys, timeout: timeout, budget: budget, hosts: map[string]hostLookup{}}
}

// ResolveHost returns the A and AAAA addresses of one of the source host names
// (unmapped) and whether the OS resolver answered both the IPv4 and the
// IPv6 question, with addresses or without any (not found): answered is
// false when the name is no valid host name, the budget of the run is used
// up, the lookup of either family failed (the addresses the other gave are
// still returned) or ctx ended.
func (r *SystemHostResolver) ResolveHost(ctx context.Context, host string) (addrs []netip.Addr, answered bool) {
	name, ok := normalizeHost(host, 1)
	if !ok {
		return nil, false
	}
	r.mu.Lock()
	res, ok := r.hosts[name]
	left := r.spent < r.budget
	r.mu.Unlock()
	if ok {
		return res.addrs, res.answered
	}
	if !left {
		return nil, false
	}
	query := name
	if strings.Contains(name, ".") {
		query = absolute(name)
	}
	start := time.Now()
	res.addrs, res.answered = lookupSourceSystem(ctx, r.sys, r.timeout, query)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.spent += time.Since(start)
	if ended(ctx) {
		return res.addrs, false
	}
	r.hosts[name] = res
	return res.addrs, res.answered
}
