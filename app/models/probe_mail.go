package models

import (
	"database/sql"
	"strings"
	"time"
)

// ProbeMail is a row of probe_mails: one probe mail sent from a monitored
// mailbox to the probe server (app/modules/probe.go). Every mail keeps its
// own send time, so its delay is measured on its own; Level keeps the worst
// level it reached, so a mail that arrived late or was resolved by hand
// still shows that it was delayed or failed.
type ProbeMail struct {
	ID            int64
	MailboxID     int64
	Address       string
	Token         string
	Slot          string // "YYYY-MM-DD HH:MM" of the send time, server local time (as in the subject)
	Manual        bool   // sent by hand, not at a send time
	Subject       string
	SentAt        time.Time
	Status        string // ProbeStatus*
	Level         string // ProbeLevel*
	DelaySeconds  int64
	ArrivedAt     sql.NullTime
	LastCheckedAt sql.NullTime
	ErrorMessage  string
	NotifiedAt    sql.NullTime
	ResolvedAt    sql.NullTime
	ResolvedBy    string
	ResolveReason string
	ResolveNote   string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Statuses of a probe mail (probe_mails.status).
const (
	ProbeStatusPending    = "pending"     // sent, not arrived, not overdue yet
	ProbeStatusDelayed    = "delayed"     // not arrived for the delay threshold
	ProbeStatusFailure    = "failure"     // not arrived for the failure threshold
	ProbeStatusSendFailed = "send_failed" // the SMTP server refused the mail or could not be reached
	ProbeStatusArrived    = "arrived"     // arrived within the delay threshold
	ProbeStatusRecovered  = "recovered"   // arrived after the delay threshold
	ProbeStatusResolved   = "resolved"    // closed by hand before it arrived
)

// Levels a probe mail reached (probe_mails.level).
const (
	ProbeLevelNone    = ""
	ProbeLevelDelayed = "delayed"
	ProbeLevelFailure = "failure"
)

// Why a probe mail was resolved by hand (probe_mails.resolve_reason). The
// Web UI words them in the language files (value.probeResolveReason.<code>).
const (
	ProbeReasonServerRecovered  = "server_recovered"   // the mail server was confirmed to work again
	ProbeReasonArrivedElsewhere = "arrived_elsewhere"  // the mail arrived but was moved or filtered on the probe server
	ProbeReasonMaintenance      = "maintenance"        // planned maintenance of the mail server
	ProbeReasonConfigError      = "config_error"       // a mistake in the probe settings, corrected since
	ProbeReasonProbeServer      = "probe_server_issue" // a problem of the probe server itself
	ProbeReasonOther            = "other"              // described in the note
)

// ProbeResolveReasons lists the reasons in the order the Web UI offers them
// ("other" last).
var ProbeResolveReasons = []string{
	ProbeReasonServerRecovered, ProbeReasonArrivedElsewhere, ProbeReasonMaintenance, ProbeReasonConfigError,
	ProbeReasonProbeServer, ProbeReasonOther,
}

// ProbeStatusOpen reports whether a probe mail still waits for its arrival
// or for a decision (the statuses a user can resolve).
func ProbeStatusOpen(status string) bool {
	switch status {
	case ProbeStatusPending, ProbeStatusDelayed, ProbeStatusFailure, ProbeStatusSendFailed:
		return true
	}
	return false
}

// Filters of ListProbeMails.
const (
	ProbeFilterOpen   = "open"   // pending, delayed, failure, send_failed
	ProbeFilterIssues = "issues" // every mail that was delayed, failed or could not be sent, open or closed
	ProbeFilterAll    = "all"
)

const probeMailColumns = `id, mailbox_id, address, token, slot, manual, subject, sent_at, status, level, delay_seconds,
	arrived_at, last_checked_at, error_message, notified_at, resolved_at, resolved_by, resolve_reason, resolve_note,
	created_at, updated_at`

// probeWaitingStatuses are the statuses the arrival check looks for.
const probeWaitingStatuses = `('pending', 'delayed', 'failure')`

// probeOpenStatuses are the statuses ProbeStatusOpen accepts.
const probeOpenStatuses = `('pending', 'delayed', 'failure', 'send_failed')`

// InsertProbeMail stores a probe mail and fills in its ID.
func InsertProbeMail(db *sql.DB, p *ProbeMail) error {
	now := time.Now().UTC()
	res, err := db.Exec(`INSERT INTO probe_mails (mailbox_id, address, token, slot, manual, subject, sent_at, status, level,
		delay_seconds, error_message, resolved_by, resolve_reason, resolve_note, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', '', '', ?, ?)`,
		p.MailboxID, p.Address, p.Token, p.Slot, boolToInt(p.Manual), p.Subject, p.SentAt.UTC(), p.Status, p.Level,
		p.DelaySeconds, p.ErrorMessage, now, now)
	if err != nil {
		return err
	}
	p.ID, _ = res.LastInsertId()
	p.CreatedAt, p.UpdatedAt = now, now
	return nil
}

// UpdateProbeMailCheck records what an arrival check found for a mail: the
// status, the level, the delay, the arrival time and the check time. db may
// be a transaction.
func UpdateProbeMailCheck(db Execer, p *ProbeMail) error {
	now := time.Now().UTC()
	_, err := db.Exec(`UPDATE probe_mails SET status = ?, level = ?, delay_seconds = ?, arrived_at = ?,
		last_checked_at = ?, updated_at = ? WHERE id = ?`,
		p.Status, p.Level, p.DelaySeconds, utcNullTime(p.ArrivedAt), utcNullTime(p.LastCheckedAt), now, p.ID)
	if err == nil {
		p.UpdatedAt = now
	}
	return err
}

// ResolveProbeMail closes an open probe mail by hand. It reports false when
// the mail is not open (any more): it arrived or was resolved meanwhile.
func ResolveProbeMail(db *sql.DB, id int64, by, reason, note string, at time.Time) (bool, error) {
	res, err := db.Exec(`UPDATE probe_mails SET status = ?, resolved_at = ?, resolved_by = ?, resolve_reason = ?,
		resolve_note = ?, updated_at = ? WHERE id = ? AND status IN `+probeOpenStatuses,
		ProbeStatusResolved, at.UTC(), by, reason, note, time.Now().UTC(), id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// MarkProbeMailsNotified records that the failures of the given mails were
// mailed.
func MarkProbeMailsNotified(db *sql.DB, ids []int64, at time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	args := make([]any, 0, len(ids)+1)
	args = append(args, at.UTC())
	for _, id := range ids {
		args = append(args, id)
	}
	_, err := db.Exec(`UPDATE probe_mails SET notified_at = ? WHERE id IN (`+placeholders(len(ids))+`)`, args...)
	return err
}

// GetProbeMail returns one probe mail (sql.ErrNoRows when absent).
func GetProbeMail(db *sql.DB, id int64) (*ProbeMail, error) {
	return scanProbeMail(db.QueryRow(`SELECT `+probeMailColumns+` FROM probe_mails WHERE id = ?`, id))
}

// ListWaitingProbeMails returns the mails the arrival check looks for (not
// arrived, not resolved, sent successfully), oldest first.
func ListWaitingProbeMails(db *sql.DB) ([]*ProbeMail, error) {
	return queryProbeMails(db, `SELECT `+probeMailColumns+` FROM probe_mails
		WHERE status IN `+probeWaitingStatuses+` ORDER BY sent_at ASC, id ASC`)
}

// ListUnnotifiedProbeFailures returns the open failures (not arrived for the
// failure threshold, or not sent) whose failure was not mailed yet.
func ListUnnotifiedProbeFailures(db *sql.DB) ([]*ProbeMail, error) {
	return queryProbeMails(db, `SELECT `+probeMailColumns+` FROM probe_mails
		WHERE status IN ('failure', 'send_failed') AND notified_at IS NULL ORDER BY address ASC, sent_at ASC`)
}

// ListOpenProbeFailures returns every open failure (not arrived for the
// failure threshold, or not sent), notified or not.
func ListOpenProbeFailures(db *sql.DB) ([]*ProbeMail, error) {
	return queryProbeMails(db, `SELECT `+probeMailColumns+` FROM probe_mails
		WHERE status IN ('failure', 'send_failed') ORDER BY address ASC, sent_at ASC`)
}

// probeFilterCondition returns the WHERE clause of a filter ("" for all).
func probeFilterCondition(filter string) string {
	switch filter {
	case ProbeFilterOpen:
		return ` WHERE status IN ` + probeOpenStatuses
	case ProbeFilterIssues:
		return ` WHERE level <> '' OR status = 'send_failed' OR resolve_reason <> ''`
	}
	return ""
}

// ListProbeMails returns one page of the probe mails of a filter (newest
// first) and the number of mails of the filter. mailboxID 0 lists every
// mailbox.
func ListProbeMails(db *sql.DB, filter string, mailboxID int64, offset, limit int) ([]*ProbeMail, int, error) {
	where := probeFilterCondition(filter)
	var args []any
	if mailboxID != 0 {
		if where == "" {
			where = ` WHERE mailbox_id = ?`
		} else {
			where = ` WHERE (` + strings.TrimPrefix(where, ` WHERE `) + `) AND mailbox_id = ?`
		}
		args = append(args, mailboxID)
	}
	var total int
	if err := db.QueryRow(`SELECT COUNT(*) FROM probe_mails`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := queryProbeMails(db, `SELECT `+probeMailColumns+` FROM probe_mails`+where+
		` ORDER BY sent_at DESC, id DESC LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	return rows, total, err
}

// ProbeCounts counts the probe mails per status, for the dashboard and the
// Alerts screen. Recovered counts the mails that arrived late, Resolved the
// ones closed by hand after they were delayed or failed (the ones closed
// without a level are not counted).
type ProbeCounts struct {
	Pending    int `json:"pending"`
	Delayed    int `json:"delayed"`
	Failure    int `json:"failure"`
	SendFailed int `json:"send_failed"`
	Recovered  int `json:"recovered"`
	Resolved   int `json:"resolved"`
}

// CountProbeMails counts the probe mails per status (mailboxID 0: every
// mailbox). Recovered and resolved mails sent before since are not counted,
// so the counts show the recent history only.
func CountProbeMails(db *sql.DB, mailboxID int64, since time.Time) (ProbeCounts, error) {
	var c ProbeCounts
	q := `SELECT status, COUNT(*) FROM probe_mails WHERE (status IN ` + probeOpenStatuses + `
		OR (status IN ('recovered', 'resolved') AND sent_at >= ?))`
	args := []any{since.UTC()}
	if mailboxID != 0 {
		q += ` AND mailbox_id = ?`
		args = append(args, mailboxID)
	}
	rows, err := db.Query(q+` GROUP BY status`, args...)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return c, err
		}
		switch status {
		case ProbeStatusPending:
			c.Pending = n
		case ProbeStatusDelayed:
			c.Delayed = n
		case ProbeStatusFailure:
			c.Failure = n
		case ProbeStatusSendFailed:
			c.SendFailed = n
		case ProbeStatusRecovered:
			c.Recovered = n
		case ProbeStatusResolved:
			c.Resolved = n
		}
	}
	return c, rows.Err()
}

// LatestProbeMails returns the newest probe mail of every mailbox that sent
// one, keyed by mailbox id.
func LatestProbeMails(db *sql.DB) (map[int64]*ProbeMail, error) {
	list, err := queryProbeMails(db, `SELECT `+probeMailColumns+` FROM probe_mails p
		WHERE id = (SELECT id FROM probe_mails q WHERE q.mailbox_id = p.mailbox_id ORDER BY sent_at DESC, id DESC LIMIT 1)`)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]*ProbeMail, len(list))
	for _, p := range list {
		out[p.MailboxID] = p
	}
	return out, nil
}

// DeleteOldProbeMails removes the closed probe mails (arrived, recovered,
// resolved) sent before the given time.
func DeleteOldProbeMails(db *sql.DB, before time.Time) (int64, error) {
	res, err := db.Exec(`DELETE FROM probe_mails WHERE status IN ('arrived', 'recovered', 'resolved') AND sent_at < ?`,
		before.UTC())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func queryProbeMails(db *sql.DB, query string, args ...any) ([]*ProbeMail, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ProbeMail
	for rows.Next() {
		p, err := scanProbeMail(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func scanProbeMail(s rowScanner) (*ProbeMail, error) {
	p := &ProbeMail{}
	var manual int
	if err := s.Scan(&p.ID, &p.MailboxID, &p.Address, &p.Token, &p.Slot, &manual, &p.Subject, &p.SentAt, &p.Status, &p.Level,
		&p.DelaySeconds, &p.ArrivedAt, &p.LastCheckedAt, &p.ErrorMessage, &p.NotifiedAt, &p.ResolvedAt, &p.ResolvedBy,
		&p.ResolveReason, &p.ResolveNote, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	p.Manual = manual != 0
	return p, nil
}

// placeholders returns n comma-separated "?".
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
