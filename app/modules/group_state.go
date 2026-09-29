package modules

import (
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"mailcare/app/models"
	"mailcare/app/modules/mailengine"
	"mailcare/app/modules/wording"
)

// MaxGroupStateNoteLength is the maximum length in characters of the note
// written with a state change.
const MaxGroupStateNoteLength = 2000

var (
	errGroupStateInvalid    = wording.New("system.invalidRequest", "state must be open, resolved or ignored")
	errResolveActionMissing = wording.New("validation.group.resolveActionRequired",
		"reason is required to mark a group resolved: "+strings.Join(models.ResolveActions, ", "))
	errIgnoreReasonMissing = wording.New("validation.group.ignoreReasonRequired",
		"reason is required to ignore a group: "+strings.Join(models.IgnoreReasons, ", "))
	errResolveActionInvalid = wording.New("system.invalidRequest",
		"reason of a resolved group must be one of "+strings.Join(models.ResolveActions, ", "))
	errIgnoreReasonInvalid = wording.New("system.invalidRequest",
		"reason of an ignored group must be one of "+strings.Join(models.IgnoreReasons, ", "))
	errGroupStateNoteTooLong = wording.New("validation.group.stateNoteTooLong",
		fmt.Sprintf("note must be %d characters or fewer", MaxGroupStateNoteLength)).With("max", MaxGroupStateNoteLength)
)

// GroupStateChange is a validated state change of a group: the new state
// with the reason code and the note stored with it (NULL when none).
type GroupStateChange struct {
	State  string
	Reason sql.NullString
	Note   sql.NullString
}

// GroupStateInput validates a state change as the Web UI and the CLI give it
// and returns what to store:
//
//   - open: no reason and no note (whatever was given is dropped);
//   - resolved: a reason from models.ResolveActions is required; the note
//     (details of what was done) is optional with every reason;
//   - ignored: a reason from models.IgnoreReasons is required; the note is
//     kept only with the reason "other", where it is optional.
//
// A blank note is stored as NULL.
func GroupStateInput(state, reason, note string) (GroupStateChange, error) {
	state, reason, note = strings.TrimSpace(state), strings.TrimSpace(reason), strings.TrimSpace(note)
	change := GroupStateChange{State: state}
	switch state {
	case GroupStateOpen:
		return change, nil
	case GroupStateResolved:
		if reason == "" {
			return change, errResolveActionMissing
		}
		if !slices.Contains(models.ResolveActions, reason) {
			return change, errResolveActionInvalid
		}
	case GroupStateIgnored:
		if reason == "" {
			return change, errIgnoreReasonMissing
		}
		if !slices.Contains(models.IgnoreReasons, reason) {
			return change, errIgnoreReasonInvalid
		}
		if reason != models.IgnoreReasonOther {
			note = ""
		}
	default:
		return change, errGroupStateInvalid
	}
	change.Reason = sql.NullString{String: reason, Valid: true}
	if utf8.RuneCountInString(note) > MaxGroupStateNoteLength {
		return change, errGroupStateNoteTooLong
	}
	if note != "" {
		change.Note = sql.NullString{String: note, Valid: true}
	}
	return change, nil
}

// ChangeGroupState applies a validated state change to a group of the index
// in one transaction: the state with its reason and note (the re-check flag
// is cleared; an open actionable group is flagged for analysis when its
// latest report does not cover every pattern, a resolved or ignored one is
// not), a history row naming the user (changedBy, "" for the CLI) and the
// re-check keys of the current members, against which later notices are
// compared. Any state can be set again: setting resolved or ignored on a
// group sent back for a re-check records the new decision.
func ChangeGroupState(idx *sql.DB, groupKey string, change GroupStateChange, changedBy string) error {
	tx, err := idx.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	uncovered, err := models.GroupHasUncoveredPattern(tx, groupKey)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if err := models.SetGroupState(tx, groupKey, change.State, change.Reason, change.Note, now, uncovered); err != nil {
		return err
	}
	c := &models.GroupStateChange{GroupKey: groupKey, State: change.State, Reason: change.Reason, Note: change.Note,
		ChangedBy: changedBy, ChangedAt: now}
	if err := models.InsertGroupStateChange(tx, c); err != nil {
		return err
	}
	if err := mailengine.RecordStateChangePatterns(tx, c.ID, groupKey); err != nil {
		return err
	}
	return tx.Commit()
}
