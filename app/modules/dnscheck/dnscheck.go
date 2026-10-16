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
// The verification does not depend on the record type the notice names
// (the type is only the last question the reporting server asked): it
// asks whether mail can be delivered to the name (resolveName). A mail
// domain is looked up as an MTA would: its MX records and the A and AAAA
// records of every MX target (no count limit), without MX records the A
// and AAAA records of the domain itself (the implicit MX of RFC 5321), a
// null MX (RFC 7505) meaning that it accepts no mail; a host name (an MX
// target of the recipient domain) by its A and AAAA records. exists: an
// address was obtained; not_found: NXDOMAIN; broken: the name exists
// without an address to deliver to, or DNS does not answer normally for it
// (every resolver failed, or the answers disagree; see vote).
//
// Outcomes are stored in dns_checks (master database, qtype NAME, see
// verifyKind) and reused by every notice about the same name while they are
// fresh (CacheFreshAge, the one period of every stored DNS lookup):
// a name checked within it is not looked up again, whatever the age of the
// notice, and an older one is. A notice that shows mail sent from the
// sending sources takes only those the authoritative servers were asked
// for (dns_checks.direct, see verify). When a live verification gives
// nothing, the latest stored outcome of the name stands in, whatever its
// age; while the check is off (StoredOnly) the stored outcomes are used as
// they are, by the window of the notice (ReuseRange). A notice can move to
// another group when it is grouped again: the outcome of its name was
// checked again since and differs, it was not verified before and now is,
// or whether its reporting MTA is a sending server or whether it shows mail
// sent from the sending sources is judged differently (the sending sources
// were changed, or their addresses could not all be looked up in the run).
// A Checker belongs to one job run: it remembers the resolvers that failed
// their control query, the MX targets of the recipient domains, the name
// servers of the domains and the addresses of the host names it resolved.
// Only each query is bounded in time (the per-query timeout, with one
// retransmission over UDP: queryAttempts); a verification, a lookup of a
// reporting MTA and a run have no time or count limit beyond that, so every
// notice of the run is verified. A name no resolver answered at all (every
// query timed out) is inconclusive: it is not stored, and without a stored
// outcome to stand in the next grouping verifies the notice again.
//
// The Checker also looks up the sending sources for the grouping: the
// addresses of the source host names (ResolveHost) and what the SPF records
// of the monitored domains list (LookupSPF, spf.go). While the
// verification is off,
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
	// DefaultTimeout bounds one attempt of a query (the UDP datagram and
	// its reply, the TCP exchange after a truncated reply, the connection to
	// the server) and, times queryAttempts, one call of the OS resolver: a
	// server that does not reply within it is asked once more (UDP), then
	// counts as not answering. The value is the owner's (no standard
	// defines it for this client); it equals the default of the OS resolver
	// (resolv.conf "timeout:5 attempts:2").
	DefaultTimeout = 5 * time.Second
	// queryAttempts is how many times a UDP query is sent to one server
	// before it counts as not answering (the first and one
	// retransmission, as the OS resolver does).
	queryAttempts = 2
	// defaultPort is the DNS port.
	defaultPort = 53
	// verifyKind is what dns_checks.qtype holds for a verification of a
	// name (VerifyKind).
	verifyKind = VerifyKind

	// controlName is the stable name every resolver must resolve (an A
	// record) before its answers count: a resolver that cannot is
	// unreachable or filtered (a closed network), and an NXDOMAIN from it
	// would be no evidence.
	controlName = "example.com"
	// failureTTL is how long an authoritative address that left a query
	// unanswered is not asked again: the longest a resolver may remember a
	// dead or unreachable server (RFC 2308 7.2; the verification asks the
	// authoritative servers as a resolver does). A reply ends it at once.
	failureTTL = 5 * time.Minute
)

// Config is the configuration of a Checker, taken from the settings
// dns_check_enabled, dns_check_resolvers and dns_check_system_resolver_only.
type Config struct {
	Enabled    bool
	StoredOnly bool          // the check is off: Verify reuses stored outcomes, nothing is ever queried (see New)
	Resolvers  []string      // resolver IPs, asked on port 53 (Port in tests)
	SystemOnly bool          // ask the OS resolver only (no authoritative server directly); Resolvers is ignored
	Port       int           // tests only; 0 = 53
	Timeout    time.Duration // per query; 0 = DefaultTimeout
}

