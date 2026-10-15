package modules

import (
	"context"
	"database/sql"
	"log"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"mailcare/app/models"
	"mailcare/app/modules/dnscheck"
	"mailcare/app/modules/mailengine"
)

// hostLookupFunc looks up the addresses of one of our own host names and
// tells whether a resolver answered both the A and the AAAA question (with
// addresses or without any).
type hostLookupFunc func(ctx context.Context, host string) ([]netip.Addr, bool)

// spfLookupFunc evaluates the SPF record of one of the organization's
// domains.
type spfLookupFunc func(ctx context.Context, domain string) dnscheck.SPFResult

// ownSourceCache is the lookups of our own sending servers during one run
// (groupOptions): mailengine.OwnHostResolver for the own host names and
// mailengine.OwnSPFResolver for the domains of the monitored mail
// addresses, in front of the resolvers of the run and of the stored lookups
// (own_source_lookups, one row per kind and name, shared by every mailbox
// and run). A row at most OwnSourceCacheFreshAge old is used without asking,
// so mailboxes sharing an SMTP host or a domain, and the runs of the same
// hour, cost one lookup; a row dated after now (the clock was set back) is
// never fresh. Otherwise the source is looked up: a complete lookup (a host
// name answered for both families, an SPF record evaluated completely)
// replaces the row (one short write, outside the transaction of any
// message), except that a complete lookup that found nothing (no address,
// no host name) never replaces a non-empty row at most OwnSourceCacheMaxAge
// old: that row is used as it is (keptRow), so that one odd or spoofed
// empty answer cannot shrink our sources; a lookup that is not complete
// never replaces it but falls back
// to it when it is at most OwnSourceCacheMaxAge old, which then counts as
// complete, so that a DNS failure for a while neither drops the addresses
// of our servers nor stops the backscatter rule. Without such a row the
// lookup stays incomplete: the backscatter rule does not apply in the run
// (an address of ours may be missing; a host name still matches as text).
// The rows no source refers to at that moment (read again then, so that a
// source another job has just added keeps its row) are deleted before the
// first lookup of the run. Every source is looked up at most once per run;
// the grouping asks only when a message needs an address compared
// (mailengine ownLookups), and may ask for several host names at once.
type ownSourceCache struct {
	db         *sql.DB
	now        func() time.Time
	lookupHost hostLookupFunc // nil: the addresses of the host names are not looked up (not answered)
	lookupSPF  spfLookupFunc  // nil: no SPF record is read or used (the DNS check is off)

	hostKinds map[string][]string // host name -> its kinds: smtp_host (of some mailbox), global_host

	pruneOnce sync.Once
	mu        sync.Mutex
	hosts     map[string]ownHost // the host names looked up in the run
	spf       map[string]ownSPF  // the domains evaluated in the run
}

// ownSourceKey identifies a source and its row.
type ownSourceKey struct {
	kind, name string
}

// ownHost is the lookup of an own host name as the run uses it.
type ownHost struct {
	addrs    []netip.Addr
	answered bool
}

// ownSPF is the evaluation of the SPF record of a domain as the run uses
// it.
type ownSPF struct {
	prefixes []netip.Prefix
	hosts    []string
	complete bool
}

// newOwnSourceCache returns the lookups of a run for the saved host names
// (own_sending_hosts, normalized) and the mailboxes: their SMTP hosts and
// the domains of their addresses. The lookups are set by the caller.
func newOwnSourceCache(db *sql.DB, globalHosts []string, mailboxes []*models.Mailbox) *ownSourceCache {
	c := &ownSourceCache{
		db: db, now: time.Now, hostKinds: map[string][]string{},
		hosts: map[string]ownHost{}, spf: map[string]ownSPF{},
	}
	eachOwnSource(globalHosts, mailboxes, func(k ownSourceKey) {
		if k.kind != models.OwnSourceKindSPF && !slices.Contains(c.hostKinds[k.name], k.kind) {
			c.hostKinds[k.name] = append(c.hostKinds[k.name], k.kind)
		}
	})
	return c
}

