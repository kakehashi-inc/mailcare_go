// Package dnscheck verifies the DNS lookup failures that notices report
// (design document "mail classification" 4.4): a notice such as "Name
// service error for name=example.jp type=MX: Host not found, try again"
// cannot tell by itself whether the name does not exist or the resolver of
// the reporting server failed, so the name is looked up again through
// several recursive resolvers and, for a notice that clearly concerns mail
// sent from the sending sources (the direct argument of Verify), the
// authoritative servers of its
// domain, and the answers are voted into an outcome (exists / not_found /
// broken).
//
// The queries are made with a small DNS client of the package (built on
// golang.org/x/net/dns/dnsmessage) because the standard resolver reports
// NXDOMAIN, an empty answer and a server failure alike. Only the OS resolver
// is used when the settings ask for it (Config.SystemOnly); it cannot tell
// NXDOMAIN from an empty answer.
//
// Outcomes are stored in dns_checks (master database) and reused by every
// notice about the same name whose window holds the check (ReuseRange: from
// 24 hours before the notice was received to 48 hours after), the earliest
// first; a notice that shows mail sent from the sending sources takes those the authoritative
// servers were asked for first (dns_checks.direct, see verify). Once a
// notice has used an outcome, grouping it again uses the same
// row, so a later change of the DNS of the name does not move it. It can
// still move to another group: when it was not verified before and a row
// now falls within its window, when whether its reporting MTA is a sending
// server or whether it shows mail sent from the sending sources is judged
// differently (the sending sources were changed, or their addresses could
// not all be looked up in the run), and when it shows such mail and used an
// outcome
// of the resolvers alone until one with the authoritative servers was
// stored within its window (see verify). A
// Checker belongs to one job run: it remembers the resolvers that failed
// their control query, the MX targets of the recipient domains, the name
// servers of the domains, the addresses of the host names it resolved and
// the inconclusive names of the run. The network time of a Checker is
// bounded: each verification by a deadline (verifyDeadlineFactor times the
// per-query timeout), all of them together with the names a notice chose
// (verifications, MX targets, reporting MTAs) by Config.Budget, and the
// names a notice chose by a count each.
//
// The Checker also looks up the sending sources for the grouping: the
// addresses of the source host names (ResolveHost) and what the SPF records
// of the monitored domains list (LookupSPF, spf.go), both within a separate
// time budget (Config.SourceBudget), so that the names the notices chose
// cannot use it up. While the verification is off,
// SystemHostResolver looks the source host names up through the OS resolver
// alone (systemhosts.go).
package dnscheck

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/publicsuffix"

	"mailcare/app/models"
	"mailcare/app/modules/mailengine"
)

const (
	// DefaultTimeout bounds one query (UDP, then TCP when truncated, each).
	DefaultTimeout = 3 * time.Second
	// DefaultMaxLookups bounds the names verified live by one Checker (one
	// grouping or reindex run). Reused outcomes do not count.
	DefaultMaxLookups = 200
	// DefaultBudget bounds the network time one Checker spends on the names
	// the notices chose: the live verifications, the MX targets of the
	// recipient domains and the addresses of the reporting MTAs. The sending
	// sources (ResolveHost, LookupSPF) do not count; DefaultSourceBudget bounds
	// them.
	DefaultBudget = 10 * time.Minute
	// DefaultSourceBudget bounds the network time one Checker spends on the
	// sending sources: the addresses of the source host names and the SPF
	// records of the monitored domains. Once it is used up they are no
	// longer looked up (a source host name is not answered, an SPF record
	// incomplete).
	DefaultSourceBudget = 2 * time.Minute
	// defaultPort is the DNS port.
	defaultPort = 53
	// verifyDeadlineFactor times the per-query timeout bounds one live
	// verification (the resolvers, the name servers of the domain and the
	// authoritative servers together) and one lookup of a reporting MTA.
	verifyDeadlineFactor = 4
	// defaultMaxMXDomains bounds the recipient domains whose MX targets one
	// Checker looks up for the applicability of a name, and
	// defaultMaxReporters the reporting MTA names it resolves (both chosen
	// by the notices).
	defaultMaxMXDomains = 200
	defaultMaxReporters = 200

	// controlName is the stable name every resolver must resolve (an A
	// record) before its answers count: a resolver that cannot is
	// unreachable or filtered (a closed network), and an NXDOMAIN from it
	// would be no evidence.
	controlName = "example.com"
	// maxNameServers bounds the authoritative servers asked per domain, and
	// maxServerAddrs the addresses tried per server.
	maxNameServers = 4
	maxServerAddrs = 2
	// authBlockedAfter is the number of different organization domains
	// whose authoritative servers left every query unanswered, after which
	// the authoritative servers are no longer asked in the run: the network
	// then most likely blocks DNS to anything but the resolvers, and every
	// name would wait for the timeouts in vain. A domain counts once its
	// servers were asked and none of them replied to any query (each such
	// address is not asked again in the run either); a domain whose servers
	// were not asked (no NS, no usable address, every address silent
	// already, the queries cut off) does not count. Counting domains rather
	// than queries keeps one domain with many silent servers (4 name servers
	// with 2 addresses each, hostile or broken) from switching the stage off
	// for the names of every other domain. A domain that answered resets
	// nothing: it is not counted itself, the silent domains before it stay
	// counted, and the stage goes off at the third silent domain however
	// many domains answered in between.
	authBlockedAfter = 3
	// maxDetailBytes bounds dns_checks.detail.
	maxDetailBytes = 4096
	// maxNoteValues bounds the records quoted per answer in the evidence.
	maxNoteValues = 5
)

// Config is the configuration of a Checker, taken from the settings
// dns_check_enabled, dns_check_resolvers and dns_check_system_resolver_only.
type Config struct {
	Enabled      bool
	StoredOnly   bool          // the check is off: Verify reuses stored outcomes, nothing is ever queried (see New)
	Resolvers    []string      // resolver IPs, asked on port 53 (Port in tests)
	SystemOnly   bool          // ask the OS resolver only (no authoritative server directly); Resolvers is ignored
	Port         int           // tests only; 0 = 53
	Timeout      time.Duration // per query; 0 = DefaultTimeout
	MaxLookups   int           // names verified live per Checker; 0 = DefaultMaxLookups
	Budget       time.Duration // network time per Checker (see DefaultBudget); 0 = DefaultBudget
	SourceBudget time.Duration // network time of the sending sources per Checker (see DefaultSourceBudget); 0 = DefaultSourceBudget
}

