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

// hostLookupFunc looks up the addresses of one of the source host names and
// tells whether a resolver answered both the A and the AAAA question (with
// addresses or without any).
type hostLookupFunc func(ctx context.Context, host string) ([]netip.Addr, bool)

// spfLookupFunc evaluates the SPF record of one of the monitored domains.
type spfLookupFunc func(ctx context.Context, domain string) dnscheck.SPFResult

// sourceCache is the lookups of the sending sources during one run
// (groupOptions): mailengine.SourceHostResolver for the source host names and
// mailengine.SourceSPFResolver for the domains of the monitored mail
// addresses, in front of the resolvers of the run and of the stored lookups
// (sending_source_lookups, one row per kind and name, shared by every mailbox
// and run). A row at most dnscheck.CacheFreshAge old (the one period of
// every stored DNS lookup) is used without asking, so mailboxes sharing an
// SMTP host or a domain, and the runs of that period, cost one lookup; a row dated after now (the clock was set back) is
// never fresh. Otherwise the source is looked up: a complete lookup (a host
// name answered for both families, an SPF record evaluated completely)
// replaces the row (one short write, outside the transaction of any
// message), except that a complete lookup that found nothing (no address,
// no host name) never replaces a non-empty row, whatever its age: that row
// is used as it is (keptRow), so that an odd or spoofed empty answer
// cannot shrink the sending sources (the statement that writes checks it
// again, so a run storing an empty answer cannot replace the row another
// run stored after keptRow looked); a lookup that is not complete never
// replaces it but falls back to it, whatever its age, which then counts as
// complete, so that a DNS failure neither drops the addresses of the
// sending servers nor stops the backscatter rule, however long it lasts.
// Without such a row the lookup stays incomplete: the backscatter rule
// does not apply in the run (an address of a sending source may be
// missing; a host name still matches as text). The rows no source refers
// to at that moment (read again then, so that a source another job has
// just added keeps its row) are deleted before the first lookup of the
// run; no row is deleted for its age. Every source is looked up at most once per run;
// the grouping asks only when a message needs an address compared
// (mailengine sourceLookups), and asks for the host names one after another.
type sourceCache struct {
	db         *sql.DB
	now        func() time.Time
	lookupHost hostLookupFunc // nil: the addresses of the host names are not looked up (not answered)
	lookupSPF  spfLookupFunc  // nil: no SPF record is read or used (the DNS check is off)

	hostKinds map[string][]string // host name -> its kinds: smtp_host (of some mailbox), global_host

	pruneOnce sync.Once
	mu        sync.Mutex
	hosts     map[string]sourceHost // the host names looked up in the run
	spf       map[string]sourceSPF  // the domains evaluated in the run
}

// sendingSourceKey identifies a source and its row.
type sendingSourceKey struct {
	kind, name string
}

// sourceHost is the lookup of a source host name as the run uses it.
type sourceHost struct {
	addrs    []netip.Addr
	answered bool
}

// sourceSPF is the evaluation of the SPF record of a domain as the run uses
// it.
type sourceSPF struct {
	prefixes []netip.Prefix
	hosts    []string
	complete bool
}

// newSourceCache returns the lookups of a run for the saved host names
// (sending_source_hosts, normalized) and the mailboxes: their SMTP hosts and
// the domains of their addresses. The lookups are set by the caller.
func newSourceCache(db *sql.DB, globalHosts []string, mailboxes []*models.Mailbox) *sourceCache {
	c := &sourceCache{
		db: db, now: time.Now, hostKinds: map[string][]string{},
		hosts: map[string]sourceHost{}, spf: map[string]sourceSPF{},
	}
	eachSendingSource(globalHosts, mailboxes, func(k sendingSourceKey) {
		if k.kind != models.SendingSourceKindSPF && !slices.Contains(c.hostKinds[k.name], k.kind) {
			c.hostKinds[k.name] = append(c.hostKinds[k.name], k.kind)
		}
	})
	return c
}

