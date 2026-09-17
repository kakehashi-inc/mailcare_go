package mailengine

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"mailcare/app/models"
)

// GroupMailbox is the grouping phase (design 5.4): it classifies the
// messages the phase has not processed yet (messages.classified = 0),
// extracts the bounce details, categorizes them and files them into groups.
// Group counters are refreshed once at the end; an actionable group whose
// message count grew is listed in GroupResult.GroupsTouched.
//
// With full = true every message is done again: the classification is
// cleared first (ClearMailClassification), then all messages are processed
// and groups left without messages are deleted together with their reports.
// A group whose key comes back keeps its state, its reports and its
// needs_analysis flag (set again only when its message count grew), and the
// responsible party named by its latest completed report is re-applied.
func GroupMailbox(ctx context.Context, mailsRoot, address string, full bool, progress Progress) (*GroupResult, error) {
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
	tracker := newGroupTracker()
	exclude := []string{address}
	for i, msg := range msgs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pm, err := loadParsedMessage(dir, msg.MessageKey)
		if err != nil {
			// No usable .json: parse the original again and refresh the
			// derived files while at it.
			raw, rerr := os.ReadFile(filepath.Join(dir, msg.MessageKey+".eml"))
			if rerr != nil {
				report(progress, fmt.Sprintf("skipping %s: %v", msg.MessageKey, rerr))
				continue
			}
			pm = ParseMessage(raw)
			pm.Source = Source{
				MessageKey: msg.MessageKey, Folder: msg.Folder, UIDValidity: msg.UIDValidity, UID: msg.UID,
				Size: msg.Size, ReceivedAt: msg.ReceivedAt.Time, FetchedAt: msg.FetchedAt,
			}
			if err := writeDerivedFiles(dir, msg.MessageKey, pm); err != nil {
				return nil, err
			}
		}
		if err := groupMessage(db, msg, pm, tracker, exclude...); err != nil {
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
	touched, err := tracker.refresh(db)
	if err != nil {
		return nil, fmt.Errorf("refresh groups: %w", err)
	}
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
	report(progress, fmt.Sprintf("grouped %d messages, %d bounces, %d groups (%d actionable groups gained messages)",
		result.Processed, result.Bounces, result.Groups, len(touched)))
	return result, nil
}

// groupMessage runs classification, extraction, categorization and grouping
// for one index row and marks it classified (design 5.3 + 5.4). exclude
// lists the addresses never taken as failed recipient (the monitored
// address); the sender of the notice itself is always excluded.
func groupMessage(db *sql.DB, msg *models.Message, pm *ParsedMessage, tracker *groupTracker, exclude ...string) error {
	cls := Classify(pm)
	var bounce *models.Bounce
	var group *models.BounceGroup
	groupKey := ""
	if cls.IsBounce && cls.Kind != bounceKindAutoReply {
		bounce = ExtractBounce(pm, cls.Kind, append([]string{pm.FromAddress}, exclude...)...)
		group = groupForBounce(cls.Kind, bounce)
		if group != nil {
			groupKey = group.GroupKey
		}
	}
	if err := models.UpdateMessageClassification(db, msg.ID, cls.IsBounce, cls.Kind, cls.Reason, groupKey); err != nil {
		return err
	}
	msg.IsBounce, msg.BounceKind, msg.ClassifyReason, msg.GroupKey, msg.Classified = cls.IsBounce, cls.Kind, cls.Reason, groupKey, true
	if bounce != nil {
		bounce.MessageID = msg.ID
		if err := models.UpsertBounce(db, bounce); err != nil {
			return err
		}
	}
	return tracker.upsert(db, group)
}

// countAllGroups returns the number of groups in the index regardless of
// state and actionability.
func countAllGroups(db *sql.DB) (int, error) {
	counts, err := models.CountGroups(db, nil)
	if err != nil {
		return 0, fmt.Errorf("count groups: %w", err)
	}
	return counts.Open + counts.Resolved + counts.Ignored, nil
}
