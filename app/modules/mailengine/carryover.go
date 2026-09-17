package mailengine

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	"mailcare/app/models"
)

// carriedGroup is what Reindex keeps from a group of the previous index.
type carriedGroup struct {
	State          string
	StateUpdatedAt sql.NullTime
	NeedsAnalysis  bool
	MessageCount   int
}

// carryover holds the group states and agent reports of the previous index
// so that Reindex can restore them for the group keys that come back (group
// keys are deterministic, so a rebuilt group is the same problem).
type carryover struct {
	groups  map[string]carriedGroup
	reports []*models.AgentReport // in old id order
}

// readCarryover reads the groups and agent reports of the index at path. The
// file is opened directly (not through OpenMailIndex) so that an index with
// an outdated schema can still be read; on any error nil is returned with the
// error and the caller continues without carrying anything over.
func readCarryover(path string) (*carryover, error) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	defer db.Close()

	c := &carryover{groups: map[string]carriedGroup{}}
	rows, err := db.Query(`SELECT group_key, state, state_updated_at, needs_analysis, message_count FROM groups`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var key, state string
		var stateUpdated any
		var needs, count int
		if err := rows.Scan(&key, &state, &stateUpdated, &needs, &count); err != nil {
			rows.Close()
			return nil, err
		}
		c.groups[key] = carriedGroup{State: state, StateUpdatedAt: anyToNullTime(stateUpdated), NeedsAnalysis: needs != 0, MessageCount: count}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = db.Query(`SELECT id, group_key, provider, status, summary, responsible, severity, report_markdown,
		error_message, message_count, started_at, finished_at, created_at FROM agent_reports ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		r := &models.AgentReport{}
		var started, finished, created any
		if err := rows.Scan(&r.ID, &r.GroupKey, &r.Provider, &r.Status, &r.Summary, &r.Responsible, &r.Severity,
			&r.ReportMarkdown, &r.ErrorMessage, &r.MessageCount, &started, &finished, &created); err != nil {
			return nil, err
		}
		r.StartedAt = anyToNullTime(started)
		r.FinishedAt = anyToNullTime(finished)
		if t := anyToNullTime(created); t.Valid {
			r.CreatedAt = t.Time
		} else {
			r.CreatedAt = time.Now().UTC()
		}
		c.reports = append(c.reports, r)
	}
	return c, rows.Err()
}

// anyToNullTime converts a DATETIME value as returned by the driver (time.Time,
// text or NULL) to sql.NullTime.
func anyToNullTime(v any) sql.NullTime {
	switch t := v.(type) {
	case nil:
		return sql.NullTime{}
	case time.Time:
		return sql.NullTime{Time: t.UTC(), Valid: true}
	case string:
		return models.ParseSQLiteTime(sql.NullString{String: t, Valid: true})
	case []byte:
		return models.ParseSQLiteTime(sql.NullString{String: string(t), Valid: true})
	}
	return sql.NullTime{}
}

// apply restores the carried state and reports into the rebuilt index for
// the group keys that exist there. needs_analysis is carried over as it was,
// except that a group whose message count grew is flagged again. It returns
// how many groups and reports were restored.
func (c *carryover) apply(db *sql.DB) (groups, reports int, err error) {
	if c == nil {
		return 0, 0, nil
	}
	restored := map[string]bool{}
	for key, old := range c.groups {
		var count int
		err := db.QueryRow(`SELECT message_count FROM groups WHERE group_key = ?`, key).Scan(&count)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return groups, reports, fmt.Errorf("lookup group %s: %w", key, err)
		}
		needs := old.NeedsAnalysis || count > old.MessageCount
		if err := models.RestoreGroupState(db, key, old.State, old.StateUpdatedAt, needs); err != nil {
			return groups, reports, fmt.Errorf("restore group %s: %w", key, err)
		}
		restored[key] = true
		groups++
	}
	for _, r := range c.reports {
		if !restored[r.GroupKey] {
			continue
		}
		if err := models.RestoreAgentReport(db, r); err != nil {
			return groups, reports, fmt.Errorf("restore report of %s: %w", r.GroupKey, err)
		}
		reports++
	}
	return groups, reports, nil
}

// reapplyReportResponsible re-applies the responsible party named by the
// latest completed agent report of every group, because the group upsert of
// a reclassify / reindex resets it to the machine-derived value.
func reapplyReportResponsible(db *sql.DB) error {
	latest, err := models.LatestCompletedAgentReports(db)
	if err != nil {
		return err
	}
	for key, r := range latest {
		switch r.Responsible {
		case responsibleSender, responsibleRecipient, responsibleDomain, responsibleUnknown:
			if err := models.UpdateGroupResponsible(db, key, r.Responsible); err != nil {
				return err
			}
		}
	}
	return nil
}
