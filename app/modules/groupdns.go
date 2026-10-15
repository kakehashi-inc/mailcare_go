package modules

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"mailcare/app/models"
	"mailcare/app/modules/dnscheck"
	"mailcare/app/modules/mailengine"
)

// groupOptions returns the options GroupMailbox and Reindex take from the
// settings for a run of the mailbox mb. Our own sending servers
// (WithOwnSources: ownSourcesFor) are always given: the backscatter rule
// (design document "mail classification" 2.6) depends on them alone,
// whether the DNS check is on or not. Every source of every mailbox
// matches, but only the saved lists and the SMTP host of mb itself make
// the sources configured for this run and give it host names, so that the
// SMTP host of another mailbox neither switches the backscatter rule on
// for mb nor ends its "nothing configured" default of the DNS judgement
// (4.4). The run looks up more
// addresses of ours through an ownSourceCache (stored per source and shared
// by every mailbox, reused for an hour, the fallback of a failed lookup):
// the addresses of the host names (WithOwnHostResolver) and, only while the
// DNS check is on, what the SPF records of the domains of the mailboxes
// list (WithOwnSPF, at most MaxSPFDomains domains). While the check is on the verifier makes
// these lookups through the configured resolvers like every lookup of the
// check (the own host names through the OS resolver first); while it is
// off only the OS resolver is asked for the host names
// (dnscheck.SystemHostResolver) and no SPF record is read, so switching the
// check off stops every query to the public resolvers. The DNS lookup
// failure verification (4.4, WithDNS) verifies live only while the check is
// on; while it is off WithDNS gets a StoredOnly Checker, which files a
// notice by the outcome stored for it (the reuse rule) and queries nothing
// (no verification, no reporting MTA, no SPF), so that switching the check
// off or on does not move the notices already verified between groups; the
// others stay unverified. A new verifier, resolver and cache
// are made for every call because the per-run limits and memories (the
// number of names, the budget, a resolver that did not answer is not asked
// again, the names looked up) belong to one run. Nothing is looked up here:
// the grouping looks up the own sources and the reporting MTAs only when a
// message needs them. An error (the mailboxes could not be listed) fails the
// run instead of grouping without the host names of the mailboxes.
func (m *JobManager) groupOptions(mb *models.Mailbox) ([]mailengine.GroupOption, error) {
	mailboxes, err := models.ListMailboxes(m.db)
	if err != nil {
		return nil, fmt.Errorf("failed to list the mailboxes: %w", err)
	}
	saved := ownSendingOf(m.db, mailboxes)
	own := ownSourcesFor(saved, mb, mailboxes)
	opts := []mailengine.GroupOption{withOwnSources(own)}
	cache := newOwnSourceCache(m.db, saved.Hosts, mailboxes)
	cfg := ResolveDNSCheck(m.db)
	if !cfg.Enabled {
		cache.lookupHost = newSystemHostLookup()
		stored := newDNSChecker(m.db, dnscheck.Config{StoredOnly: true})
		return append(opts, withOwnHostResolver(cache), withDNS(stored, own)), nil
	}
	checker := newDNSChecker(m.db, dnscheck.Config{
		Enabled:    true,
		Resolvers:  cfg.Resolvers,
		SystemOnly: cfg.SystemOnly,
	})
	cache.lookupHost, cache.lookupSPF = checkerLookups(checker)
	return append(opts, withOwnHostResolver(cache), withOwnSPF(cache, ownSPFDomains(mailboxes)), withDNS(checker, own)), nil
}