// Stats counts the Verify calls of a Checker by how they ended
// (mailengine.DNSVerifyStatus). Every call counts in exactly one field.
type Stats struct {
	Verified      int // looked up now; the outcome was stored
	Reused        int // the outcome came from dns_checks (or from earlier in the run)
	NotApplicable int // no outcome: the name may not be looked up for the notice, or is no usable name
	Inconclusive  int // no outcome: no resolver gave any answer (unreachable, every query timed out), the run was canceled, or no resolver answered the MX question of the applicability
	CheckOff      int // no outcome: none stored, and the check is off (StoredOnly or not enabled)
}

// add counts one call that ended with status.
func (s *Stats) add(status mailengine.DNSVerifyStatus) {
	switch status {
	case mailengine.DNSStatusVerified:
		s.Verified++
	case mailengine.DNSStatusReused:
		s.Reused++
	case mailengine.DNSStatusNotApplicable:
		s.NotApplicable++
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
	db         *sql.DB
	enabled    bool
	storedOnly bool // Config.StoredOnly: no query at all, the stored outcomes only
	systemOnly bool // Config.SystemOnly: no authoritative server is asked directly
	paths      []resolverPath
	port       uint16
	timeout    time.Duration

	// sys is the OS resolver: the resolver of the SystemOnly mode, and the
	// one that knows the single-label host names of the settings (tests
	// point it at a fake server).
	sys systemResolver
	// now is the clock of the fresh period and of the stored rows
	// (time.Now; tests move it).
	now func() time.Time
	// allowLocal accepts authoritative servers on loopback and private
	// addresses (tests only; see serverAllowed).
	allowLocal bool
	// The spf fields are the limits of an SPF evaluation (spf.go, RFC
	// 7208); tests may lower them.
	spfMaxQueries   int
	spfMaxVoid      int
	spfMaxDepth     int
	spfMaxMXTargets int

	mu          sync.Mutex
	controls    []*controlState              // per path; nil until its control query ran
	mxTargets   map[string]mxLookup          // recipient domain -> its MX target names
	nameServers map[string]*nameServerSet    // organization domain -> its servers
	hosts       map[string]hostLookup        // ResolveHost results
	reporters   map[string]reporterResult    // ResolveReporter results
	spf         map[string]SPFResult         // LookupSPF results
	silent      map[netip.AddrPort]time.Time // authoritative address -> when it last left a query unanswered
	stats       Stats
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

// checkKey identifies a verification (and the dns_checks rows of it, qtype
// verifyKind): the name.
type checkKey struct {
	name string
}

// mxLookup is the lookup of the MX targets of a recipient domain made
// earlier in the run: the target names, and whether a resolver answered
// (an unanswered lookup has no targets).
type mxLookup struct {
	targets  map[string]bool
	answered bool
}

// reporterResult is a lookup of a reporting MTA made earlier in the run:
// its addresses and whether they are definitive (see ResolveReporter).
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
// (the window of the notice, see ReuseRange) and leaves every other name not
// verified, ResolveReporter answers from the stored lookups alone,
// ResolveHost answers nothing and LookupSPF reads nothing, so that
// switching the check off stops every query yet files the notices as the
// stored results say.
func New(db *sql.DB, cfg Config) *Checker {
	c := &Checker{
		db:              db,
		enabled:         cfg.Enabled,
		storedOnly:      cfg.StoredOnly,
		systemOnly:      cfg.SystemOnly,
		port:            defaultPort,
		timeout:         cfg.Timeout,
		sys:             defaultSystemResolver(),
		now:             time.Now,
		spfMaxQueries:   spfMaxQueries,
		spfMaxVoid:      spfMaxVoidLookups,
		spfMaxDepth:     spfMaxDepth,
		spfMaxMXTargets: spfMaxMXTargets,
		spf:             map[string]SPFResult{},
		mxTargets:       map[string]mxLookup{},
		nameServers:     map[string]*nameServerSet{},
		hosts:           map[string]hostLookup{},
		reporters:       map[string]reporterResult{},
		silent:          map[netip.AddrPort]time.Time{},
	}
	if cfg.Port > 0 && cfg.Port <= 65535 {
		c.port = uint16(cfg.Port)
	}
	if c.timeout <= 0 {
		c.timeout = DefaultTimeout
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
// (mailengine.DNSVerifier), whatever its age and whatever record type the
// notice names. recipientDomain is the domain of the failed recipient and
// noticeTime when the notice was received (zero = now). direct lets a live
// verification ask the authoritative servers of the name as well (never in
// the SystemOnly mode); without it only the resolvers vote. The outcome is
// returned with ok = true; ok = false means not verified: the verification
// is off and nothing is stored for the notice (or the Checker is
// StoredOnly), the name is neither in the organization domain of the
// recipient domain nor one of its MX targets, or no resolver gave any
// answer (every query timed out) or the run was canceled (inconclusive;
// the next grouping verifies the notice again). VerifyStatus tells which.
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

// Live reports whether the Checker may query (mailengine.LiveVerifier): the
// DNS check is on; a StoredOnly Checker only reuses stored outcomes.
func (c *Checker) Live() bool {
	return c.enabled && !c.storedOnly
}

// verify is VerifyStatus without the counting: the outcome ("" = not
// verified) and how it was reached.
//
// A Checker that may query uses a stored outcome checked within
// CacheFreshAge before now without any query (not even the MX lookup of
// the applicability), the earliest of that period, whatever the age of
// the notice; otherwise it verifies the name live, each query bounded by
// the per-query timeout, and stores the outcome. When the live
// verification gives nothing (the name may not be looked up for the
// notice, or no resolver gave any answer), the latest stored outcome of
// the name stands in, whatever its age (latest); without one the notice
// is not verified and the next grouping verifies it again. Nothing is
// remembered within the run: every notice of the name reads the stored
// outcomes again.
//
// A notice that shows mail sent from the sending sources (direct, while the authoritative servers
// may be asked at all: not in the SystemOnly mode) uses only a fresh outcome the authoritative
// servers were asked for (a direct row), so that an outcome of the
// resolvers alone, stored for a notice that did not show such mail, does
// not decide it: without one it is verified live with them and stores a
// direct row. Any other notice uses a fresh outcome of either kind. The
// stand-in of a live verification that gave nothing is of either kind.
//
// A StoredOnly Checker (the check is off) queries nothing and uses the
// stored outcomes as they are, by the window of the notice (ReuseRange):
// the earliest checked within it, for a direct notice a direct one first.
func (c *Checker) verify(ctx context.Context, f mailengine.DNSFailure, recipientDomain string, noticeTime time.Time, direct bool) (mailengine.DNSOutcome, mailengine.DNSVerifyStatus) {
	if !c.enabled && !c.storedOnly {
		return "", mailengine.DNSStatusCheckOff
	}
	name, ok := normalizeName(f.Name)
	if !ok {
		return "", mailengine.DNSStatusNotApplicable
	}
	key := checkKey{name: name}
	now := c.now()
	tryDirect := direct && !c.systemOnly
	if !c.live() {
		from, to := ReuseRange(noticeTime, now)
		if outcome, ok := c.stored(key, from, to, tryDirect); ok {
			return outcome, mailengine.DNSStatusReused
		}
		if tryDirect {
			if outcome, ok := c.stored(key, from, to, false); ok {
				return outcome, mailengine.DNSStatusReused
			}
		}
		return "", mailengine.DNSStatusCheckOff
	}
	from := now.Add(-CacheFreshAge)
	if outcome, ok := c.stored(key, from, now, tryDirect); ok {
		return outcome, mailengine.DNSStatusReused
	}
	// otherwise is how a live verification that gave nothing ends: with
	// the latest stored outcome when there is one.
	otherwise := func(status mailengine.DNSVerifyStatus) (mailengine.DNSOutcome, mailengine.DNSVerifyStatus) {
		if outcome, ok := c.latest(key); ok {
			return outcome, mailengine.DNSStatusReused
		}
		return "", status
	}
	applicable, host, why := c.applicable(ctx, name, recipientDomain)
	if ended(ctx) {
		return "", mailengine.DNSStatusInconclusive
	}
	if !applicable {
		return otherwise(why)
	}
	outcome, ok, detail, asked := c.check(ctx, name, host, direct)
	if ended(ctx) {
		// Canceled: what was not answered tells nothing about the name.
		return "", mailengine.DNSStatusInconclusive
	}
	if !ok {
		// Inconclusive (no resolver is reachable, or none gave any answer:
		// every query timed out): nothing is stored.
		return otherwise(mailengine.DNSStatusInconclusive)
	}
	return c.store(key, outcome, detail, asked, from, now)
}

// VerifyKind is what dns_checks.qtype holds for a verification of a name:
// the verification does not depend on the record type a notice names (the
// rows of the earlier verification per type, MX / A / AAAA, are not used
// and go with the daily cleanup).
const VerifyKind = "NAME"

// CacheFreshAge is how long a stored DNS lookup result is used instead of
// asking again: one value for every stored lookup (the verifications of
// dns_checks, and the sending sources and the reporting MTAs of
// sending_source_lookups). A result checked longer ago is looked up again
// by a Checker that may query; it still stands in when the new lookup gives
// nothing, and a StoredOnly Checker (the check is off) uses the stored
// results as they are. Many records have a TTL of 3600 seconds, but reusing
// a result for one hour only would multiply the queries: 12 hours is the
// owner's practical value that keeps the number of queries low (no
// standard defines it).
const CacheFreshAge = 12 * time.Hour

// ReuseWindow is how long before a notice was received a stored outcome
// still counts for it while the check is off (a StoredOnly Checker,
// ReuseRange), and how long before the mail retention the daily cleanup
// keeps the stored outcomes and the stored reporting MTAs.
const ReuseWindow = 24 * time.Hour

// ReuseRange returns the window of the stored outcomes a notice received at
// noticeTime may use while the check is off (a StoredOnly Checker), both
// ends included: from ReuseWindow before it up to now, whatever the age of
// the notice. The earliest outcome of the window is used, so that switching
// the check off keeps the outcome a notice was filed by. A time after now
// (or none) counts as now.
func ReuseRange(noticeTime, now time.Time) (from, to time.Time) {
	if noticeTime.IsZero() || noticeTime.After(now) {
		noticeTime = now
	}
	return noticeTime.Add(-ReuseWindow), now
}

// live reports whether the Checker may query at all: the check is on and
// the Checker is not StoredOnly.
func (c *Checker) live() bool {
	return c.enabled && !c.storedOnly
}

// applicable reports whether a name may be looked up for a notice to
// recipientDomain: the name of a forged notice is chosen by its author, so
// only the recipient domain, one of its MX targets or a name in its
// organization domain is looked up. host tells how the name is verified:
// an MX target of the recipient domain as a host (its A and AAAA), any other
// name as a mail domain (its MX, see resolveName). When it may not, why
// tells the reason as the status of the verification: not applicable (no
// recipient domain, or the MX targets were found and the name is none of
// them nor in the organization domain) or inconclusive (no resolver
// answered the MX question).
func (c *Checker) applicable(ctx context.Context, name, recipientDomain string) (ok, host bool, why mailengine.DNSVerifyStatus) {
	domain, ok := normalizeName(strings.TrimPrefix(recipientDomain, "@"))
	if !ok {
		return false, false, mailengine.DNSStatusNotApplicable
	}
	if name == domain {
		return true, false, mailengine.DNSStatusNotApplicable
	}
	targets, why := c.mxTargetsOf(ctx, domain)
	if targets[name] {
		return true, true, why
	}
	if organizationDomain(name) == organizationDomain(domain) {
		return true, false, why
	}
	return false, false, why
}

// stored returns the outcome dns_checks holds for a notice within the
// window [from, to] (the fresh period of a live Checker, ReuseRange of a
// StoredOnly one): the earliest one checked within it, of a verification
// that asked the authoritative servers when directOnly.
func (c *Checker) stored(key checkKey, from, to time.Time, directOnly bool) (mailengine.DNSOutcome, bool) {
	if c.db == nil {
		return "", false
	}
	row, err := models.FindDNSCheck(c.db, key.name, verifyKind, from, to, directOnly)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			log.Printf("dns check: failed to read the stored outcome of %s: %v", key.name, err)
		}
		return "", false
	}
	return validOutcome(row.Outcome)
}

// latest returns the latest outcome dns_checks holds for the name, whatever
// its age and kind: the stand-in of a live verification that gave nothing.
func (c *Checker) latest(key checkKey) (mailengine.DNSOutcome, bool) {
	if c.db == nil {
		return "", false
	}
	row, err := models.LatestDNSCheck(c.db, key.name, verifyKind)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			log.Printf("dns check: failed to read the stored outcome of %s: %v", key.name, err)
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
// verifying the same name at once) stored one the notice would use
// meanwhile (models.InsertDNSCheckIfAbsent: a direct one for a direct
// outcome, any one else; checked from `from`, the start of the fresh
// period, up to now, since the other run dates its row when it took its
// time, which may be after this one's), and returns the row that won: this
// one (verified) or the other run's (reused), so that every notice of the
// name gets the same outcome. The row is dated `to` (when the verification
// took its time). A failure is logged; the outcome of this check is then
// used.
func (c *Checker) store(key checkKey, outcome mailengine.DNSOutcome, detail string, direct bool, from, to time.Time) (mailengine.DNSOutcome, mailengine.DNSVerifyStatus) {
	checkedAt := to
	if c.db == nil {
		return outcome, mailengine.DNSStatusVerified
	}
	row := &models.DNSCheck{Name: key.name, QType: verifyKind, Outcome: string(outcome), Detail: detail, Direct: direct, CheckedAt: checkedAt}
	end := c.now()
	if end.Before(to) {
		end = to
	}
	winner, inserted, err := models.InsertDNSCheckIfAbsent(c.db, row, from, end)
	if err != nil {
		log.Printf("dns check: failed to store the outcome of %s: %v", key.name, err)
		return outcome, mailengine.DNSStatusVerified
	}
	if inserted {
		return outcome, mailengine.DNSStatusVerified
	}
	if other, ok := validOutcome(winner.Outcome); ok {
		return other, mailengine.DNSStatusReused
	}
	return outcome, mailengine.DNSStatusVerified
}

// check verifies the name through every reachable resolver (resolveName:
// as a mail domain, or as a host when host) and, when direct and not in
// the SystemOnly mode, asks the authoritative servers of its organization
// domain whether the name exists, and votes the answers (without the
// authoritative servers the resolvers alone vote). It returns the outcome,
// whether there is one, the evidence and whether the authoritative stage
// ran (asked: the name servers were looked up, whatever they answered; not
// when the resolvers were all unreachable). When the run is canceled before every resolver answered, there is
// no outcome: what was cut off tells nothing about the name.
func (c *Checker) check(ctx context.Context, name string, host, direct bool) (outcome mailengine.DNSOutcome, ok bool, detail string, asked bool) {
	controls := c.controlAll(ctx)
	var reachable []resolverPath
	for i, p := range c.paths {
		if controls[i].ok {
			reachable = append(reachable, p)
		}
	}
	answers := make([]answer, len(reachable))
	notes := make([]string, len(reachable))
	for i := range reachable {
		answers[i], notes[i] = c.resolveName(ctx, reachable[i], name, host)
	}
	if ended(ctx) {
		return "", false, "", false
	}

	how := "as a mail domain: MX, then the A and AAAA of every MX target (or of the name itself without MX)"
	if host {
		how = "as a host (an MX target of the recipient domain): A and AAAA"
	}
	lines := []string{"checked " + how}
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
		auth, authLines = c.authoritative(ctx, reachable, name, host)
		lines = append(lines, authLines...)
		asked = true
	}
	outcome, ok = vote(answers, auth)
	result := string(outcome)
	if !ok {
		result = "inconclusive"
	}
	head := fmt.Sprintf("name=%s outcome=%s", name, result)
	return outcome, ok, joinDetail(head, lines), asked
}

// joinDetail joins the evidence lines under the head line.
func joinDetail(head string, lines []string) string {
	return strings.Join(append([]string{head}, lines...), "\n")
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
	for _, i := range pending {
		ok, note := c.control(ctx, c.paths[i])
		out[i] = controlState{ok: ok, note: note}
		if ended(ctx) {
			continue
		}
		c.mu.Lock()
		c.controls[i] = &out[i]
		c.mu.Unlock()
	}
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
	a, _, note := c.lookupType(ctx, p.addr, controlName, typeA, false)
	if a != answerData {
		return false, note
	}
	return true, "ok"
}

// resolveName verifies a name through one reachable resolver (the DNS
// client of the package, or the OS resolver in the SystemOnly mode) and
// returns the answer and its evidence: data when an address (A or AAAA)
// was obtained for the mail hosts of a domain (every MX target is looked
// up; without MX records the name itself, the implicit MX of RFC 5321 5.1)
// or for a host, NXDOMAIN when the name does not exist, no data when it
// exists without an address to deliver to (no MX target has an address,
// no address without MX, or a null MX of RFC 7505: the domain accepts no
// mail), and a failure when the answers needed failed. The record type the
// notice named does not matter.
func (c *Checker) resolveName(ctx context.Context, p resolverPath, name string, host bool) (answer, string) {
	if !p.addr.IsValid() {
		return c.systemResolveName(ctx, name, host)
	}
	if host {
		return c.addressAnswer(ctx, p.addr, name)
	}
	a, targets, note := c.lookupType(ctx, p.addr, name, typeMX, false)
	switch a {
	case answerNoData:
		got, addrNote := c.addressAnswer(ctx, p.addr, name)
		return got, "MX none (the name itself is the mail host); " + addrNote
	case answerData:
	default:
		return a, "MX " + note
	}
	hosts := mxHosts(targets)
	if len(hosts) == 0 {
		return answerNoData, "MX " + note + " (null MX: the domain accepts no mail)"
	}
	answers := make([]answer, len(hosts))
	notes := make([]string, len(hosts))
	for i := range hosts {
		answers[i], notes[i] = c.addressAnswer(ctx, p.addr, hosts[i])
		notes[i] = hosts[i] + ": " + notes[i]
	}
	return targetsAnswer(answers), "MX " + note + "; " + strings.Join(notes, "; ")
}

// mxHosts returns the MX target names to look up: the valid host names
// (not the root of a null MX, nothing that is no host name), each once.
func mxHosts(targets []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, h := range targets {
		if h == "." || h == "(invalid name)" || seen[h] {
			continue
		}
		seen[h] = true
		out = append(out, h)
	}
	return out
}

// targetsAnswer combines the answers of the MX targets of a domain: data
// when any target has an address; else no answer when a lookup got no
// reply at all (an address may be missing only because a server did not
// answer); else a failure when a lookup got an error reply (SERVFAIL,
// REFUSED); else no data (no target has an address; a target that does
// not exist counts as one without an address).
func targetsAnswer(answers []answer) answer {
	result := answerNoData
	for _, a := range answers {
		switch a {
		case answerData:
			return answerData
		case answerNone:
			result = answerNone
		case answerFail:
			if result != answerNone {
				result = answerFail
			}
		}
	}
	return result
}

// addressAnswer looks up the A and the AAAA records of a host through one
// server (both, in parallel) and returns the answer: data when either gave
// an address, NXDOMAIN when both said NXDOMAIN, a failure when no address
// came and a lookup failed, else no data.
func (c *Checker) addressAnswer(ctx context.Context, server netip.AddrPort, host string) (answer, string) {
	types := []queryType{typeA, typeAAAA}
	answers := make([]answer, len(types))
	notes := make([]string, len(types))
	for i := range types {
		var note string
		answers[i], _, note = c.lookupType(ctx, server, host, types[i], false)
		notes[i] = typeName(types[i]) + " " + note
	}
	return addressesAnswer(answers), strings.Join(notes, ", ")
}

// addressesAnswer combines the answers of the A and the AAAA lookup of one
// host (see addressAnswer): data when either gave an address; else no
// answer when a lookup got no reply at all; else a failure when one got an
// error reply; NXDOMAIN when both said so; else no data.
func addressesAnswer(answers []answer) answer {
	nx, fail, none := 0, false, false
	for _, a := range answers {
		switch a {
		case answerData:
			return answerData
		case answerNX:
			nx++
		case answerFail:
			fail = true
		case answerNone:
			none = true
		}
	}
	switch {
	case none:
		return answerNone
	case fail:
		return answerFail
	case nx == len(answers):
		return answerNX
	}
	return answerNoData
}

// mxTargetsOf returns the MX target names of a recipient domain, looked up
// once per run through the first resolver that answers (an empty set when
// none does or the domain has no MX; remembered too), and what a name that
// is none of them is for the verification (mailengine.DNSVerifyStatus): not
// applicable when a resolver answered, inconclusive when none did (or ctx
// ended).
func (c *Checker) mxTargetsOf(ctx context.Context, domain string) (targets map[string]bool, why mailengine.DNSVerifyStatus) {
	c.mu.Lock()
	known, ok := c.mxTargets[domain]
	c.mu.Unlock()
	if ok {
		return known.targets, mxStatus(known.answered)
	}
	found := mxLookup{targets: map[string]bool{}}
	for _, p := range c.reachableResolvers(ctx) {
		var hosts []string
		var a answer
		if p.addr.IsValid() {
			a, hosts, _ = c.lookupType(ctx, p.addr, domain, typeMX, false)
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
// resolver knows no single-label name.
//
// answered is true when both the A and the AAAA question were answered,
// with addresses or without any (NXDOMAIN, no data). It is false when the
// verification is off (or StoredOnly), the name is no valid host name, no
// resolver answered both questions (the addresses one of them gave are
// still returned) or ctx ended. The cache of
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
	res.addrs, res.answered = c.sourceHostAddresses(ctx, name)
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
				a, values, _ := c.lookupType(ctx, p.addr, host, t, false)
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
// of two labels or more (never an IP literal), and each query is bounded by
// the per-query timeout.
//
// The addresses of an answered lookup are stored (sending_source_lookups,
// kind reporter: the addresses only, so that whether the reporting MTA is a
// sending server is judged again against the current settings every time)
// and used without asking while fresh (CacheFreshAge), by every run. An
// older row is looked up again; when no resolver answers, the stored row
// stands in, whatever its age. A StoredOnly Checker (the check is off)
// asks nothing and uses the stored row as it is. The result is remembered
// for the run, whatever the age of the notice.
//
// answered is true when a resolver answered (with addresses, or with none:
// NXDOMAIN or no data) or a stored row was used. It is false when the
// verification is off, the name is refused (no host name of two labels or
// more; it is never looked up), no resolver answered in time and nothing
// is stored for the name, the Checker is StoredOnly and nothing is stored,
// or ctx ended before a resolver answered (nothing is stored or
// remembered then, and no stored row stands in): the caller cannot tell then whether the reporting MTA is a
// sending server.
func (c *Checker) ResolveReporter(ctx context.Context, host string) (addrs []netip.Addr, answered bool) {
	if !c.enabled && !c.storedOnly {
		return nil, false
	}
	name, ok := normalizeName(host)
	if !ok {
		return nil, false
	}
	c.mu.Lock()
	res, cached := c.reporters[name]
	c.mu.Unlock()
	if cached {
		return res.addrs, res.answered
	}
	switch row := c.storedReporter(name); {
	case row != nil && (!c.live() || c.fresh(row.FetchedAt)):
		res = reporterResult{addrs: storedAddrs(row), answered: true}
	case !c.live():
		// Nothing stored, and nothing may be asked.
	default:
		res.addrs, res.answered = c.firstAddresses(ctx, name)
		if ended(ctx) {
			return res.addrs, res.answered
		}
		switch {
		case res.answered:
			c.storeReporter(name, res.addrs)
		case row != nil:
			res = reporterResult{addrs: storedAddrs(row), answered: true}
		}
	}
	c.mu.Lock()
	c.reporters[name] = res
	c.mu.Unlock()
	return res.addrs, res.answered
}

// fresh reports whether a stored lookup fetched at t is used without asking
// again: at most CacheFreshAge old. A time after now (the clock was set back
// since it was stored) has no known age and never is.
func (c *Checker) fresh(t time.Time) bool {
	age := c.now().Sub(t)
	return age >= 0 && age <= CacheFreshAge
}

// storedReporter returns the stored lookup of a reporting MTA (nil when
// there is none or it cannot be read).
func (c *Checker) storedReporter(name string) *models.SendingSourceLookup {
	if c.db == nil {
		return nil
	}
	row, err := models.GetSendingSourceLookup(c.db, models.SendingSourceKindReporter, name)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			log.Printf("dns check: failed to read the stored lookup of %s: %v", name, err)
		}
		return nil
	}
	return row
}

// storeReporter stores the answered lookup of a reporting MTA in place of
// the one stored before (an answer without any address too: it is the
// answer). A failure is logged: the stored rows are only a reuse and a
// fallback.
func (c *Checker) storeReporter(name string, addrs []netip.Addr) {
	if c.db == nil {
		return
	}
	values := make([]string, 0, len(addrs))
	for _, a := range addrs {
		values = append(values, a.Unmap().String())
	}
	row := &models.SendingSourceLookup{Kind: models.SendingSourceKindReporter, Name: name, Addresses: values, FetchedAt: c.now()}
	if _, err := models.ReplaceSendingSourceLookup(c.db, row, false); err != nil {
		log.Printf("dns check: failed to store the lookup of %s: %v", name, err)
	}
}

// storedAddrs returns the addresses of a stored lookup (an entry that does
// not parse is skipped).
func storedAddrs(row *models.SendingSourceLookup) []netip.Addr {
	var addrs []netip.Addr
	for _, v := range row.Addresses {
		if a, err := netip.ParseAddr(v); err == nil {
			addrs = append(addrs, a.Unmap())
		}
	}
	return addrs
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
		a, values, _ := c.lookupType(ctx, p.addr, host, t, false)
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
// of the name (found through the reachable resolvers) without recursion
// whether the name exists: its MX records for a mail domain, its A records
// for a host (NXDOMAIN: it does not; any other authoritative answer, data,
// no data or a CNAME: it does). It returns their answers (answerNone for
// the servers that did not answer or are not authoritative for the name),
// and the evidence lines. Every name server and every usable address of it
// is tried (an address that left a query unanswered within failureTTL
// is skipped), each query within the per-query timeout.
func (c *Checker) authoritative(ctx context.Context, reachable []resolverPath, name string, host bool) ([]answer, []string) {
	org := organizationDomain(name)
	set := c.nameServersOf(ctx, reachable, org)
	if len(set.servers) == 0 {
		return nil, []string{fmt.Sprintf("authoritative: %s", set.note)}
	}
	answers := make([]answer, len(set.servers))
	lines := make([]string, len(set.servers))
	for i, s := range set.servers {
		answers[i] = answerNone
		lines[i] = fmt.Sprintf("authoritative %s: no usable address", s.host)
		for _, addr := range s.addrs {
			server := netip.AddrPortFrom(addr, c.port)
			if c.isSilent(server) {
				lines[i] = fmt.Sprintf("authoritative %s (%s): no response in the last %d minutes", s.host, addr, int(failureTTL.Minutes()))
				continue
			}
			t := typeMX
			if host {
				t = typeA
			}
			a, _, note := c.lookupType(ctx, server, name, t, true)
			if a == answerNoData {
				a = answerData // the name exists
			}
			answers[i] = a
			lines[i] = fmt.Sprintf("authoritative %s (%s): %s %s", s.host, addr, typeName(t), note)
			if !c.isSilent(server) {
				break
			}
		}
	}
	return answers, lines
}

// nameServersOf returns the authoritative servers of a domain: its NS
// records from the first reachable resolver that answers (sorted by name),
// each with its usable addresses (IPv4 first) looked up through the same
// resolver. Remembered for the run.
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
			a, hosts, _ = c.lookupType(ctx, p.addr, org, typeNS, false)
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
		servers := make([]nameServer, len(sorted))
		for i, n := range sorted {
			addrs, _ := c.addressesOf(ctx, p, n)
			servers[i] = nameServer{host: n, addrs: c.usableServerAddrs(addrs)}
		}
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
// asked at (serverAllowed), IPv4 first.
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
	return append(v4, v6...)
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
// an address that did not is not asked again for failureTTL, one that
// did is asked again at once.
func (c *Checker) noteAuthReply(server netip.AddrPort, answered bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if answered {
		delete(c.silent, server)
		return
	}
	c.silent[server] = c.now()
}

// isSilent reports whether the address left a query unanswered within
// failureTTL.
func (c *Checker) isSilent(server netip.AddrPort) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	at, ok := c.silent[server]
	return ok && c.now().Sub(at) < failureTTL
}

// ended reports whether ctx is canceled or a deadline its caller set has
// passed (the sockets of the queries are cut at once then, so a query can
// end a moment before the timer of ctx marks it done); what such a query
// did not get tells nothing about the name or the server.
func ended(ctx context.Context) bool {
	if ctx.Err() != nil {
		return true
	}
	deadline, ok := ctx.Deadline()
	return ok && !time.Now().Before(deadline)
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

// quote joins the records of an answer for the evidence (all of them).
func quote(values []string) string {
	return strings.Join(values, ", ")
}
