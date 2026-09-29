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
}

// carryover holds what Reindex keeps from the previous index: the IMAP
// identity and fetch facts of every message (by message key, since the raw
// file does not record them), and the group states and agent reports, which
// are restored for the group keys that come back (group keys are
// deterministic, so a rebuilt group is the same problem).
type carryover struct {
	sources  map[string]models.MessageSource
	groups   map[string]carriedGroup
	reports  []*models.AgentReport // in old id order
	patterns map[int64][]string    // report id -> pattern keys the report covered
}

// messageSource returns the carried source of a message key (nil when the
// previous index did not know the key, or when nothing was carried over).
func (c *carryover) messageSource(key string) *models.MessageSource {
	if c == nil {
		return nil
	}
	if s, ok := c.sources[key]; ok {
		return &s
	}
	return nil
}

// readCarryover reads the message sources, the groups and the agent reports
// (with the patterns they covered) of the index at path through the models.
// The file is opened with models.OpenMailIndex, which first migrates it to
// the current schema, so an index written by an older version is read like
// a current one. A missing file yields nil, nil.
func readCarryover(path string) (*carryover, error) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	db, err := models.OpenMailIndex(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	c := &carryover{groups: map[string]carriedGroup{}}
	c.sources, err = models.ListMessageSources(db)
	if err != nil {
		return nil, err
	}
	groups, err := models.ListGroups(db, models.GroupFilter{})
	if err != nil {
		return nil, err
	}
	for _, g := range groups {
		c.groups[g.GroupKey] = carriedGroup{
			State: g.State, StateUpdatedAt: g.StateUpdatedAt, NeedsAnalysis: g.NeedsAnalysis,
		}
	}
	c.reports, err = models.ListAllAgentReports(db)
	if err != nil {
		return nil, err
	}
	c.patterns, err = models.ListAllAgentReportPatterns(db)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// apply restores the carried state, reports and report patterns into the
// rebuilt index for the group keys that exist there. needs_analysis is
// carried over as it was, except that an actionable group with a member
// pattern (bounce or DMARC record) its latest completed report did not
// cover is flagged again (a
// recipient-side group is never flagged, RestoreGroupState). It returns how
// many groups and reports were restored.
func (c *carryover) apply(db *sql.DB) (groups, reports int, err error) {
	if c == nil {
		return 0, 0, nil
	}
	restored := map[string]*models.BounceGroup{}
	for key := range c.groups {
		g, err := models.GetGroup(db, key)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return groups, reports, fmt.Errorf("lookup group %s: %w", key, err)
		}
		restored[key] = g
	}
	for _, r := range c.reports {
		if restored[r.GroupKey] == nil {
			continue
		}
		if err := models.RestoreAgentReport(db, r); err != nil {
			return groups, reports, fmt.Errorf("restore report of %s: %w", r.GroupKey, err)
		}
		if err := models.InsertAgentReportPatterns(db, r.ID, c.patterns[r.ID]); err != nil {
			return groups, reports, fmt.Errorf("restore report patterns of %s: %w", r.GroupKey, err)
		}
		reports++
	}
	for key, g := range restored {
		old := c.groups[key]
		uncovered, err := models.GroupHasUncoveredPattern(db, key)
		if err != nil {
			return groups, reports, fmt.Errorf("check patterns of %s: %w", key, err)
		}
		needs := old.NeedsAnalysis || (g.Actionable && uncovered)
		if err := models.RestoreGroupState(db, key, old.State, old.StateUpdatedAt, needs); err != nil {
			return groups, reports, fmt.Errorf("restore group %s: %w", key, err)
		}
		groups++
	}
	return groups, reports, nil
}

// reapplyReportResponsible re-applies the responsible party named by the
// latest completed agent report of every group, because the group upsert of
// a grouping / reindex resets it to the machine-derived value. As when the
// report was first stored (agent.AnalyzeGroup), only a definite answer
// (sender / recipient / domain) replaces the rule-based value; "unknown", an
// empty or an unexpected value leaves it as the grouping derived it.
func reapplyReportResponsible(db *sql.DB) error {
	latest, err := models.LatestCompletedAgentReports(db)
	if err != nil {
		return err
	}
	for key, r := range latest {
		if !isDefiniteResponsible(r.Responsible) {
			continue
		}
		if err := models.UpdateGroupResponsible(db, key, r.Responsible); err != nil {
			return err
		}
	}
	return nil
}

// isDefiniteResponsible reports whether v names an actual party (sender,
// recipient or domain) rather than "", unknown or anything unexpected.
func isDefiniteResponsible(v string) bool {
	switch v {
	case responsibleSender, responsibleRecipient, responsibleDomain:
		return true
	}
	return false
}