// eachOwnSource calls fn with every source the saved host names
// (own_sending_hosts, normalized) and the mailboxes refer to: each host
// name of the setting, the SMTP host of every mailbox and the domain of
// every mailbox address (enabled or not), repeats included.
func eachOwnSource(globalHosts []string, mailboxes []*models.Mailbox, fn func(ownSourceKey)) {
	for _, h := range globalHosts {
		fn(ownSourceKey{models.OwnSourceKindGlobalHost, h})
	}
	for _, mb := range mailboxes {
		if h, ok := normalizeOwnHost(strings.TrimSpace(mb.SmtpHost)); ok {
			fn(ownSourceKey{models.OwnSourceKindSMTPHost, h})
		}
		if d, ok := mailboxDomain(mb.Address); ok {
			fn(ownSourceKey{models.OwnSourceKindSPF, d})
		}
	}
}

// mailboxDomain returns the domain of a monitored mail address when it is a
// host name of two labels or more (normalizeOwnHost): only such a domain has
// an SPF record to read.
func mailboxDomain(address string) (string, bool) {
	i := strings.LastIndexByte(address, '@')
	if i < 0 {
		return "", false
	}
	d, ok := normalizeOwnHost(address[i+1:])
	if !ok || !strings.Contains(d, ".") {
		return "", false
	}
	return d, true
}

// ownSPFDomains returns the domains whose SPF record a run evaluates: the
// distinct domains of the mailboxes in mailbox order, at most
// MaxSPFDomains.
func ownSPFDomains(mailboxes []*models.Mailbox) []string {
	var out []string
	for _, mb := range mailboxes {
		d, ok := mailboxDomain(mb.Address)
		if !ok || slices.Contains(out, d) {
			continue
		}
		if len(out) == MaxSPFDomains {
			break
		}
		out = append(out, d)
	}
	return out
}

// ResolveHost returns the addresses of one of our own host names
// (mailengine.OwnHostResolver): the stored ones while fresh, else looked up
// and, when the lookup was answered (both families), stored for every kind
// the name is (the SMTP host of a mailbox, a host name of the setting). An
// answer without any address stores nothing while one of those kinds holds
// a non-empty row at most OwnSourceCacheMaxAge old; that row's addresses
// are used (answered; keptRow).
// When the lookup is not answered the freshest stored ones at most
// OwnSourceCacheMaxAge old stand in (answered then); without such a row the
// addresses the lookup found, if any, are returned unanswered, and nothing
// is stored.
func (c *ownSourceCache) ResolveHost(ctx context.Context, host string) ([]netip.Addr, bool) {
	c.mu.Lock()
	res, ok := c.hosts[host]
	c.mu.Unlock()
	if ok {
		return res.addrs, res.answered
	}
	c.prune()
	kinds := c.hostKinds[host]
	if row := c.storedRow(kinds, host, OwnSourceCacheFreshAge, true); row != nil {
		res = ownHost{addrs: storedAddrs(row), answered: true}
	} else {
		var addrs []netip.Addr
		var answered bool
		if c.lookupHost != nil {
			addrs, answered = c.lookupHost(ctx, host)
		}
		if ctx.Err() != nil {
			return addrs, false
		}
		res = ownHost{addrs: addrs, answered: answered}
		var kept *models.OwnSourceLookup
		if answered && len(addrs) == 0 {
			kept = c.keptRow(kinds, host)
		}
		switch {
		case kept != nil:
			res = ownHost{addrs: storedAddrs(kept), answered: true}
		case answered:
			values := make([]string, 0, len(addrs))
			for _, a := range addrs {
				values = append(values, a.Unmap().String())
			}
			for _, kind := range kinds {
				c.store(&models.OwnSourceLookup{Kind: kind, Name: host, Addresses: values})
			}
		default:
			if row := c.storedRow(kinds, host, OwnSourceCacheMaxAge, false); row != nil {
				res = ownHost{addrs: storedAddrs(row), answered: true}
			}
		}
	}
	c.mu.Lock()
	c.hosts[host] = res
	c.mu.Unlock()
	return res.addrs, res.answered
}

