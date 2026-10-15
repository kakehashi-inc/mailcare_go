package mailengine

import (
	"context"
	"database/sql"
	"fmt"

	"mailcare/app/models"
)

// GroupMailbox is the grouping phase (design 5.4): it classifies the
// messages the phase has not processed yet (messages.classified = 0),
// extracts the bounce details and the failing records of DMARC reports,
// categorizes them and files them into groups. Every message is parsed
// again from its .eml (the index holds no parsed copy); the writes of one
// message form one transaction (groupMessage), so the counters of a group
// are always in step with its members. An actionable group that received a
// member (bounce or DMARC record) of a pattern its latest completed report
// did not cover is flagged for analysis and listed in
// GroupResult.GroupsTouched.
//
// With full = true every message is done again: the classification is
// cleared first (ClearMailClassification, which also drops every bounces
// and dmarc_records row), then all messages are processed and groups left without messages
// are deleted together with their reports. A group whose key comes back
// keeps its state, its reports and its needs_analysis flag (set again only
// when one of its patterns is not covered by its latest completed report),
// and the responsible party named by its latest completed report is
// re-applied.
//
// WithDNS verifies the DNS lookup failures the notices report (design
// document "mail classification" 4.4) before the transaction of each
// message; without it every detected failure stays not verified.
// WithOwnSources gives the classification our own sending servers for the
// backscatter rule (2.6); without it the rule does not apply.
// WithOwnHostResolver resolves their host names and WithOwnSPF reads the
// SPF records of the monitored domains for both judgements.
func GroupMailbox(ctx context.Context, mailsRoot, address string, full bool, recheck RecheckDays, progress Progress, opts ...GroupOption) (*GroupResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cfg := newGroupConfig(opts)
	dir := MailboxDir(mailsRoot, address)
	db, err := OpenIndex(ctx, mailsRoot, address, progress)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	if full {
		if err := models.ClearMailClassification(db); err != nil {
			return nil, fmt.Errorf("clear classification: %w", err)
		}
	}
	msgs, err := models.ListUnclassifiedMessages(db)
	if err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}
	report(progress, fmt.Sprintf("grouping %d messages", len(msgs)))
	result := &GroupResult{GroupsTouched: []string{}}
	tracker := newGroupTracker(recheck)
	junk := &noticeJunkCounts{}
	exclude := []string{address}
	for i, msg := range msgs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		raw, err := readRawMessage(dir, msg.MessageKey)
		if err != nil {
			// Without the original nothing can be decided; the row stays
			// unclassified and is tried again next time.
			report(progress, fmt.Sprintf("skipping %s: %v", msg.MessageKey, err))
			continue
		}
		if err := groupMessage(ctx, db, msg, ParseMessage(raw), tracker, cfg, exclude...); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			return nil, fmt.Errorf("group %s: %w", msg.MessageKey, err)
		}
		result.Processed++
		if msg.IsBounce {
			result.Bounces++
		}
		junk.add(msg.Rule)
		if (i+1)%progressInterval == 0 {
			report(progress, fmt.Sprintf("grouped %d of %d messages", i+1, len(msgs)))
		}
	}
	cfg.dns.reportSummary(progress)
	cfg.lookups.reportSummary(progress, cfg.backscatterApplies())
	junk.reportSummary(progress)
	touched := tracker.touched()
	result.GroupsTouched = touched
	if full {
		if err := models.DeleteEmptyGroups(db); err != nil {
			return nil, fmt.Errorf("delete empty groups: %w", err)
		}
	}
	if err := reapplyReportResponsible(db); err != nil {
		return nil, fmt.Errorf("reapply responsible: %w", err)
	}
	result.Groups, err = countAllGroups(db)
	if err != nil {
		return nil, err
	}
	report(progress, fmt.Sprintf("grouped %d messages, %d bounces, %d groups (%d actionable groups need analysis)",
		result.Processed, result.Bounces, result.Groups, len(touched)))
	return result, nil
}

// noticeJunkCounts counts the messages of one GroupMailbox or Reindex run
// that the junk rules about delivery-failure notices took (the backscatter
// rule and the fake notices F1 and F2), for the progress line of the run:
// these rules can take genuine-looking notices, so the administrator sees
// how many went to junk.
type noticeJunkCounts struct {
	backscatter, fakeLink, fakeAuth int
}