// eachSendingSource calls fn with every source the saved host names
// (sending_source_hosts, normalized) and the mailboxes refer to: each host
// name of the setting, the SMTP host of every mailbox and the domain of
// every mailbox address (enabled or not), repeats included.
func eachSendingSource(globalHosts []string, mailboxes []*models.Mailbox, fn func(sendingSourceKey)) {
	for _, h := range globalHosts {
		fn(sendingSourceKey{models.SendingSourceKindGlobalHost, h})
	}
	for _, mb := range mailboxes {
		if h, ok := normalizeSourceHost(strings.TrimSpace(mb.SmtpHost)); ok {
			fn(sendingSourceKey{models.SendingSourceKindSMTPHost, h})
		}
		if d, ok := mailboxDomain(mb.Address); ok {
			fn(sendingSourceKey{models.SendingSourceKindSPF, d})
		}
	}
}

// mailboxDomain returns the domain of a monitored mail address when it is a
// host name of two labels or more (normalizeSourceHost): only such a domain has
// an SPF record to read.
func mailboxDomain(address string) (string, bool) {
	i := strings.LastIndexByte(address, '@')
	if i < 0 {
		return "", false
	}
	d, ok := normalizeSourceHost(address[i+1:])
	if !ok || !strings.Contains(d, ".") {
		return "", false
	}
	return d, true
}

// sourceSPFDomains returns the domains whose SPF record a run evaluates: the
// distinct domains of the mailboxes in mailbox order.
func sourceSPFDomains(mailboxes []*models.Mailbox) []string {
	var out []string
	for _, mb := range mailboxes {
		d, ok := mailboxDomain(mb.Address)
		if !ok || slices.Contains(out, d) {
			continue
		}
		out = append(out, d)
	}
	return out
}

// ResolveHost returns the addresses of one of the source host names
// (mailengine.SourceHostResolver): the stored ones while fresh, else looked up
// and, when the lookup was answered (both families), stored for every kind
// the name is (the SMTP host of a mailbox, a host name of the setting). An
// answer without any address stores nothing while one of those kinds holds
// a non-empty row; that row's addresses are used (answered; keptRow).
// When the lookup is not answered the freshest stored ones stand in
// (answered then); without such a row the addresses the lookup found, if
// any, are returned unanswered, and nothing is stored.
func (c *sourceCache) ResolveHost(ctx context.Context, host string) ([]netip.Addr, bool) {
	c.mu.Lock()
	res, ok := c.hosts[host]
	c.mu.Unlock()
	if ok {
		return res.addrs, res.answered
	}
	c.prune()
	kinds := c.hostKinds[host]
	if row := c.storedRow(kinds, host, true); row != nil {
		res = sourceHost{addrs: storedAddrs(row), answered: true}
	} else {
		var addrs []netip.Addr
		var answered bool
		if c.lookupHost != nil {
			addrs, answered = c.lookupHost(ctx, host)
		}
		if ctx.Err() != nil {
			return addrs, false
		}
		res = sourceHost{addrs: addrs, answered: answered}
		var kept *models.SendingSourceLookup
		if answered && len(addrs) == 0 {
			kept = c.keptRow(kinds, host)
		}
		switch {
		case kept != nil:
			res = sourceHost{addrs: storedAddrs(kept), answered: true}
		case answered:
			values := make([]string, 0, len(addrs))
			for _, a := range addrs {
				values = append(values, a.Unmap().String())
			}
			refused := false
			for _, kind := range kinds {
				if !c.store(&models.SendingSourceLookup{Kind: kind, Name: host, Addresses: values}, false) {
					refused = true
				}
			}
			if refused && len(addrs) == 0 {
				// Another run stored a non-empty row since keptRow looked:
				// it is used as keptRow would have.
				if kept := c.keptRow(kinds, host); kept != nil {
					res = sourceHost{addrs: storedAddrs(kept), answered: true}
				}
			}
		default:
			if row := c.storedRow(kinds, host, false); row != nil {
				res = sourceHost{addrs: storedAddrs(row), answered: true}
			}
		}
	}
	c.mu.Lock()
	c.hosts[host] = res
	c.mu.Unlock()
	return res.addrs, res.answered
}