// ResolveSPF evaluates the SPF record of a domain of the monitored mail
// addresses (mailengine.OwnSPFResolver): the stored result while fresh,
// else evaluated, a complete evaluation stored (one that found nothing only
// when the stored result is empty or older than OwnSourceCacheMaxAge, else
// the stored result is used as it is: keptRow); an incomplete one falls
// back to the stored result when it is at most OwnSourceCacheMaxAge old,
// which counts as complete, and stays incomplete without one. A stored
// result that takes in too much address space is never used (usableSPF).
// Without an SPF lookup (the DNS check is off) nothing is read or used:
// incomplete.
func (c *ownSourceCache) ResolveSPF(ctx context.Context, domain string) ([]netip.Prefix, []string, bool) {
	if c.lookupSPF == nil {
		return nil, nil, false
	}
	c.mu.Lock()
	res, ok := c.spf[domain]
	c.mu.Unlock()
	if ok {
		return res.prefixes, res.hosts, res.complete
	}
	c.prune()
	kinds := []string{models.OwnSourceKindSPF}
	if row := usableSPF(c.storedRow(kinds, domain, OwnSourceCacheFreshAge, true)); row != nil {
		res = ownSPF{prefixes: parseStoredPrefixes(row.Addresses), hosts: row.Hosts, complete: true}
	} else {
		r := c.lookupSPF(ctx, domain)
		if ctx.Err() != nil {
			return r.Prefixes, r.Hosts, false
		}
		res = ownSPF{prefixes: r.Prefixes, hosts: r.Hosts, complete: r.Complete}
		var kept *models.OwnSourceLookup
		if r.Complete && len(r.Prefixes) == 0 && len(r.Hosts) == 0 {
			kept = usableSPF(c.keptRow(kinds, domain))
		}
		switch {
		case kept != nil:
			res = ownSPF{prefixes: parseStoredPrefixes(kept.Addresses), hosts: kept.Hosts, complete: true}
		case r.Complete:
			values := make([]string, 0, len(r.Prefixes))
			for _, p := range r.Prefixes {
				values = append(values, prefixText(p))
			}
			c.store(&models.OwnSourceLookup{Kind: models.OwnSourceKindSPF, Name: domain, Addresses: values, Hosts: r.Hosts})
		default:
			if row := usableSPF(c.storedRow(kinds, domain, OwnSourceCacheMaxAge, false)); row != nil {
				res = ownSPF{prefixes: parseStoredPrefixes(row.Addresses), hosts: row.Hosts, complete: true}
			}
		}
	}
	c.mu.Lock()
	c.spf[domain] = res
	c.mu.Unlock()
	return res.prefixes, res.hosts, res.complete
}

// usableSPF returns a stored SPF result unless its prefixes take in more
// than an evaluation may count as ours (dnscheck.SPFTooWide: a row stored
// before the limits of the address space were applied); such a row is
// neither used without asking, nor kept, nor a fallback, so it cannot pass
// for a complete evaluation.
func usableSPF(row *models.OwnSourceLookup) *models.OwnSourceLookup {
	if row == nil || dnscheck.SPFTooWide(parseStoredPrefixes(row.Addresses)) {
		return nil
	}
	return row
}

// storedRow returns the most recently fetched stored row of a name among
// its kinds that is at most maxAge old (nil when there is none or it cannot
// be read). fresh asks for a row to use without looking the source up: a
// row dated after now (the clock was set back since it was written) has no
// known age and never is one; it may still stand in for a failed lookup
// (fresh false) until the daily cleanup deletes it (OwnSourceCacheMaxFuture).
func (c *ownSourceCache) storedRow(kinds []string, name string, maxAge time.Duration, fresh bool) *models.OwnSourceLookup {
	var best *models.OwnSourceLookup
	for _, kind := range kinds {
		row, err := models.GetOwnSourceLookup(c.db, kind, name)
		if err != nil {
			if err != sql.ErrNoRows {
				log.Printf("failed to read the stored lookup of %s: %v", name, err)
			}
			continue
		}
		age := c.now().Sub(row.FetchedAt)
		if age > maxAge || (fresh && age < 0) {
			continue
		}
		if best == nil || row.FetchedAt.After(best.FetchedAt) {
			best = row
		}
	}
	return best
}