// add counts a message classified in the run by the rule of its
// classification (messages.rule); other rules are not counted.
func (c *noticeJunkCounts) add(rule string) {
	switch rule {
	case ruleBackscatter:
		c.backscatter++
	case ruleFakeNoticeLink:
		c.fakeLink++
	case ruleFakeNoticeAuth:
		c.fakeAuth++
	}
}

// reportSummary adds the progress line of the counts when any message was
// taken by one of the rules.
func (c *noticeJunkCounts) reportSummary(progress Progress) {
	if c.backscatter+c.fakeLink+c.fakeAuth == 0 {
		return
	}
	report(progress, fmt.Sprintf("junk: %d %s, %d %s, %d %s",
		c.backscatter, ruleBackscatter, c.fakeLink, ruleFakeNoticeLink, c.fakeAuth, ruleFakeNoticeAuth))
}

// groupMessage runs classification, extraction, categorization and grouping
// for one index row and marks it classified (design document "mail classification" 2-4, design 5.4). Every write
// of the message happens in one transaction: the group rows (UpsertGroup),
// the bounces row (only for failed / delayed notices; auto-replies, daemon
// mail without failure evidence, junk and ordinary mail get none; it records
// where the diagnostic was found, the category rule and the pattern key) and
// the dmarc_records rows (the failing records of a DMARC report, each with
// its group, category rule and pattern key), the counters and the analysis
// flag of every group touched (groupTracker.recount: set when the pattern of
// the member is not covered by the latest completed report) and finally the
// detection outcome (is_bounce, bounce_kind, rule, the body actually used)
// of the messages row. The order groups -> members -> messages also holds
// without a transaction: an interruption leaves the message unclassified,
// so the next run does it again, and never a classified message without
// its details. msg is updated in place. exclude lists the addresses never
// taken as failed recipient (the monitored address); the sender of the
// notice itself is always excluded.
//
// Everything that needs no database runs before the transaction is opened
// (planGrouping): classification (with the own sending servers of cfg),
// extraction, the verification of a DNS lookup failure (cfg.dns, nil when
// off; it may use the network, and a SQLite write transaction is never held
// meanwhile) and the categorization. A cancellation noticed after that
// writes nothing, so the message is not filed as not verified because the
// verification was cut short.
func groupMessage(ctx context.Context, db *sql.DB, msg *models.Message, pm *ParsedMessage, tracker *groupTracker, cfg groupConfig, exclude ...string) error {
	plan := planGrouping(ctx, msg, pm, cfg, exclude...)
	if err := ctx.Err(); err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := groupMessageIn(tx, msg, plan, tracker); err != nil {
		return err
	}
	return tx.Commit()
}

// messageGrouping is what the grouping step decided for one message before
// anything is written: its classification, the bounce details with their
// group (failed / delayed notices) and the failing records of a DMARC
// report with their distinct groups.
type messageGrouping struct {
	cls          Classification
	bounce       *models.Bounce
	group        *models.BounceGroup
	records      []*models.DMARCRecord
	recordGroups []*reportGroup
}

// planGrouping classifies a message (with the own sending servers of the
// run, which the backscatter rule needs: classifyOwn), extracts the bounce
// details of a failed or delayed notice, verifies the DNS lookup failure
// its diagnostic reports (dnsVerification.verdict; only these notices reach
// the verifier, never junk, DMARC reports or other mail) and categorizes
// it, and categorizes the failing records of a DMARC report. It does not
// touch the database.
func planGrouping(ctx context.Context, msg *models.Message, pm *ParsedMessage, cfg groupConfig, exclude ...string) *messageGrouping {
	plan := &messageGrouping{cls: classifyOwn(ctx, pm, mailboxOf(exclude), cfg)}
	if plan.cls.IsBounce && isGroupedKind(plan.cls.Kind) {
		plan.bounce = ExtractBounce(pm, plan.cls.Kind, append([]string{pm.FromAddress}, exclude...)...)
		verdict := cfg.dns.verdict(ctx, plan.bounce, pm, mailboxOf(exclude), noticeTime(msg))
		plan.group = groupForBounceWith(plan.cls.Kind, plan.bounce, verdict)
	}
	if plan.cls.Kind == bounceKindReport {
		records, groups := dmarcRecordsFor(pm.DMARC)
		plan.records, plan.recordGroups = records, distinctReportGroups(records, groups)
	}
	return plan
}

