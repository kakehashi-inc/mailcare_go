package mailengine

import (
	"database/sql"
	"errors"
	"time"

	"mailcare/app/models"
)

// Re-check of resolved and ignored groups.
//
// A resolved or ignored group is sent back to the users for a new decision
// (groups.needs_recheck, the "(re)" tabs of Alerts) when a notice filed into
// it shows that the matter is not settled:
//
//   - a new pattern: a member whose re-check key (RecheckKey) was not among
//     the keys of the group at its latest state change. Only the categories
//     whose group can hold different causes are compared this way
//     (recheckByPattern); in the others the unit and the authority already
//     name the problem, and another wording only means another receiving
//     server reports it.
//   - for a resolved group only: a member about mail sent after the time the
//     action needs to take effect, the category's re-check days counted
//     from the state change (RecheckDays; the send date is the date of the
//     returned message, or of the DMARC report period, else of the notice).
//     An ignored group keeps receiving the notices it was ignored for.
//
// The decision is taken when the notice is filed (the grouping phase, with
// the analysis flag) and when an index is rebuilt (carryover). Sending a
// group back also flags it for analysis when it is actionable.

// RecheckDays maps a category to its re-check days (the settings); a
// category missing from it uses DefaultRecheckDays.
type RecheckDays map[string]int

// Bounds of the re-check days of a category.
const (
	MinRecheckDays = 1
	MaxRecheckDays = 365
)

// defaultRecheckDays is how long, after a group was marked resolved, notices
// about mail sent in the meantime are expected (the action takes effect):
// 14 days where a third party must act (a blacklist, the recipient side's
// administrator), 7 days where the receiving side needs time (reputation,
// daily reports, recipient lists), 3 days where a change on the sending
// side takes effect once DNS caught up.
var defaultRecheckDays = map[string]int{
	categoryIPBlocked:             14,
	categorySenderBlocked:         14,
	categoryRateLimited:           7,
	categoryUnknownFailure:        7,
	categoryDMARCSPFMissing:       7,
	categoryDMARCDKIMFailed:       7,
	categoryDMARCNotAuthenticated: 7,
	categoryUserUnknown:           7,
	categoryMailboxFull:           7,
	categoryMailboxDisabled:       7,
	categoryDomainNotFound:        7,
	categoryDeliveryDelay:         7,
	categoryAuthFailure:           3,
	categoryContentRejected:       3,
	categoryMessageTooLarge:       3,
	categoryServerConfig:          3,
}

// DefaultRecheckDays returns the default re-check days of a category (7 for
// an unknown one).
func DefaultRecheckDays(category string) int {
	if d, ok := defaultRecheckDays[category]; ok {
		return d
	}
	return 7
}

// For returns the re-check days of a category.
func (r RecheckDays) For(category string) int {
	if d, ok := r[category]; ok && d >= MinRecheckDays {
		return d
	}
	return DefaultRecheckDays(category)
}

// recheckByPattern names the categories whose groups are sent back by a new
// pattern: a group of them can hold different causes (SPF or DKIM, TLS or
// HELO, the results of a DMARC record).
var recheckByPattern = map[string]bool{
	categoryAuthFailure:           true,
	categoryServerConfig:          true,
	categoryDMARCSPFMissing:       true,
	categoryDMARCDKIMFailed:       true,
	categoryDMARCNotAuthenticated: true,
}

// RecheckKey is the re-check key of a member: for a bounce its pattern
// without the remote MTA (status code, wording, kind of diagnostic source),
// for a DMARC record its pattern (which names no server).
func RecheckKey(m *models.GroupBounce) string {
	if m.DMARC != nil {
		return m.DMARC.PatternKey
	}
	return PatternKey(m.StatusCode, m.DiagnosticTemplate, "", m.DiagnosticSource)
}

// SentAt is when the mail a member reports on was sent: the date of the
// returned message of a bounce, the end of the report period of a DMARC
// record, else the date of the notice.
func SentAt(m *models.GroupBounce) time.Time {
	if m.DMARC != nil {
		if m.DMARC.EndAt.Valid {
			return m.DMARC.EndAt.Time.UTC()
		}
		return m.Date
	}
	if m.OriginalDate.Valid {
		return m.OriginalDate.Time.UTC()
	}
	return m.Date
}

// GroupRecheckKeys returns the distinct re-check keys of the current members
// of a group.
func GroupRecheckKeys(db models.Execer, groupKey string) ([]string, error) {
	members, err := models.ListGroupBounces(db, groupKey)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var keys []string
	for _, m := range members {
		if k := RecheckKey(m); !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	return keys, nil
}

// RecordStateChangePatterns records the re-check keys of the current members
// of a group as the keys of a state change.
func RecordStateChangePatterns(db models.Execer, changeID int64, groupKey string) error {
	keys, err := GroupRecheckKeys(db, groupKey)
	if err != nil {
		return err
	}
	return models.InsertGroupStateChangePatterns(db, changeID, keys)
}

// fillStateChangePatterns records the re-check keys of the state changes
// that have none: the history rows the 0004 migration created for the
// groups that were already resolved or ignored (their members at that time
// are their members now).
func fillStateChangePatterns(db *sql.DB) error {
	missing, err := models.ListStateChangesWithoutPatterns(db)
	if err != nil {
		return err
	}
	for id, key := range missing {
		if err := RecordStateChangePatterns(db, id, key); err != nil {
			return err
		}
	}
	return nil
}

// recheckBasis is what a member of a resolved or ignored group is compared
// with: the latest state change and its re-check keys.
type recheckBasis struct {
	change *models.GroupStateChange
	keys   map[string]bool
}

// loadRecheckBasis reads the latest state change of a group and its keys
// (nil when the group has no recorded state change).
func loadRecheckBasis(db models.Execer, groupKey string) (*recheckBasis, error) {
	change, err := models.LatestGroupStateChange(db, groupKey)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	keys, err := models.ListGroupStateChangePatterns(db, change.ID)
	if err != nil {
		return nil, err
	}
	return &recheckBasis{change: change, keys: keys}, nil
}

// sendsBack reports whether a member sends a resolved or ignored group back
// for a re-check (see the comment at the top of this file).
func (b *recheckBasis) sendsBack(g *models.BounceGroup, m *models.GroupBounce, days RecheckDays) bool {
	if b == nil || g.State == groupStateOpen {
		return false
	}
	if recheckByPattern[g.Category] && !b.keys[RecheckKey(m)] {
		return true
	}
	if g.State != groupStateResolved {
		return false
	}
	deadline := b.change.ChangedAt.AddDate(0, 0, days.For(g.Category))
	return SentAt(m).After(deadline)
}

// groupSentBack reports whether any current member sends a resolved or
// ignored group back for a re-check (used when an index is rebuilt).
func groupSentBack(db models.Execer, g *models.BounceGroup, days RecheckDays) (bool, error) {
	if g.State == groupStateOpen {
		return false, nil
	}
	basis, err := loadRecheckBasis(db, g.GroupKey)
	if err != nil || basis == nil {
		return false, err
	}
	members, err := models.ListGroupBounces(db, g.GroupKey)
	if err != nil {
		return false, err
	}
	for _, m := range members {
		if basis.sendsBack(g, m, days) {
			return true, nil
		}
	}
	return false, nil
}