// keptRow returns the stored row a complete lookup of a name that found
// nothing must not replace: the most recently fetched row among its kinds
// that lists an address or a host name and is at most OwnSourceCacheMaxAge
// old (one dated after now included); nil when there is none. The row is
// left untouched (its fetched_at is not renewed), so the empty answer
// replaces it once it is older than that. Every kind is judged together: an
// empty row written for one kind would otherwise be the freshest of the
// name in the next run and be used in place of the kept one.
func (c *ownSourceCache) keptRow(kinds []string, name string) *models.OwnSourceLookup {
	var best *models.OwnSourceLookup
	for _, kind := range kinds {
		row, err := models.GetOwnSourceLookup(c.db, kind, name)
		if err != nil {
			if err != sql.ErrNoRows {
				log.Printf("failed to read the stored lookup of %s: %v", name, err)
			}
			continue
		}
		if (len(row.Addresses) == 0 && len(row.Hosts) == 0) || c.now().Sub(row.FetchedAt) > OwnSourceCacheMaxAge {
			continue
		}
		if best == nil || row.FetchedAt.After(best.FetchedAt) {
			best = row
		}
	}
	return best
}

// store writes the row of a source fetched now. A failure is logged: the
// stored rows are only a fallback.
func (c *ownSourceCache) store(row *models.OwnSourceLookup) {
	row.FetchedAt = c.now()
	if err := models.ReplaceOwnSourceLookup(c.db, row); err != nil {
		log.Printf("failed to store the lookup of %s: %v", row.Name, err)
	}
}

// prune deletes, once per run, the stored rows no current source refers to:
// no mailbox has that SMTP host or an address of that domain any more, or
// the host name left own_sending_hosts. The mailboxes and the setting are
// read again here, after the rows, rather than taken from the start of the
// run: another job (several run at once) may have added a source since and
// stored its row, which must stay. A failure is logged and deletes nothing.
// Lookups of the run made at the same time wait for it.
func (c *ownSourceCache) prune() {
	c.pruneOnce.Do(c.pruneGone)
}

// pruneGone is prune without the once-per-run guard.
func (c *ownSourceCache) pruneGone() {
	rows, err := models.ListOwnSourceLookups(c.db)
	if err != nil {
		log.Printf("failed to list the stored lookups of the own sending servers: %v", err)
		return
	}
	mailboxes, err := models.ListMailboxes(c.db)
	if err != nil {
		log.Printf("failed to list the mailboxes for the stored lookups of the own sending servers: %v", err)
		return
	}
	sources := map[ownSourceKey]bool{}
	eachOwnSource(ResolveOwnSendingHosts(c.db), mailboxes, func(k ownSourceKey) { sources[k] = true })
	for _, row := range rows {
		if sources[ownSourceKey{row.Kind, row.Name}] {
			continue
		}
		if err := models.DeleteOwnSourceLookup(c.db, row.ID); err != nil {
			log.Printf("failed to delete the stored lookup of %s: %v", row.Name, err)
		}
	}
}

// storedAddrs returns the addresses of a stored host lookup.
func storedAddrs(row *models.OwnSourceLookup) []netip.Addr {
	var addrs []netip.Addr
	for _, p := range parseStoredPrefixes(row.Addresses) {
		addrs = append(addrs, p.Addr())
	}
	return addrs
}

// prefixText is the stored form of a prefix: the address alone for a
// single address, else the CIDR.
func prefixText(p netip.Prefix) string {
	if p.IsSingleIP() {
		return p.Addr().String()
	}
	return p.String()
}

// parseStoredPrefixes reads the stored addresses and CIDR prefixes (an
// address is a single-address prefix); an entry that does not parse is
// skipped.
func parseStoredPrefixes(values []string) []netip.Prefix {
	var out []netip.Prefix
	for _, v := range values {
		if strings.Contains(v, "/") {
			if p, err := netip.ParsePrefix(v); err == nil {
				out = append(out, p.Masked())
			}
			continue
		}
		if a, err := netip.ParseAddr(v); err == nil {
			a = a.Unmap()
			out = append(out, netip.PrefixFrom(a, a.BitLen()))
		}
	}
	return out
}

// The cache is what the grouping resolves through.
var (
	_ mailengine.OwnHostResolver = (*ownSourceCache)(nil)
	_ mailengine.OwnSPFResolver  = (*ownSourceCache)(nil)
)