// ResolveSPF evaluates the SPF record of a domain of the monitored mail
// addresses (mailengine.SourceSPFResolver): the stored result while fresh,
// else evaluated, a complete evaluation stored (one that found nothing only
// when the stored result is empty, else the stored result is used as it
// is: keptRow); an incomplete one falls back to the stored result, whatever
// its age, which counts as complete, and stays incomplete without one. A stored
// result that takes in too much address space is never used (usableSPF).
// Without an SPF lookup (the DNS check is off) nothing is read or used:
// incomplete.
func (c *sourceCache) ResolveSPF(ctx context.Context, domain string) ([]netip.Prefix, []string, bool) {
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
	kinds := []string{models.SendingSourceKindSPF}
	if row := usableSPF(c.storedRow(kinds, domain, true)); row != nil {
		res = sourceSPF{prefixes: parseStoredPrefixes(row.Addresses), hosts: row.Hosts, complete: true}
	} else {
		r := c.lookupSPF(ctx, domain)
		if ctx.Err() != nil {
			return r.Prefixes, r.Hosts, false
		}
		res = sourceSPF{prefixes: r.Prefixes, hosts: r.Hosts, complete: r.Complete}
		var kept *models.SendingSourceLookup
		unusable := false // the row keptRow found takes in too much: replaced whatever it lists
		if r.Complete && len(r.Prefixes) == 0 && len(r.Hosts) == 0 {
			found := c.keptRow(kinds, domain)
			kept = usableSPF(found)
			unusable = found != nil && kept == nil
		}
		switch {
		case kept != nil:
			res = sourceSPF{prefixes: parseStoredPrefixes(kept.Addresses), hosts: kept.Hosts, complete: true}
		case r.Complete:
			values := make([]string, 0, len(r.Prefixes))
			for _, p := range r.Prefixes {
				values = append(values, prefixText(p))
			}
			stored := c.store(&models.SendingSourceLookup{Kind: models.SendingSourceKindSPF, Name: domain, Addresses: values, Hosts: r.Hosts}, unusable)
			if !stored && len(r.Prefixes) == 0 && len(r.Hosts) == 0 {
				// Another run stored a non-empty row since keptRow looked:
				// it is used as keptRow would have.
				if kept := usableSPF(c.keptRow(kinds, domain)); kept != nil {
					res = sourceSPF{prefixes: parseStoredPrefixes(kept.Addresses), hosts: kept.Hosts, complete: true}
				}
			}
		default:
			if row := usableSPF(c.storedRow(kinds, domain, false)); row != nil {
				res = sourceSPF{prefixes: parseStoredPrefixes(row.Addresses), hosts: row.Hosts, complete: true}
			}
		}
	}
	c.mu.Lock()
	c.spf[domain] = res
	c.mu.Unlock()
	return res.prefixes, res.hosts, res.complete
}

// usableSPF returns a stored SPF result unless its prefixes take in more
// than an evaluation may count as sending sources (dnscheck.SPFTooWide: a row stored
// before the limits of the address space were applied); such a row is
// neither used without asking, nor kept, nor a fallback, so it cannot pass
// for a complete evaluation.
func usableSPF(row *models.SendingSourceLookup) *models.SendingSourceLookup {
	if row == nil || dnscheck.SPFTooWide(parseStoredPrefixes(row.Addresses)) {
		return nil
	}
	return row
}