// classifyOwn classifies a message with the own sending servers of the run
// and, when it was taken for backscatter, again with the addresses the run
// looks up (ownLookups: the addresses of the own host names and the SPF
// records of the monitored domains, looked up on the first such message of
// the run). The classification itself stays pure. The addresses can only
// add matches, and the sources are read by the backscatter rule alone, so
// they can only turn that verdict into another one: a message the rule does
// not take without them needs no lookup, which keeps a run without a
// backscatter candidate from looking anything up. While the lookups of the
// run are incomplete (the lookup of an own host name was not answered, or
// an SPF record could not be evaluated completely), an address of ours may
// be missing, so the message is classified without the rule instead: a
// failure to look a source up never turns a genuine notice into junk.
func classifyOwn(ctx context.Context, pm *ParsedMessage, mailbox string, cfg groupConfig) Classification {
	cls := ClassifyWith(pm, mailbox, cfg.own)
	if cls.Rule != ruleBackscatter {
		return cls
	}
	extra := cfg.lookups.get(ctx)
	if !extra.complete() {
		return ClassifyWith(pm, mailbox, OwnSources{})
	}
	if len(extra.addrs) > 0 || len(extra.prefixes) > 0 || len(extra.hosts) > 0 {
		cls = ClassifyWith(pm, mailbox, cfg.own.withResolved(extra.addrs, extra.prefixes, extra.hosts))
	}
	return cls
}

// groupMessageIn writes what planGrouping decided over an Execer (a
// transaction in production; the tests also drive it with a plain database
// or a failing wrapper to check the write order).
func groupMessageIn(db models.Execer, msg *models.Message, plan *messageGrouping, tracker *groupTracker) error {
	cls, bounce, group, records, recordGroups := plan.cls, plan.bounce, plan.group, plan.records, plan.recordGroups
	groupKey := ""
	if group != nil {
		groupKey = group.GroupKey
	}
	// 1. the groups (descriptive columns; the counters follow the members).
	if err := tracker.upsert(db, group); err != nil {
		return err
	}
	for _, rg := range recordGroups {
		if err := tracker.upsert(db, rg.group); err != nil {
			return err
		}
	}
	// 2. the bounce details and the DMARC records, or the removal of stale
	// ones.
	if bounce != nil {
		bounce.ID = msg.ID
		bounce.GroupKey = groupKey
		if err := models.UpsertBounce(db, bounce); err != nil {
			return err
		}
	} else if err := models.DeleteBounce(db, msg.ID); err != nil {
		return err
	}
	if err := models.ReplaceDMARCRecords(db, msg.ID, records); err != nil {
		return err
	}
	// 3. the counters and the analysis flag of every group touched, once
	// per group however many records of the report it received.
	if bounce != nil {
		member := &models.GroupBounce{Bounce: *bounce, MessageKey: msg.MessageKey, Date: msg.Date.UTC()}
		if err := tracker.recount(db, group, member); err != nil {
			return err
		}
	}
	for _, rg := range recordGroups {
		members := make([]*models.GroupBounce, 0, len(rg.records))
		for _, r := range rg.records {
			members = append(members, &models.GroupBounce{
				Bounce:     models.Bounce{ID: msg.ID, GroupKey: r.GroupKey, PatternKey: r.PatternKey},
				MessageKey: msg.MessageKey, Date: msg.Date.UTC(), DMARC: r,
			})
		}
		if err := tracker.recount(db, rg.group, members...); err != nil {
			return err
		}
	}
	// 4. the detection outcome; the message counts as processed only now.
	if err := models.UpdateMessageClassification(db, msg.ID, cls.IsBounce, cls.Kind, cls.Rule, cls.BodySource); err != nil {
		return err
	}
	msg.IsBounce, msg.BounceKind, msg.Rule, msg.BodySource, msg.GroupKey, msg.Classified =
		cls.IsBounce, cls.Kind, cls.Rule, cls.BodySource, groupKey, true
	return nil
}

