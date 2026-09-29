package mailengine

import (
	"database/sql"
	"errors"
	"fmt"
	"os"

	"mailcare/app/models"
)

// carryover holds what Reindex keeps from the previous index: the IMAP
// identity and fetch facts of every message (by message key, since the raw
// file does not record them), and the group states (with their reason,
// note, flags and history) and agent reports, which are restored for the
// group keys that come back (group keys are deterministic, so a rebuilt
// group is the same problem). Of a carried group only the state columns are
// used.
type carryover struct {
	sources        map[string]models.MessageSource
	groups         map[string]*models.BounceGroup
	reports        []*models.AgentReport      // in old id order
	patterns       map[int64][]string         // report id -> pattern keys the report covered
	changes        []*models.GroupStateChange // in old id order
	changePatterns map[int64][]string         // state change id -> re-check keys of the group at the change
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

	c := &carryover{groups: map[string]*models.BounceGroup{}}
	c.sources, err = models.ListMessageSources(db)
	if err != nil {
		return nil, err
	}
	groups, err := models.ListGroups(db, models.GroupFilter{})
	if err != nil {
		return nil, err
	}
	for _, g := range groups {
		c.groups[g.GroupKey] = g
	}
	c.reports, err = models.ListAllAgentReports(db)
	if err != nil {
		return nil, err
	}
	c.patterns, err = models.ListAllAgentReportPatterns(db)
	if err != nil {
		return nil, err
	}
	c.changes, err = models.ListAllGroupStateChanges(db)
	if err != nil {
		return nil, err
	}
	c.changePatterns, err = models.ListAllGroupStateChangePatterns(db)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// apply restores the carried state columns, state history, reports and
// report patterns into the rebuilt index for the group keys that exist
// there. The flags are carried over as they were, except that an open
// actionable group (or a resolved / ignored one already sent back for a
// re-check) with a member pattern (bounce or DMARC record) its latest
// completed report did not cover is flagged for analysis again, and a
// resolved or ignored group whose members send it back
// (recheckBasis.sendsBack, with the re-check days) is sent back and flagged
// for analysis when it is actionable. A recipient-side group is never
// flagged for analysis (RestoreGroupState). It returns how many groups and
// reports were restored.
func (c *carryover) apply(db *sql.DB, recheck RecheckDays) (groups, reports int, err error) {
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
	for _, ch := range c.changes {
		if restored[ch.GroupKey] == nil {
			continue
		}
		if err := models.RestoreGroupStateChange(db, ch); err != nil {
			return groups, reports, fmt.Errorf("restore state change of %s: %w", ch.GroupKey, err)
		}
		if err := models.InsertGroupStateChangePatterns(db, ch.ID, c.changePatterns[ch.ID]); err != nil {
			return groups, reports, fmt.Errorf("restore state change patterns of %s: %w", ch.GroupKey, err)
		}
	}
	if err := fillStateChangePatterns(db); err != nil {
		return groups, reports, fmt.Errorf("record state change patterns: %w", err)
	}
	for key, g := range restored {
		state := *c.groups[key]
		state.GroupKey, state.Actionable, state.Category = key, g.Actionable, g.Category
		if state.State != groupStateOpen && !state.NeedsRecheck {
			back, err := groupSentBack(db, &state, recheck)
			if err != nil {
				return groups, reports, fmt.Errorf("check the re-check of %s: %w", key, err)
			}
			if back {
				state.NeedsRecheck, state.NeedsAnalysis = true, true
			}
		} else {
			uncovered, err := models.GroupHasUncoveredPattern(db, key)
			if err != nil {
				return groups, reports, fmt.Errorf("check patterns of %s: %w", key, err)
			}
			state.NeedsAnalysis = state.NeedsAnalysis || uncovered
		}
		if err := models.RestoreGroupState(db, &state); err != nil {
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