// ownSourcesFor returns our own sending servers as the run of the mailbox
// mb sees them: the saved lists, with the saved host names and the SMTP
// host of mb as its host names, and the SMTP hosts of the other mailboxes
// as host names that only match (mailengine.OwnSources.WithOtherHosts).
// The SMTP host of mb is taken from the mailboxes just listed (mb may be
// older), normalized like the others; an invalid one counts as none.
func ownSourcesFor(saved OwnSending, mb *models.Mailbox, mailboxes []*models.Mailbox) mailengine.OwnSources {
	hosts := slices.Clone(saved.Hosts)
	if mb != nil {
		raw := mb.SmtpHost
		for _, cur := range mailboxes {
			if cur.ID == mb.ID {
				raw = cur.SmtpHost
				break
			}
		}
		if h, ok := normalizeOwnHost(strings.TrimSpace(raw)); ok {
			hosts = append(hosts, h)
		}
	}
	return mailengine.NewOwnSources(saved.IPv4, saved.IPv6, hosts, nil).WithOtherHosts(saved.MailboxHosts)
}

// The constructors groupOptions builds its options with. The tests replace
// them to see what it passes on (the options themselves are opaque) and to
// stand in for the network.
var (
	newDNSChecker       = dnscheck.New
	newSystemHostLookup = func() hostLookupFunc { return dnscheck.NewSystemHostResolver().ResolveHost }
	checkerLookups      = func(c *dnscheck.Checker) (hostLookupFunc, spfLookupFunc) { return c.ResolveHost, c.LookupSPF }
	withDNS             = mailengine.WithDNS
	withOwnSources      = mailengine.WithOwnSources
	withOwnHostResolver = mailengine.WithOwnHostResolver
	withOwnSPF          = mailengine.WithOwnSPF
)

// pruneDNSChecks deletes the DNS verification results checked before the
// mail retention (mail_keep_days) and the reuse window before it
// (dnscheck.ReuseWindow): a notice still retained may use a result checked
// up to that window before it was received, and every notice older than the
// retention is removed by then. It also deletes the stored lookups of the
// own sending servers older than OwnSourceCacheKeepAge (long past the age
// at which they stand in for a failed lookup) and those dated more than
// OwnSourceCacheMaxFuture after now (the clock was set back). The tables are
// shared by every mailbox, so like the job history they are pruned once per
// cleanup, whichever child runs first. Every deletion is tried even when one
// before it failed; the failures are joined.
func (m *JobManager) pruneDNSChecks(progress func(string)) error {
	var errs []error
	keepDays := ResolveMailKeepDays(m.db)
	now := time.Now()
	cutoff := now.Add(-time.Duration(keepDays)*24*time.Hour - dnscheck.ReuseWindow)
	if removed, err := models.DeleteDNSChecksOlderThan(m.db, cutoff); err != nil {
		progress(fmt.Sprintf("error: dns checks: %v", err))
		errs = append(errs, fmt.Errorf("dns checks: %w", err))
	} else if removed > 0 {
		progress(fmt.Sprintf("removed %d dns check result(s) older than the mail retention (%d days) and the %d-hour reuse window",
			removed, keepDays, int(dnscheck.ReuseWindow.Hours())))
	}
	if stale, err := models.DeleteOwnSourceLookupsOlderThan(m.db, now.Add(-OwnSourceCacheKeepAge)); err != nil {
		progress(fmt.Sprintf("error: own source lookups: %v", err))
		errs = append(errs, fmt.Errorf("own source lookups: %w", err))
	} else if stale > 0 {
		progress(fmt.Sprintf("removed %d stored own source lookup(s) older than %d days", stale, int(OwnSourceCacheKeepAge.Hours()/24)))
	}
	if future, err := models.DeleteOwnSourceLookupsNewerThan(m.db, now.Add(OwnSourceCacheMaxFuture)); err != nil {
		progress(fmt.Sprintf("error: own source lookups: %v", err))
		errs = append(errs, fmt.Errorf("own source lookups: %w", err))
	} else if future > 0 {
		progress(fmt.Sprintf("removed %d stored own source lookup(s) dated more than %d hours in the future", future,
			int(OwnSourceCacheMaxFuture.Hours())))
	}
	return errors.Join(errs...)
}