// isGroupedKind reports whether a bounce kind gets bounce details and a
// group: only failed and delayed notices do. Auto-replies and daemon mail
// without failure evidence ("other": success DSNs, daemon mail with
// ordinary wording) are recorded as notices but not grouped; a DMARC report
// ("report") gets no bounce details, its failing records are grouped one by
// one (dmarcRecordsFor).
func isGroupedKind(kind string) bool {
	return kind == bounceKindFailed || kind == bounceKindDelayed
}

// reportGroup is one distinct group of the failing records of a DMARC
// report, with one record per distinct pattern key in report order.
type reportGroup struct {
	group   *models.BounceGroup
	records []*models.DMARCRecord
}

// distinctReportGroups folds the group of every record (records[i] belongs
// to groups[i]) into the distinct groups of the report, so that a report
// with many records of the same group writes and recounts that group once.
func distinctReportGroups(records []*models.DMARCRecord, groups []*models.BounceGroup) []*reportGroup {
	var out []*reportGroup
	byKey := map[string]*reportGroup{}
	seen := map[string]bool{}
	for i, g := range groups {
		rg := byKey[g.GroupKey]
		if rg == nil {
			rg = &reportGroup{group: g}
			byKey[g.GroupKey] = rg
			out = append(out, rg)
		}
		if key := g.GroupKey + "|" + records[i].PatternKey; !seen[key] {
			seen[key] = true
			rg.records = append(rg.records, records[i])
		}
	}
	return out
}

// GroupOption configures one GroupMailbox or Reindex run.
type GroupOption func(*groupConfig)

// groupConfig is the configuration of one run built from its options.
type groupConfig struct {
	own     OwnSources       // our own sending servers (WithOwnSources); empty: no backscatter rule
	lookups *ownLookups      // the addresses the run looks up for them (nil: none)
	dns     *dnsVerification // nil: DNS lookup failures are not verified

	// What the options gave, combined into own, lookups and dns by
	// newGroupConfig.
	ownGiven    bool            // WithOwnSources was given
	verifier    DNSVerifier     // WithDNS (nil: not verified)
	dnsOwn      OwnSources      // the sources given to WithDNS
	ownResolver OwnHostResolver // WithOwnHostResolver
	spfResolver OwnSPFResolver  // WithOwnSPF
	spfDomains  []string        // WithOwnSPF
}

// newGroupConfig applies the options of one run. The verification of WithDNS
// tells our own reporting MTAs by the sources of WithOwnSources when that
// option is given too, whatever the order of the options, else by the
// sources given to WithDNS. The host names of those sources are resolved by
// the resolver of WithOwnHostResolver, else by the verifier when it is a
// HostResolver, and the SPF records of WithOwnSPF are evaluated, once for
// the run and for both judgements (ownLookups).
func newGroupConfig(opts []GroupOption) groupConfig {
	var cfg groupConfig
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	dnsOwn := cfg.dnsOwn
	if cfg.ownGiven {
		dnsOwn = cfg.own
	}
	resolver := cfg.ownResolver
	if resolver == nil {
		if r, ok := cfg.verifier.(HostResolver); ok {
			resolver = r
		}
	}
	cfg.lookups = newOwnLookups(resolver, dnsOwn, cfg.spfResolver, cfg.spfDomains)
	cfg.dns = newDNSVerification(cfg.verifier, dnsOwn, cfg.lookups)
	return cfg
}

// backscatterApplies reports whether the backscatter rule applies to the
// run: our own sending servers were given (WithOwnSources) with a host name
// configured for the mailbox (OwnSources.hasHosts).
func (cfg groupConfig) backscatterApplies() bool {
	return cfg.ownGiven && cfg.own.hasHosts()
}

// mailboxOf returns the monitored address among the excluded addresses
// (the first one; "" when none was given).
func mailboxOf(exclude []string) string {
	if len(exclude) == 0 {
		return ""
	}
	return exclude[0]
}

// countAllGroups returns the number of groups in the index regardless of
// state and actionability.
func countAllGroups(db *sql.DB) (int, error) {
	counts, err := models.CountGroups(db, models.GroupScopeAll)
	if err != nil {
		return 0, fmt.Errorf("count groups: %w", err)
	}
	return counts.Open + counts.Resolved + counts.ResolvedRecheck + counts.Ignored + counts.IgnoredRecheck, nil
}