// Stats counts the Verify calls of a Checker by how they ended
// (mailengine.DNSVerifyStatus). Every call counts in exactly one field.
type Stats struct {
	Verified      int // looked up now; the outcome was stored
	Reused        int // the outcome came from dns_checks (or from earlier in the run)
	OverBudget    int // no outcome: the budget, MaxLookups or the recipient domains of the MX lookups of the Checker were used up
	NotApplicable int // no outcome: the name may not be looked up for the notice, or is no usable name
	Inconclusive  int // no outcome: no resolver gave a usable answer in time (about the name or the MX targets)
	TooOld        int // no outcome: none stored, and the notice is too old for a live verification
	CheckOff      int // no outcome: none stored, and the check is off (StoredOnly or not enabled)
}

// add counts one call that ended with status.
func (s *Stats) add(status mailengine.DNSVerifyStatus) {
	switch status {
	case mailengine.DNSStatusVerified:
		s.Verified++
	case mailengine.DNSStatusReused:
		s.Reused++
	case mailengine.DNSStatusOverBudget:
		s.OverBudget++
	case mailengine.DNSStatusNotApplicable:
		s.NotApplicable++
	case mailengine.DNSStatusTooOld:
		s.TooOld++
	case mailengine.DNSStatusCheckOff:
		s.CheckOff++
	default:
		s.Inconclusive++
	}
}

// Checker verifies the DNS lookup failures of one job run (see the package
// documentation). Create one per run with New; it is safe for concurrent
// use, although the grouping calls it from one goroutine.
type Checker struct {
	db           *sql.DB
	enabled      bool
	storedOnly   bool // Config.StoredOnly: no query at all, the stored outcomes only
	systemOnly   bool // Config.SystemOnly: no authoritative server is asked directly
	paths        []resolverPath
	port         uint16
	timeout      time.Duration
	maxLookups   int
	budget       time.Duration
	sourceBudget time.Duration

	// sys is the OS resolver: the resolver of the SystemOnly mode, and the
	// one that knows the single-label host names of the settings (tests
	// point it at a fake server).
	sys systemResolver
	// now is the clock of the reuse window and of the stored rows
	// (time.Now; tests move it).
	now func() time.Time
	// allowLocal accepts authoritative servers on loopback and private
	// addresses (tests only; see serverAllowed).
	allowLocal bool
	// maxMXDomains and maxReporters are defaultMaxMXDomains and
	// defaultMaxReporters, the spf fields the bounds of an SPF evaluation
	// (spf.go); tests may lower them.
	maxMXDomains    int
	maxReporters    int
	spfMaxQueries   int
	spfMaxDepth     int
	spfMaxPrefixes  int
	spfMaxMXTargets int

	mu              sync.Mutex
	controls        []*controlState           // per path; nil until its control query ran
	mxTargets       map[string]mxLookup       // recipient domain -> its MX target names
	mxLookups       int                       // recipient domains whose MX targets were looked up
	nameServers     map[string]*nameServerSet // organization domain -> its servers
	hosts           map[string]hostLookup     // ResolveHost results
	reporters       map[string]reporterResult // ResolveReporter results
	spf             map[string]SPFResult      // LookupSPF results
	reporterLookups int                       // reporting MTA names looked up
	results         map[memoKey]memo          // outcomes of this run
	silent          map[netip.AddrPort]bool   // authoritative addresses that did not answer
	authDomains     map[string]bool           // organization domain -> a server of it replied (false: asked, none replied)
	lookups         int                       // live verifications started
	spent           time.Duration             // network time counted against the budget
	sourceSpent     time.Duration             // network time counted against the budget of the sending sources
	stats           Stats
}

// resolverPath is one recursive resolver the verification asks: a
// configured address (asked with the DNS client of the package) or, when addr
// is not valid, the OS resolver.
type resolverPath struct {
	label string
	addr  netip.AddrPort
}

// controlState is the result of the control query of a resolver.
type controlState struct {
	ok   bool
	note string
}

// nameServer is an authoritative server of a domain and the addresses to
// ask it at.
type nameServer struct {
	host  string
	addrs []netip.Addr
}

// nameServerSet is the authoritative servers of a domain, or why there are
// none (note).
type nameServerSet struct {
	servers []nameServer
	note    string
}

// checkKey identifies a verification (and a dns_checks row): the name and
// the record type.
type checkKey struct {
	name, qtype string
}

// memo is the outcome of a verification made earlier in the run (ok false:
// inconclusive), the time it stands for (when the row it comes from was
// checked, or when the inconclusive verification was made) and whether the
// authoritative servers were asked (direct, like dns_checks.direct). It
// counts only for a notice whose window holds that time (ReuseRange), like
// the row, and for a notice that shows mail sent from the sending sources like a row of its kind
// (see verify).
type memo struct {
	outcome mailengine.DNSOutcome
	ok      bool
	at      time.Time
	direct  bool
}

// memoKey identifies a memo of the run: the verification and whether the
// authoritative servers were asked in it.
type memoKey struct {
	checkKey
	direct bool
}

// mxLookup is the lookup of the MX targets of a recipient domain made
// earlier in the run: the target names, and whether a resolver answered
// (an unanswered lookup has no targets).
type mxLookup struct {
	targets  map[string]bool
	answered bool
}

// reporterResult is a lookup of a reporting MTA made earlier in the run:
// its addresses and whether a resolver answered (see ResolveReporter).
type reporterResult struct {
	addrs    []netip.Addr
	answered bool
}

// hostLookup is a lookup of one of the source host names made earlier in the
// run: its addresses and whether a resolver answered (see ResolveHost).
type hostLookup struct {
	addrs    []netip.Addr
	answered bool
}

// New returns a Checker for one job run. db is the master database (nil
// stores and reuses nothing). Invalid resolver entries are skipped. New
// makes no query. With Config.StoredOnly (the check is switched off) the
// Checker never makes one: Verify returns the outcome stored for a notice
// (the reuse rule, see ReuseRange) and leaves every other name not
// verified, ResolveHost and ResolveReporter answer nothing and LookupSPF
// reads nothing, so that switching the check off stops every query yet
// files the notices as the stored outcomes say.
func New(db *sql.DB, cfg Config) *Checker {
	c := &Checker{
		db:              db,
		enabled:         cfg.Enabled,
		storedOnly:      cfg.StoredOnly,
		systemOnly:      cfg.SystemOnly,
		port:            defaultPort,
		timeout:         cfg.Timeout,
		maxLookups:      cfg.MaxLookups,
		budget:          cfg.Budget,
		sourceBudget:    cfg.SourceBudget,
		sys:             defaultSystemResolver(),
		now:             time.Now,
		maxMXDomains:    defaultMaxMXDomains,
		maxReporters:    defaultMaxReporters,
		spfMaxQueries:   defaultSPFMaxQueries,
		spfMaxDepth:     defaultSPFMaxDepth,
		spfMaxPrefixes:  defaultSPFMaxPrefixes,
		spfMaxMXTargets: defaultSPFMaxMXTargets,
		spf:             map[string]SPFResult{},
		mxTargets:       map[string]mxLookup{},
		nameServers:     map[string]*nameServerSet{},
		hosts:           map[string]hostLookup{},
		reporters:       map[string]reporterResult{},
		results:         map[memoKey]memo{},
		silent:          map[netip.AddrPort]bool{},
		authDomains:     map[string]bool{},
	}
	if cfg.Port > 0 && cfg.Port <= 65535 {
		c.port = uint16(cfg.Port)
	}
	if c.timeout <= 0 {
		c.timeout = DefaultTimeout
	}
	if c.maxLookups <= 0 {
		c.maxLookups = DefaultMaxLookups
	}
	if c.budget <= 0 {
		c.budget = DefaultBudget
	}
	if c.sourceBudget <= 0 {
		c.sourceBudget = DefaultSourceBudget
	}
	if cfg.SystemOnly {
		c.paths = []resolverPath{{label: "system"}}
	} else {
		seen := map[netip.Addr]bool{}
		for _, s := range cfg.Resolvers {
			a, err := netip.ParseAddr(strings.TrimSpace(s))
			if err != nil {
				continue
			}
			a = a.Unmap()
			if seen[a] {
				continue
			}
			seen[a] = true
			c.paths = append(c.paths, resolverPath{label: a.String(), addr: netip.AddrPortFrom(a, c.port)})
		}
	}
	c.controls = make([]*controlState, len(c.paths))
	return c
}

