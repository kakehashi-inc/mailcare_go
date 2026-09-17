package mailengine

import (
	"database/sql"
	"errors"
	"fmt"
	"os"

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

// errCarryoverOutdated is returned by readCarryover when the previous index
// has an older schema: its rows cannot be read through the models, so
// nothing is carried over.
var errCarryoverOutdated = errors.New("previous index has an outdated schema")

// readCarryover reads the groups and agent reports of the index at path
// through the models. The file is opened directly (not through OpenMailIndex,
// which would fail on an outdated schema); when its schema version is not
// the current one, errCarryoverOutdated is returned and the caller continues
// without carrying anything over. A missing file yields nil, nil.
func readCarryover(path string) (*carryover, error) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_time_format=sqlite")
	if err != nil {
		return nil, err
	}
	defer db.Close()

	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return nil, err
	}
	if version != models.MailIndexSchemaVersion {
		return nil, fmt.Errorf("%w (version %d, current %d)", errCarryoverOutdated, version, models.MailIndexSchemaVersion)
	}

	c := &carryover{groups: map[string]carriedGroup{}}
	groups, err := models.ListGroups(db, models.GroupFilter{})
	if err != nil {
		return nil, err
	}
	for _, g := range groups {
		c.groups[g.GroupKey] = carriedGroup{
			State: g.State, StateUpdatedAt: g.StateUpdatedAt, NeedsAnalysis: g.NeedsAnalysis, MessageCount: g.MessageCount,
		}
	}
	c.reports, err = models.ListAllAgentReports(db)
	if err != nil {
		return nil, err
	}
	return c, nil
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
		g, err := models.GetGroup(db, key)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return groups, reports, fmt.Errorf("lookup group %s: %w", key, err)
		}
		needs := old.NeedsAnalysis || g.MessageCount > old.MessageCount
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
// a grouping / reindex resets it to the machine-derived value.
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
