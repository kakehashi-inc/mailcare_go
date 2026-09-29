package models

import (
	"database/sql"
	"errors"
	"time"
)

// GroupStateChange is a row of group_state_changes: one state change of a
// group, kept as its history. Reason is the code the user chose (a
// ResolveAction* for resolved, an IgnoreReason* for ignored, NULL for open
// and for the rows the migration created from the state a group already
// had); Note is the free text the user wrote (NULL when none). ChangedBy is
// the username of the user who made the change ("" for the CLI and the
// migration). The member patterns of the group at the change, against which
// later notices are compared, are kept in group_state_change_patterns.
type GroupStateChange struct {
	ID        int64
	GroupKey  string
	State     string
	Reason    sql.NullString
	Note      sql.NullString
	ChangedBy string
	ChangedAt time.Time
}

// What was done for a resolved group (group_state_changes.reason,
// groups.state_reason). The Web UI words them in the language files
// (value.resolveAction.<code>); the agent gets an English description
// (package agent).
const (
	ResolveActionDelisting      = "delisting"       // asked the blacklist to delist the sending IP
	ResolveActionDNSFixed       = "dns_fixed"       // corrected SPF, DKIM or DMARC of the sending domain
	ResolveActionServerFixed    = "server_fixed"    // corrected the sending server's configuration
	ResolveActionSenderChanged  = "sender_changed"  // reviewed the sender address
	ResolveActionContentChanged = "content_changed" // reviewed the content or the attachments
	ResolveActionVolumeAdjusted = "volume_adjusted" // lowered the sending volume or rate
	ResolveActionRecipientFixed = "recipient_fixed" // corrected or removed the recipient address
	ResolveActionRecipientAsked = "recipient_asked" // contacted the recipient side's administrator
	ResolveActionOther          = "other"           // described in the note
)

// ResolveActions lists the codes of a resolved group in the order the Web UI
// offers them ("other" last).
var ResolveActions = []string{
	ResolveActionDelisting, ResolveActionDNSFixed, ResolveActionServerFixed, ResolveActionSenderChanged,
	ResolveActionContentChanged, ResolveActionVolumeAdjusted, ResolveActionRecipientFixed, ResolveActionRecipientAsked,
	ResolveActionOther,
}

// Why a group was ignored (group_state_changes.reason, groups.state_reason).
// Worded like ResolveAction*.
const (
	IgnoreReasonTemporary       = "temporary"        // a temporary problem that has already cleared
	IgnoreReasonRecipientSide   = "recipient_side"   // a problem on the recipient side
	IgnoreReasonInputError      = "input_error"      // an address a user of the sending system entered wrongly
	IgnoreReasonStoppedSending  = "stopped_sending"  // mail to the address is no longer sent
	IgnoreReasonSpoofing        = "spoofing"         // a third party using the monitored domain
	IgnoreReasonExternalService = "external_service" // a problem of an external service used for sending
	IgnoreReasonFalsePositive   = "false_positive"   // detected by mistake
	IgnoreReasonLowImpact       = "low_impact"       // the impact is small
	IgnoreReasonTestMail        = "test_mail"        // mail sent for a test or a check
	IgnoreReasonOther           = "other"            // written in the note
)

// IgnoreReasons lists the codes of an ignored group in the order the Web UI
// offers them ("other" last).
var IgnoreReasons = []string{
	IgnoreReasonTemporary, IgnoreReasonRecipientSide, IgnoreReasonInputError, IgnoreReasonStoppedSending,
	IgnoreReasonSpoofing, IgnoreReasonExternalService, IgnoreReasonFalsePositive, IgnoreReasonLowImpact,
	IgnoreReasonTestMail, IgnoreReasonOther,
}

const groupStateChangeColumns = `id, group_key, state, reason, note, changed_by, changed_at`

// InsertGroupStateChange appends a state change to the history of its group
// and sets c.ID.
func InsertGroupStateChange(db Execer, c *GroupStateChange) error {
	res, err := db.Exec(`INSERT INTO group_state_changes (group_key, state, reason, note, changed_by, changed_at)
		VALUES (?, ?, ?, ?, ?, ?)`, c.GroupKey, c.State, c.Reason, c.Note, c.ChangedBy, c.ChangedAt.UTC())
	if err != nil {
		return err
	}
	c.ID, err = res.LastInsertId()
	return err
}

// RestoreGroupStateChange inserts a state change with its original id (used
// when an index is rebuilt, so that its recorded patterns stay attached).
func RestoreGroupStateChange(db Execer, c *GroupStateChange) error {
	if c.ID <= 0 {
		return errors.New("restore group state change: missing id")
	}
	_, err := db.Exec(`INSERT INTO group_state_changes (`+groupStateChangeColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.GroupKey, c.State, c.Reason, c.Note, c.ChangedBy, c.ChangedAt.UTC())
	return err
}

// ListGroupStateChanges returns the history of a group, newest first.
func ListGroupStateChanges(db Execer, groupKey string) ([]*GroupStateChange, error) {
	return queryGroupStateChanges(db, `SELECT `+groupStateChangeColumns+` FROM group_state_changes
		WHERE group_key = ? ORDER BY id DESC`, groupKey)
}

// ListAllGroupStateChanges returns every state change of the index in id
// order (used to carry the history over when the index is rebuilt).
func ListAllGroupStateChanges(db Execer) ([]*GroupStateChange, error) {
	return queryGroupStateChanges(db, `SELECT `+groupStateChangeColumns+` FROM group_state_changes ORDER BY id`)
}

// LatestGroupStateChange returns the newest state change of a group
// (sql.ErrNoRows when it has none).
func LatestGroupStateChange(db Execer, groupKey string) (*GroupStateChange, error) {
	changes, err := queryGroupStateChanges(db, `SELECT `+groupStateChangeColumns+` FROM group_state_changes
		WHERE group_key = ? ORDER BY id DESC LIMIT 1`, groupKey)
	if err != nil {
		return nil, err
	}
	if len(changes) == 0 {
		return nil, sql.ErrNoRows
	}
	return changes[0], nil
}

func queryGroupStateChanges(db Execer, query string, args ...any) ([]*GroupStateChange, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*GroupStateChange
	for rows.Next() {
		c := &GroupStateChange{}
		if err := rows.Scan(&c.ID, &c.GroupKey, &c.State, &c.Reason, &c.Note, &c.ChangedBy, &c.ChangedAt); err != nil {
			return nil, err
		}
		c.ChangedAt = c.ChangedAt.UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}