// Stats returns the counts of the Verify calls so far.
func (c *Checker) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stats
}

// Verify verifies the name of a DNS lookup failure a notice reports
// (mailengine.DNSVerifier). recipientDomain is the domain of the failed
// recipient and noticeTime when the notice was received (zero = now).
// direct lets a live verification ask the authoritative servers of the
// name as well (never in the SystemOnly mode); without it only the
// resolvers vote. The outcome is returned with ok = true; ok = false means
// not verified: the verification is off, no outcome is stored for the
// notice while it is too old for a live verification
// (mailengine.LiveCheckAllowed) or the Checker is StoredOnly, the name is
// neither in the organization domain of the recipient domain nor one of
// its MX targets, the run reached MaxLookups or used up its Budget, or no
// resolver gave a usable answer in time (inconclusive). VerifyStatus tells
// which.
func (c *Checker) Verify(ctx context.Context, f mailengine.DNSFailure, recipientDomain string, noticeTime time.Time, direct bool) (mailengine.DNSOutcome, bool) {
	outcome, _ := c.VerifyStatus(ctx, f, recipientDomain, noticeTime, direct)
	return outcome, outcome != ""
}

// VerifyStatus is Verify that also tells how the verification ended
// (mailengine.DNSStatusVerifier, for the progress line of the run); the
// outcome is "" when the name was not verified. Every call counts in Stats.
func (c *Checker) VerifyStatus(ctx context.Context, f mailengine.DNSFailure, recipientDomain string, noticeTime time.Time, direct bool) (mailengine.DNSOutcome, mailengine.DNSVerifyStatus) {
	outcome, status := c.verify(ctx, f, recipientDomain, noticeTime, direct)
	c.mu.Lock()
	c.stats.add(status)
	c.mu.Unlock()
	return outcome, status
}

// verify is VerifyStatus without the counting: the outcome ("" = not
// verified) and how it was reached. An outcome stored for the notice
// (within its window, ReuseRange) or remembered from earlier in the run
// (at a time within that window) comes first and costs no query at all
// (not even the MX lookup of the applicability); only then is the name
// verified live, and only for a recent notice of a Checker that may query,
// bounded by the deadline of the verification. The window and the age of
// the notice are taken at one moment, so a notice old enough to be
// verified live always has that moment in its window.
//
// A notice that shows mail sent from the sending sources (direct, while the authoritative servers
// may be asked at all: not in the SystemOnly mode, and not once the stage
// is off for the run, authBlocked) uses only an outcome the authoritative
// servers were asked for (a direct row or memo), so that an outcome of the
// resolvers alone, stored for a notice that did not show such mail, does
// not decide it: without one it is verified live with them and stores a
// direct row. An outcome of the resolvers alone stands in only when that
// cannot be done or gives nothing: the notice is too old for a live
// verification, the Checker may not query, the name may not be looked up
// for the notice, the limits of the run are reached, or the live
// verification is inconclusive. Any other notice uses an outcome of either
// kind, the earliest of its window.
func (c *Checker) verify(ctx context.Context, f mailengine.DNSFailure, recipientDomain string, noticeTime time.Time, direct bool) (mailengine.DNSOutcome, mailengine.DNSVerifyStatus) {
	if !c.enabled && !c.storedOnly {
		return "", mailengine.DNSStatusCheckOff
	}
	name, ok := normalizeName(f.Name)
	if !ok {
		return "", mailengine.DNSStatusNotApplicable
	}
	qtype, ok := normalizeQType(f.QType)
	if !ok {
		return "", mailengine.DNSStatusNotApplicable
	}
	key := checkKey{name: name, qtype: qtype}
	now := c.now()
	from, to := ReuseRange(noticeTime, now)
	tryDirect := direct && !c.systemOnly && !c.authBlocked()
	// otherwise is how a notice that shows mail sent from the sending sources
	// ends without an outcome of the direct kind: with one of the resolvers
	// alone when there is one.
	otherwise := func(status mailengine.DNSVerifyStatus) (mailengine.DNSOutcome, mailengine.DNSVerifyStatus) {
		if tryDirect {
			if outcome, reused, ok := c.reuse(key, from, to, false); ok {
				return outcome, reused
			}
		}
		return "", status
	}
	if outcome, status, ok := c.reuse(key, from, to, tryDirect); ok {
		if status == mailengine.DNSStatusReused {
			return outcome, status
		}
		// Inconclusive earlier in the run: not verified again.
		return otherwise(status)
	}
	if !c.live() {
		return otherwise(mailengine.DNSStatusCheckOff)
	}
	if !mailengine.LiveCheckAllowed(noticeTime, now) {
		return otherwise(mailengine.DNSStatusTooOld)
	}
	if applicable, why := c.applicable(ctx, name, recipientDomain); !applicable {
		return otherwise(why)
	}
	if !c.budgetLeft() || !c.takeLookup() {
		return otherwise(mailengine.DNSStatusOverBudget)
	}
	live, cancel := context.WithTimeout(ctx, c.verifyDeadline())
	start := time.Now()
	outcome, ok, detail, asked := c.check(live, name, qtype, direct)
	c.spend(time.Since(start))
	cancel()
	if ended(ctx) {
		// Canceled: what was not answered tells nothing about the name.
		return "", mailengine.DNSStatusInconclusive
	}
	if !ok {
		// Inconclusive, also when the deadline cut the resolvers short: the
		// name is not asked again in the run for a notice whose window
		// holds this moment (one that shows mail sent from the sending sources too when it was
		// such a notice, whether the authoritative stage could run or not)
		// and nothing is stored.
		c.remember(key, memo{at: now, direct: asked || tryDirect})
		return otherwise(mailengine.DNSStatusInconclusive)
	}
	m, status := c.store(key, outcome, detail, asked, from, to)
	c.remember(key, m)
	return m.outcome, status
}

