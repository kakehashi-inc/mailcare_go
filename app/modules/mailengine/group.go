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
func GroupMailbox(ctx context.Context, mailsRoot, address string, full bool, recheck RecheckDays, progress Progress) (*GroupResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
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
		if err := groupMessage(db, msg, ParseMessage(raw), tracker, exclude...); err != nil {
			return nil, fmt.Errorf("group %s: %w", msg.MessageKey, err)
		}
		result.Processed++
		if msg.IsBounce {
			result.Bounces++
		}
		if (i+1)%progressInterval == 0 {
			report(progress, fmt.Sprintf("grouped %d of %d messages", i+1, len(msgs)))
		}
	}
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
func groupMessage(db *sql.DB, msg *models.Message, pm *ParsedMessage, tracker *groupTracker, exclude ...string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := groupMessageIn(tx, msg, pm, tracker, exclude...); err != nil {
		return err
	}
	return tx.Commit()
}

// groupMessageIn is the body of groupMessage over an Execer (a transaction
// in production; the tests also drive it with a plain database or a failing
// wrapper to check the write order).
func groupMessageIn(db models.Execer, msg *models.Message, pm *ParsedMessage, tracker *groupTracker, exclude ...string) error {
	cls := Classify(pm, mailboxOf(exclude))
	var bounce *models.Bounce
	var group *models.BounceGroup
	groupKey := ""
	if cls.IsBounce && isGroupedKind(cls.Kind) {
		bounce = ExtractBounce(pm, cls.Kind, append([]string{pm.FromAddress}, exclude...)...)
		group = groupForBounce(cls.Kind, bounce)
		if group != nil {
			groupKey = group.GroupKey
		}
	}
	var records []*models.DMARCRecord
	var recordGroups []*reportGroup
	if cls.Kind == bounceKindReport {
		var groups []*models.BounceGroup
		records, groups = dmarcRecordsFor(pm.DMARC)
		recordGroups = distinctReportGroups(records, groups)
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