// storedRow returns the most recently fetched stored row of a name among
// its kinds (nil when there is none or it cannot be read). fresh asks for a
// row to use without looking the source up: at most
// dnscheck.CacheFreshAge old; a row dated after now (the clock was set back since it was written)
// has no known age and never is one. Any row may stand in for a failed
// lookup (fresh false), a row dated after now until the daily cleanup
// deletes it.
func (c *sourceCache) storedRow(kinds []string, name string, fresh bool) *models.SendingSourceLookup {
	var best *models.SendingSourceLookup
	for _, kind := range kinds {
		row, err := models.GetSendingSourceLookup(c.db, kind, name)
		if err != nil {
			if err != sql.ErrNoRows {
				log.Printf("failed to read the stored lookup of %s: %v", name, err)
			}
			continue
		}
		if age := c.now().Sub(row.FetchedAt); fresh && (age > dnscheck.CacheFreshAge || age < 0) {
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
// that lists an address or a host name, whatever its age; nil when there
// is none. The row is left untouched (its fetched_at is not renewed).
// Every kind is judged together: an empty row written for one kind would
// otherwise be the freshest of the name in the next run and be used in
// place of the kept one.
func (c *sourceCache) keptRow(kinds []string, name string) *models.SendingSourceLookup {
	var best *models.SendingSourceLookup
	for _, kind := range kinds {
		row, err := models.GetSendingSourceLookup(c.db, kind, name)
		if err != nil {
			if err != sql.ErrNoRows {
				log.Printf("failed to read the stored lookup of %s: %v", name, err)
			}
			continue
		}
		if len(row.Addresses) == 0 && len(row.Hosts) == 0 {
			continue
		}
		if best == nil || row.FetchedAt.After(best.FetchedAt) {
			best = row
		}
	}
	return best
}

// store writes the row of a source fetched now, except that a row listing
// nothing does not replace a non-empty one (the rule of keptRow, applied
// again by the statement itself: another run may have stored that row
// since keptRow looked) unless force (the stored row is one the runs may
// not use, usableSPF). It reports whether the row was written. A failure is
// logged: the stored rows are only a fallback.
func (c *sourceCache) store(row *models.SendingSourceLookup, force bool) bool {
	row.FetchedAt = c.now()
	written, err := models.ReplaceSendingSourceLookup(c.db, row, !force)
	if err != nil {
		log.Printf("failed to store the lookup of %s: %v", row.Name, err)
	}
	return written
}

// prune deletes, once per run, the stored rows no current source refers to:
// no mailbox has that SMTP host or an address of that domain any more, or
// the host name left sending_source_hosts (never a row of a reporting MTA,
// kind reporter, which is no source). The mailboxes and the setting are
// read again here, after the rows, rather than taken from the start of the
// run: another job (several run at once) may have added a source since and
// stored its row, which must stay. A failure is logged and deletes nothing.
// Lookups of the run made at the same time wait for it.
func (c *sourceCache) prune() {
	c.pruneOnce.Do(c.pruneGone)
}

// pruneGone is prune without the once-per-run guard.
func (c *sourceCache) pruneGone() {
	rows, err := models.ListSendingSourceLookups(c.db)
	if err != nil {
		log.Printf("failed to list the stored lookups of the sending sources: %v", err)
		return
	}
	mailboxes, err := models.ListMailboxes(c.db)
	if err != nil {
		log.Printf("failed to list the mailboxes for the stored lookups of the sending sources: %v", err)
		return
	}
	sources := map[sendingSourceKey]bool{}
	eachSendingSource(ResolveSendingSourceHosts(c.db), mailboxes, func(k sendingSourceKey) { sources[k] = true })
	for _, row := range rows {
		// The reporting MTAs are no sources: the daily cleanup deletes
		// them by age (pruneDNSChecks).
		if row.Kind == models.SendingSourceKindReporter || sources[sendingSourceKey{row.Kind, row.Name}] {
			continue
		}
		if err := models.DeleteSendingSourceLookup(c.db, row.ID); err != nil {
			log.Printf("failed to delete the stored lookup of %s: %v", row.Name, err)
		}
	}
}

// storedAddrs returns the addresses of a stored host lookup.
func storedAddrs(row *models.SendingSourceLookup) []netip.Addr {
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
	_ mailengine.SourceHostResolver = (*sourceCache)(nil)
	_ mailengine.SourceSPFResolver  = (*sourceCache)(nil)
)