// reuse returns what a notice whose window is [from, to] finds for the name
// without a query (of a verification that asked the authoritative servers
// only, when directOnly): the outcome stored, else the one remembered from
// earlier in the run (reused), or an inconclusive verification of the run
// (inconclusive: not made again). ok is false when there is none of them.
func (c *Checker) reuse(key checkKey, from, to time.Time, directOnly bool) (mailengine.DNSOutcome, mailengine.DNSVerifyStatus, bool) {
	if outcome, ok := c.stored(key, from, to, directOnly); ok {
		return outcome, mailengine.DNSStatusReused, true
	}
	if m, ok := c.remembered(key, from, to, directOnly); ok {
		if m.ok {
			return m.outcome, mailengine.DNSStatusReused, true
		}
		return "", mailengine.DNSStatusInconclusive, true
	}
	return "", mailengine.DNSStatusInconclusive, false
}

// ReuseWindow is how long before a notice was received a stored outcome
// still counts for it (design document "mail classification" 4.4).
const ReuseWindow = 24 * time.Hour

// ReuseRange returns the window of the stored outcomes a notice received at
// noticeTime may use, both ends included: from ReuseWindow before it to
// mailengine.DNSLiveMaxAge after it, the last moment it may be verified live
// (a later lookup says nothing about a failure of days before, and a row a
// recent notice stored today must not decide an old one). A time after now
// (or none) counts as now, so that the outcome checked now is found again.
// The grouping (Verify, which also stores within it) and the analysis,
// which shows the outcome a notice used, both pick the stored outcome
// through it.
func ReuseRange(noticeTime, now time.Time) (from, to time.Time) {
	if noticeTime.IsZero() || noticeTime.After(now) {
		noticeTime = now
	}
	return noticeTime.Add(-ReuseWindow), noticeTime.Add(mailengine.DNSLiveMaxAge)
}

// inRange reports whether t lies within [from, to].
func inRange(t, from, to time.Time) bool {
	return !t.Before(from) && !t.After(to)
}

// live reports whether the Checker may query at all: the check is on and
// the Checker is not StoredOnly.
func (c *Checker) live() bool {
	return c.enabled && !c.storedOnly
}

// verifyDeadline is the time one live verification, or one lookup of a
// reporting MTA, may take: verifyDeadlineFactor times the per-query timeout
// (12 seconds by default).
func (c *Checker) verifyDeadline() time.Duration {
	return verifyDeadlineFactor * c.timeout
}

// budgetLeft reports whether the Checker still has network time for the
// names the notices chose.
func (c *Checker) budgetLeft() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.spent < c.budget
}

// spend counts the wall-clock time of one live operation against the
// budget (the queries it ran in parallel count once).
func (c *Checker) spend(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.spent += d
}

// sourceBudgetLeft reports whether the Checker still has network time for the
// sending sources (their separate budget).
func (c *Checker) sourceBudgetLeft() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sourceSpent < c.sourceBudget
}

// spendSource counts the wall-clock time of one lookup of the sending
// sources against the budget of the sending sources.
func (c *Checker) spendSource(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sourceSpent += d
}

// normalizeQType returns the record type of a failure as stored in
// dns_checks.qtype: MX, A or AAAA; the Sendmail forms name no type and are
// verified as MX.
func normalizeQType(qtype string) (string, bool) {
	switch t := strings.ToUpper(strings.TrimSpace(qtype)); t {
	case "":
		return "MX", true
	case "MX", "A", "AAAA":
		return t, true
	}
	return "", false
}

// applicable reports whether a name may be looked up for a notice to
// recipientDomain: the name of a forged notice is chosen by its author, so
// only a name in the organization domain of the recipient domain, or one of
// the MX targets of the recipient domain, is looked up. When it may not,
// why tells the reason as the status of the verification: not applicable
// (no recipient domain, or the MX targets were found and the name is none
// of them), over budget (the MX targets were needed past the limits of the
// run) or inconclusive (no resolver answered the MX question).
func (c *Checker) applicable(ctx context.Context, name, recipientDomain string) (ok bool, why mailengine.DNSVerifyStatus) {
	domain, ok := normalizeName(strings.TrimPrefix(recipientDomain, "@"))
	if !ok {
		return false, mailengine.DNSStatusNotApplicable
	}
	if organizationDomain(name) == organizationDomain(domain) {
		return true, mailengine.DNSStatusNotApplicable
	}
	targets, why := c.mxTargetsOf(ctx, domain)
	return targets[name], why
}

// stored returns the outcome dns_checks holds for a notice whose window is
// [from, to] (ReuseRange): the earliest one checked within it, of a
// verification that asked the authoritative servers when directOnly.
func (c *Checker) stored(key checkKey, from, to time.Time, directOnly bool) (mailengine.DNSOutcome, bool) {
	if c.db == nil {
		return "", false
	}
	row, err := models.FindDNSCheck(c.db, key.name, key.qtype, from, to, directOnly)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			log.Printf("dns check: failed to read the stored outcome of %s %s: %v", key.name, key.qtype, err)
		}
		return "", false
	}
	return validOutcome(row.Outcome)
}

// validOutcome returns a stored outcome when it is one of the verification
// results.
func validOutcome(s string) (mailengine.DNSOutcome, bool) {
	switch outcome := mailengine.DNSOutcome(s); outcome {
	case mailengine.DNSOutcomeExists, mailengine.DNSOutcomeNotFound, mailengine.DNSOutcomeBroken:
		return outcome, true
	}
	return "", false
}

// store saves a verified outcome with its evidence and whether the
// authoritative servers were asked (direct) unless another run (two jobs
// verifying the same name at once) stored one the notice would use within
// the window [from, to] meanwhile (models.InsertDNSCheckIfAbsent: a direct
// one for a direct outcome, any one else), and returns the row that won:
// this one (verified) or the other run's (reused), so that every notice of
// the window gets the same outcome. The row is dated when the check ended, but
// no later than to: a notice that was just young enough for a live check
// when it started (mailengine.DNSLiveMaxAge) would otherwise store a row a
// few seconds past the window of the notice and lose the outcome when it is grouped
// again. A failure is logged; the outcome of this check is then used (and
// remembered for the rest of the run).
func (c *Checker) store(key checkKey, outcome mailengine.DNSOutcome, detail string, direct bool, from, to time.Time) (memo, mailengine.DNSVerifyStatus) {
	checkedAt := c.now()
	if checkedAt.After(to) {
		checkedAt = to
	}
	local := memo{outcome: outcome, ok: true, at: checkedAt, direct: direct}
	if c.db == nil {
		return local, mailengine.DNSStatusVerified
	}
	row := &models.DNSCheck{Name: key.name, QType: key.qtype, Outcome: string(outcome), Detail: detail, Direct: direct, CheckedAt: checkedAt}
	winner, inserted, err := models.InsertDNSCheckIfAbsent(c.db, row, from, to)
	if err != nil {
		log.Printf("dns check: failed to store the outcome of %s %s: %v", key.name, key.qtype, err)
		return local, mailengine.DNSStatusVerified
	}
	if inserted {
		local.at = row.CheckedAt
		return local, mailengine.DNSStatusVerified
	}
	if other, ok := validOutcome(winner.Outcome); ok {
		return memo{outcome: other, ok: true, at: winner.CheckedAt, direct: winner.Direct}, mailengine.DNSStatusReused
	}
	return local, mailengine.DNSStatusVerified
}

// remembered returns what the run found for the name earlier when it
// counts for a notice whose window is [from, to]: an outcome stored (or
// found) at a time within the window, or an inconclusive verification made
// within it; of a verification that asked the authoritative servers only,
// when directOnly. When both kinds count, an outcome comes before an
// inconclusive verification, and the earlier before the later.
func (c *Checker) remembered(key checkKey, from, to time.Time, directOnly bool) (memo, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var best memo
	found := false
	for _, direct := range []bool{true, false} {
		if directOnly && !direct {
			continue
		}
		m, ok := c.results[memoKey{checkKey: key, direct: direct}]
		if !ok || !inRange(m.at, from, to) {
			continue
		}
		if !found || (m.ok && !best.ok) || (m.ok == best.ok && m.at.Before(best.at)) {
			best, found = m, true
		}
	}
	return best, found
}

func (c *Checker) remember(key checkKey, m memo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.results[memoKey{checkKey: key, direct: m.direct}] = m
}

// takeLookup counts a live verification against MaxLookups; false when the
// run has used them all.
func (c *Checker) takeLookup() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lookups >= c.maxLookups {
		return false
	}
	c.lookups++
	return true
}

// check looks the name up through every reachable resolver and, when
// direct and not in the SystemOnly mode, the authoritative servers of its
// organization domain, and votes the answers (without the authoritative
// servers the resolvers alone vote). It returns the outcome, whether there
// is one, the evidence and whether the authoritative stage ran (asked: the
// name servers were looked up, whatever they answered; not when the
// resolvers were all unreachable or the stage is off for the run). When
// ctx ends (the run is canceled or the deadline of the verification passes)
// before every resolver answered, there is no outcome: a resolver cut off
// would vote as a failure. An authoritative server cut off does not vote.
func (c *Checker) check(ctx context.Context, name, qtype string, direct bool) (outcome mailengine.DNSOutcome, ok bool, detail string, asked bool) {
	controls := c.controlAll(ctx)
	var reachable []resolverPath
	for i, p := range c.paths {
		if controls[i].ok {
			reachable = append(reachable, p)
		}
	}
	answers := make([]answer, len(reachable))
	notes := make([]string, len(reachable))
	each(len(reachable), func(i int) {
		answers[i], notes[i] = c.lookup(ctx, reachable[i], name, qtype)
	})
	if ended(ctx) {
		return "", false, "", false
	}

	var lines []string
	k := 0
	for i, p := range c.paths {
		if !controls[i].ok {
			lines = append(lines, fmt.Sprintf("resolver %s: control failed (%s); not asked", p.label, controls[i].note))
			continue
		}
		lines = append(lines, fmt.Sprintf("resolver %s: control ok; %s", p.label, notes[k]))
		k++
	}
	if len(c.paths) == 0 {
		lines = append(lines, "resolver: none configured")
	}
	var auth []answer
	switch {
	case len(reachable) == 0:
	case c.systemOnly:
		lines = append(lines, "authoritative: not asked (only the OS resolver is used)")
	case !direct:
		lines = append(lines, "authoritative: not asked (the notice does not show mail sent from the sending sources)")
	default:
		var authLines []string
		auth, authLines, asked = c.authoritative(ctx, reachable, name, qtype)
		lines = append(lines, authLines...)
		if ended(ctx) {
			lines = append(lines, "authoritative: cut short by the deadline of the verification")
		}
	}
	outcome, ok = vote(answers, auth)
	result := string(outcome)
	if !ok {
		result = "inconclusive"
	}
	head := fmt.Sprintf("name=%s type=%s outcome=%s", name, qtype, result)
	return outcome, ok, joinDetail(head, lines), asked
}

// joinDetail joins the evidence lines under the head line, cut at a line
// boundary to stay within maxDetailBytes.
func joinDetail(head string, lines []string) string {
	var b strings.Builder
	b.WriteString(head)
	for i, l := range lines {
		if b.Len()+1+len(l) > maxDetailBytes-32 {
			fmt.Fprintf(&b, "\n(%d more lines omitted)", len(lines)-i)
			break
		}
		b.WriteString("\n")
		b.WriteString(l)
	}
	return b.String()
}

// controlAll runs the control query of every resolver whose control has not
// run yet in this run (in parallel) and returns the state of every path. A
// resolver that failed stays unreachable for the life of the Checker; a
// control cut short by ctx is not remembered.
func (c *Checker) controlAll(ctx context.Context) []controlState {
	out := make([]controlState, len(c.paths))
	var pending []int
	c.mu.Lock()
	for i, s := range c.controls {
		if s != nil {
			out[i] = *s
		} else {
			pending = append(pending, i)
		}
	}
	c.mu.Unlock()
	each(len(pending), func(j int) {
		i := pending[j]
		ok, note := c.control(ctx, c.paths[i])
		out[i] = controlState{ok: ok, note: note}
		if ended(ctx) {
			return
		}
		c.mu.Lock()
		c.controls[i] = &out[i]
		c.mu.Unlock()
	})
	return out
}

// reachableResolvers returns the resolvers that passed their control query,
// in the configured order.
func (c *Checker) reachableResolvers(ctx context.Context) []resolverPath {
	controls := c.controlAll(ctx)
	var out []resolverPath
	for i, p := range c.paths {
		if controls[i].ok {
			out = append(out, p)
		}
	}
	return out
}

// control runs the control query of one resolver: it must return an A
// record of controlName.
func (c *Checker) control(ctx context.Context, p resolverPath) (bool, string) {
	if !p.addr.IsValid() {
		return c.systemControl(ctx)
	}
	a, _, note := c.lookupType(ctx, p.addr, controlName, typeA, nil)
	if a != answerData {
		return false, note
	}
	return true, "ok"
}

// lookup asks one reachable resolver the question of a verification.
func (c *Checker) lookup(ctx context.Context, p resolverPath, name, qtype string) (answer, string) {
	if !p.addr.IsValid() {
		return c.systemLookup(ctx, name, qtype)
	}
	return c.lookupTarget(ctx, p.addr, name, qtype, nil)
}

// mxTargetsOf returns the MX target names of a recipient domain, looked up
// once per run through the first resolver that answers (an empty set when
// none does or the domain has no MX; remembered too), and what a name that
// is none of them is for the verification (mailengine.DNSVerifyStatus): not
// applicable when a resolver answered, inconclusive when none did (or ctx
// ended), over budget when the domain was not looked up because
// maxMXDomains domains were already looked up or the budget is used up (a
// later run looks it up).
func (c *Checker) mxTargetsOf(ctx context.Context, domain string) (targets map[string]bool, why mailengine.DNSVerifyStatus) {
	c.mu.Lock()
	known, ok := c.mxTargets[domain]
	switch {
	case ok:
	case c.mxLookups >= c.maxMXDomains, c.spent >= c.budget:
		c.mu.Unlock()
		return nil, mailengine.DNSStatusOverBudget
	default:
		c.mxLookups++
	}
	c.mu.Unlock()
	if ok {
		return known.targets, mxStatus(known.answered)
	}
	start := time.Now()
	defer func() { c.spend(time.Since(start)) }()
	found := mxLookup{targets: map[string]bool{}}
	for _, p := range c.reachableResolvers(ctx) {
		var hosts []string
		var a answer
		if p.addr.IsValid() {
			a, hosts, _ = c.lookupType(ctx, p.addr, domain, typeMX, nil)
		} else {
			hosts, a = c.systemMX(ctx, domain)
		}
		if a == answerFail || a == answerNone {
			continue
		}
		for _, h := range hosts {
			if n, ok := normalizeName(h); ok {
				found.targets[n] = true
			}
		}
		found.answered = true
		break
	}
	if ended(ctx) {
		return found.targets, mailengine.DNSStatusInconclusive
	}
	c.mu.Lock()
	c.mxTargets[domain] = found
	c.mu.Unlock()
	return found.targets, mxStatus(found.answered)
}

// mxStatus is the status of a name that is none of the MX targets of a
// domain whose lookup was answered or not.
func mxStatus(answered bool) mailengine.DNSVerifyStatus {
	if answered {
		return mailengine.DNSStatusNotApplicable
	}
	return mailengine.DNSStatusInconclusive
}

// ResolveHost returns the A and AAAA addresses of one of the source host names
// (sending_source_hosts and the SMTP hosts of the monitored mail addresses,
// configured by the administrator and trusted), remembered for the run
// (mailengine.SourceHostResolver), and whether the lookup was answered. The
// OS resolver is asked first (sourceHostAddresses): a name of two labels or
// more as an absolute name, a single-label name (an internal MTA such as
// "mailhost") as written, so that its search list and hosts file apply.
// Only when it gives no address is a name of two labels or more asked of
// the configured resolvers (the first that answers both questions, see
// sourceAddresses; not in the SystemOnly mode, whose resolver is the OS one):
// a name of a split-horizon DNS resolves only inside, and a public
// resolver knows no single-label name. Each lookup counts against the
// budget of the sending sources (DefaultSourceBudget), not the budget of the
// verification.
//
// answered is true when both the A and the AAAA question were answered,
// with addresses or without any (NXDOMAIN, no data). It is false when the
// verification is off (or StoredOnly), the name is no valid host name, the budget of the
// sending sources is used up, no resolver answered both questions (the
// addresses one of them gave are still returned) or ctx ended. The cache of
// the sending sources (app/modules) stores only answered lookups, and the
// grouping counts an unanswered one as incomplete (an address of a sending
// source may be missing).
func (c *Checker) ResolveHost(ctx context.Context, host string) (addrs []netip.Addr, answered bool) {
	if !c.live() {
		return nil, false
	}
	name, ok := normalizeHost(host, 1)
	if !ok {
		return nil, false
	}
	c.mu.Lock()
	res, ok := c.hosts[name]
	c.mu.Unlock()
	if ok {
		return res.addrs, res.answered
	}
	if !c.sourceBudgetLeft() {
		return nil, false
	}
	start := time.Now()
	res.addrs, res.answered = c.sourceHostAddresses(ctx, name)
	c.spendSource(time.Since(start))
	if ended(ctx) {
		return res.addrs, false
	}
	c.mu.Lock()
	c.hosts[name] = res
	c.mu.Unlock()
	return res.addrs, res.answered
}

// sourceHostAddresses looks one of the source host names up for ResolveHost: the
// OS resolver first, whose addresses are taken whenever it gives any
// (answered when it answered both families; half an answer is not
// completed elsewhere). When it gives none, a name of two labels or more is
// asked of the configured resolvers (sourceAddresses), whose answer decides
// when one answered both questions or gave an address; otherwise the OS
// resolver's answer without addresses stands when it answered both
// families, and the lookup is not answered when nobody did.
func (c *Checker) sourceHostAddresses(ctx context.Context, name string) ([]netip.Addr, bool) {
	multi := strings.Contains(name, ".")
	query := name
	if multi {
		query = absolute(name)
	}
	addrs, answered := lookupSourceSystem(ctx, c.sys, c.timeout, query)
	if len(addrs) > 0 || !multi || c.systemOnly || ended(ctx) {
		return addrs, answered
	}
	public, publicAnswered := c.sourceAddresses(ctx, name)
	if publicAnswered || len(public) > 0 {
		return public, publicAnswered
	}
	return nil, answered
}

// sourceAddresses returns the A and AAAA addresses of one of the source host
// names (two labels or more) through the first reachable resolver that
// answers both questions (each with data, no data or NXDOMAIN), and whether
// one did. A resolver that answers only one of them (the other a SERVFAIL
// or no reply) does not count: the next one is asked, and when none answers
// both, the addresses found first are returned unanswered, so that half an
// answer never replaces a whole one stored before.
func (c *Checker) sourceAddresses(ctx context.Context, host string) ([]netip.Addr, bool) {
	var partial []netip.Addr
	for _, p := range c.reachableResolvers(ctx) {
		var addrs []netip.Addr
		answered := true
		if !p.addr.IsValid() {
			addrs, answered = lookupSourceSystem(ctx, c.sys, c.timeout, absolute(host))
		} else {
			for _, t := range []queryType{typeA, typeAAAA} {
				a, values, _ := c.lookupType(ctx, p.addr, host, t, nil)
				for _, v := range values {
					if addr, err := netip.ParseAddr(v); err == nil {
						addrs = append(addrs, addr)
					}
				}
				if a == answerFail || a == answerNone {
					answered = false
				}
			}
		}
		if answered {
			return addrs, true
		}
		if partial == nil {
			partial = addrs
		}
	}
	return partial, false
}

// ResolveReporter returns the A and AAAA addresses of the host name a
// notice gives as its reporting MTA (mailengine.HostResolver), to compare
// them with the sending sources, and whether the answer is definitive.
// The name comes from the notice, so it is untrusted: it must be a host name
// of two labels or more (never an IP literal), at most maxReporters names
// are looked up per Checker, each lookup is bounded by the deadline of a
// verification and counts against the budget, and the result is remembered
// for the run (a lookup no resolver answered too).
//
// answered is true when a resolver answered (with addresses, or with none:
// NXDOMAIN or no data). It is false when the verification is off or the
// Checker is StoredOnly, the name is refused (no host name of two labels or
// more; it is never looked up), the run reached maxReporters or used up its
// budget before the name was looked up, no resolver answered in time, or
// ctx ended: the caller cannot tell then whether the reporting MTA is a
// sending server, and a later notice of the run would not get the same answer as
// an earlier one.
func (c *Checker) ResolveReporter(ctx context.Context, host string) (addrs []netip.Addr, answered bool) {
	if !c.live() {
		return nil, false
	}
	name, ok := normalizeName(host)
	if !ok {
		return nil, false
	}
	c.mu.Lock()
	res, cached := c.reporters[name]
	switch {
	case cached:
	case c.reporterLookups >= c.maxReporters, c.spent >= c.budget:
		c.mu.Unlock()
		return nil, false
	default:
		c.reporterLookups++
	}
	c.mu.Unlock()
	if cached {
		return res.addrs, res.answered
	}
	live, cancel := context.WithTimeout(ctx, c.verifyDeadline())
	start := time.Now()
	res.addrs, res.answered = c.firstAddresses(live, name)
	c.spend(time.Since(start))
	cancel()
	if ended(ctx) {
		return res.addrs, res.answered
	}
	c.mu.Lock()
	c.reporters[name] = res
	c.mu.Unlock()
	return res.addrs, res.answered
}

// firstAddresses returns the A and AAAA addresses of a host through the
// first reachable resolver that answers (none when the host has none), and
// whether one answered.
func (c *Checker) firstAddresses(ctx context.Context, host string) ([]netip.Addr, bool) {
	for _, p := range c.reachableResolvers(ctx) {
		addrs, a := c.addressesOf(ctx, p, host)
		if a == answerFail || a == answerNone {
			continue
		}
		return addrs, true
	}
	return nil, false
}

// addressesOf returns the A and AAAA addresses of a host through one
// resolver, and the answer (data when there is any address).
func (c *Checker) addressesOf(ctx context.Context, p resolverPath, host string) ([]netip.Addr, answer) {
	if !p.addr.IsValid() {
		return c.systemAddresses(ctx, host)
	}
	var addrs []netip.Addr
	result := answerNoData
	for _, t := range []queryType{typeA, typeAAAA} {
		a, values, _ := c.lookupType(ctx, p.addr, host, t, nil)
		for _, v := range values {
			if addr, err := netip.ParseAddr(v); err == nil {
				addrs = append(addrs, addr)
			}
		}
		result = combine(result, a)
	}
	if len(addrs) > 0 {
		return addrs, answerData
	}
	return nil, result
}

// combine merges the answers of the A and AAAA lookups of one name, or of
// the implicit MX lookups: data wins, then a failure, then NXDOMAIN.
func combine(a, b answer) answer {
	rank := func(x answer) int {
		switch x {
		case answerData:
			return 4
		case answerFail, answerNone:
			return 3
		case answerNX:
			return 2
		}
		return 1
	}
	if rank(b) > rank(a) {
		return b
	}
	return a
}

// authoritative asks the authoritative servers of the organization domain
// of the name (found through the reachable resolvers) the same question
// without recursion. It returns their answers (answerNone for the servers
// that did not answer or are not authoritative for the name), the evidence
// lines and whether the stage ran (false only when it is off for the run,
// authBlocked).
func (c *Checker) authoritative(ctx context.Context, reachable []resolverPath, name, qtype string) ([]answer, []string, bool) {
	if c.authBlocked() {
		return nil, []string{fmt.Sprintf("authoritative: skipped (the authoritative servers of %d domains did not answer in this run)", authBlockedAfter)}, false
	}
	org := organizationDomain(name)
	set := c.nameServersOf(ctx, reachable, org)
	if len(set.servers) == 0 {
		return nil, []string{fmt.Sprintf("authoritative: %s", set.note)}, true
	}
	answers := make([]answer, len(set.servers))
	lines := make([]string, len(set.servers))
	tally := &authTally{}
	each(len(set.servers), func(i int) {
		s := set.servers[i]
		answers[i] = answerNone
		lines[i] = fmt.Sprintf("authoritative %s: no usable address", s.host)
		for _, addr := range s.addrs {
			server := netip.AddrPortFrom(addr, c.port)
			if c.isSilent(server) {
				lines[i] = fmt.Sprintf("authoritative %s (%s): no response earlier in this run", s.host, addr)
				continue
			}
			a, note := c.lookupTarget(ctx, server, name, qtype, tally)
			answers[i] = a
			lines[i] = fmt.Sprintf("authoritative %s (%s): %s", s.host, addr, note)
			if !c.isSilent(server) {
				break
			}
		}
	})
	c.noteAuthDomain(org, tally)
	return answers, lines, true
}

// nameServersOf returns the authoritative servers of a domain: its NS
// records from the first reachable resolver that answers, at most
// maxNameServers of them (sorted by name), each with up to maxServerAddrs
// usable addresses (IPv4 first) looked up through the same resolver.
// Remembered for the run.
func (c *Checker) nameServersOf(ctx context.Context, reachable []resolverPath, org string) *nameServerSet {
	c.mu.Lock()
	set, ok := c.nameServers[org]
	c.mu.Unlock()
	if ok {
		return set
	}
	set = &nameServerSet{note: fmt.Sprintf("NS lookup of %s failed", org)}
	for _, p := range reachable {
		var hosts []string
		var a answer
		if p.addr.IsValid() {
			a, hosts, _ = c.lookupType(ctx, p.addr, org, typeNS, nil)
		} else {
			hosts, a = c.systemNS(ctx, org)
		}
		if a == answerFail || a == answerNone {
			continue
		}
		set.note = fmt.Sprintf("no NS records for %s", org)
		names := map[string]bool{}
		for _, h := range hosts {
			if n, ok := normalizeName(h); ok {
				names[n] = true
			}
		}
		sorted := make([]string, 0, len(names))
		for n := range names {
			sorted = append(sorted, n)
		}
		sort.Strings(sorted)
		if len(sorted) > maxNameServers {
			sorted = sorted[:maxNameServers]
		}
		servers := make([]nameServer, len(sorted))
		each(len(sorted), func(i int) {
			addrs, _ := c.addressesOf(ctx, p, sorted[i])
			servers[i] = nameServer{host: sorted[i], addrs: c.usableServerAddrs(addrs)}
		})
		set.servers = servers
		break
	}
	if ended(ctx) {
		return set
	}
	c.mu.Lock()
	c.nameServers[org] = set
	c.mu.Unlock()
	return set
}

// usableServerAddrs keeps the addresses an authoritative server may be
// asked at (serverAllowed), IPv4 first, at most maxServerAddrs.
func (c *Checker) usableServerAddrs(addrs []netip.Addr) []netip.Addr {
	var v4, v6 []netip.Addr
	for _, a := range addrs {
		a = a.Unmap()
		if !c.serverAllowed(a) {
			continue
		}
		if a.Is4() {
			v4 = append(v4, a)
		} else {
			v6 = append(v6, a)
		}
	}
	out := append(v4, v6...)
	if len(out) > maxServerAddrs {
		out = out[:maxServerAddrs]
	}
	return out
}

// specialPurpose lists the special-purpose ranges (IANA registries) an
// authoritative server is never asked at, besides what the predicates of
// netip.Addr exclude (the two overlap in part).
var specialPurpose = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8",          // this network
		"100.64.0.0/10",      // shared address space (carrier-grade NAT)
		"192.0.0.0/24",       // IETF protocol assignments
		"192.0.2.0/24",       // documentation (TEST-NET-1)
		"198.18.0.0/15",      // benchmarking
		"198.51.100.0/24",    // documentation (TEST-NET-2)
		"203.0.113.0/24",     // documentation (TEST-NET-3)
		"240.0.0.0/4",        // reserved
		"255.255.255.255/32", // limited broadcast
		"fc00::/7",           // unique local
		"fe80::/10",          // link-local
		"64:ff9b::/96",       // NAT64
		"64:ff9b:1::/48",     // local-use NAT64
		"2002::/16",          // 6to4
		"2001::/32",          // Teredo
		"2001:db8::/32",      // documentation
		"100::/64",           // discard-only
		"fec0::/10",          // site-local (deprecated)
		"::/96",              // IPv4-compatible (deprecated)
		"3fff::/20",          // documentation (RFC 9637)
		"5f00::/16",          // segment routing SIDs
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// serverAllowed reports whether an authoritative server may be asked at the
// address: a global unicast address outside the special-purpose ranges,
// after unmapping an IPv4-mapped IPv6 address. The NS records of a domain
// are chosen by its owner, so loopback, private, link-local, multicast,
// unspecified, documentation, translation and similar addresses are never
// asked (tests set allowLocal for their loopback servers).
func (c *Checker) serverAllowed(a netip.Addr) bool {
	if !a.IsValid() || a.Zone() != "" {
		return false
	}
	if c.allowLocal {
		return true
	}
	a = a.Unmap()
	if !a.IsGlobalUnicast() || a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsMulticast() || a.IsUnspecified() {
		return false
	}
	for _, p := range specialPurpose {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

// noteAuthReply records whether an authoritative server answered a query:
// an address that did not is not asked again in the run.
func (c *Checker) noteAuthReply(server netip.AddrPort, answered bool) {
	if answered {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.silent[server] = true
}

func (c *Checker) isSilent(server netip.AddrPort) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.silent[server]
}

// authTally counts the authoritative queries of one verification that were
// noted (not cut off by the end of ctx) and how many of them got a reply,
// for the silence of the organization domain asked (noteAuthDomain). The
// servers of a domain are asked concurrently.
type authTally struct {
	mu              sync.Mutex
	asked, answered int
}

func (t *authTally) note(answered bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.asked++
	if answered {
		t.answered++
	}
}

// noteAuthDomain records how the authoritative servers of the organization
// domain org took the queries of one verification (see authBlockedAfter): a
// domain one of whose servers replied stays answered for the rest of the
// run, one that was asked without any reply is silent until a reply comes,
// and nothing is recorded when no query was noted.
func (c *Checker) noteAuthDomain(org string, t *authTally) {
	t.mu.Lock()
	asked, answered := t.asked, t.answered
	t.mu.Unlock()
	if asked == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if answered > 0 {
		c.authDomains[org] = true
		return
	}
	if _, known := c.authDomains[org]; !known {
		c.authDomains[org] = false
	}
}

// authBlocked reports whether authBlockedAfter organization domains were
// silent in this run, so that the authoritative servers are no longer
// asked.
func (c *Checker) authBlocked() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	silent := 0
	for _, answered := range c.authDomains {
		if !answered {
			silent++
		}
	}
	return silent >= authBlockedAfter
}

// ended reports whether ctx is canceled or its deadline has passed. The
// sockets of the queries carry the deadline of ctx itself, so a query can
// time out at the deadline a moment before the timer of ctx marks it done;
// what such a query did not get tells nothing about the name or the server
// either.
func ended(ctx context.Context) bool {
	if ctx.Err() != nil {
		return true
	}
	deadline, ok := ctx.Deadline()
	return ok && !time.Now().Before(deadline)
}

// each runs fn(0) ... fn(n-1) concurrently and waits for all of them.
func each(n int, fn func(i int)) {
	if n == 1 {
		fn(0)
		return
	}
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() { fn(i) })
	}
	wg.Wait()
}

// normalizeName lower-cases a host name, removes its trailing dot and
// validates it: at least two labels of letters, digits and hyphens (1-63
// characters, no hyphen at either end), at most 253 characters, and a
// top-level label that is not all digits (an IP literal is no name). It
// repeats the check of mailengine.ParseDNSFailure, since every name a
// notice chose and that is looked up here must pass it.
func normalizeName(s string) (string, bool) {
	return normalizeHost(s, 2)
}

// normalizeHost is normalizeName with the least number of labels given:
// the host names of the settings may be single-label names.
func normalizeHost(s string, minLabels int) (string, bool) {
	name := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".")
	if name == "" || len(name) > 253 {
		return "", false
	}
	labels := strings.Split(name, ".")
	if len(labels) < minLabels {
		return "", false
	}
	for _, label := range labels {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", false
		}
		for i := 0; i < len(label); i++ {
			ch := label[i]
			if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') && ch != '-' {
				return "", false
			}
		}
	}
	if !strings.ContainsAny(labels[len(labels)-1], "abcdefghijklmnopqrstuvwxyz") {
		return "", false
	}
	return name, true
}

// organizationDomain returns the registrable domain of a normalized name
// (the public suffix plus one label, "mx01.example.co.jp" ->
// "example.co.jp"); a name the public suffix list cannot place is returned
// as it is.
func organizationDomain(name string) string {
	if org, err := publicsuffix.EffectiveTLDPlusOne(name); err == nil {
		return org
	}
	return name
}

// quote joins the records of an answer for the evidence, at most
// maxNoteValues of them.
func quote(values []string) string {
	if len(values) > maxNoteValues {
		return strings.Join(values[:maxNoteValues], ", ") + fmt.Sprintf(", +%d more", len(values)-maxNoteValues)
	}
	return strings.Join(values, ", ")
}
