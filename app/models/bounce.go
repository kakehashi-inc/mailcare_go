package models

import (
	"database/sql"
	"time"
)

// Bounce is a row of the per-mailbox bounces table: the details extracted from
// one bounce message (delivery-status part and/or body text). It is keyed by
// the message id and removed with the message.
type Bounce struct {
	MessageID          int64        `json:"-"`
	OriginalRecipient  string       `json:"original_recipient"`
	RecipientDomain    string       `json:"recipient_domain"`
	Action             string       `json:"action"` // failed | delayed | delivered | relayed | expanded | ""
	StatusCode         string       `json:"status_code"`
	SMTPCode           string       `json:"smtp_code"`
	Diagnostic         string       `json:"diagnostic"`
	DiagnosticTemplate string       `json:"diagnostic_template"`
	RemoteMTA          string       `json:"remote_mta"`
	RemoteIP           string       `json:"remote_ip"`
	ReportingMTA       string       `json:"reporting_mta"`
	OriginalMessageID  string       `json:"original_message_id"`
	OriginalSubject    string       `json:"original_subject"`
	OriginalFrom       string       `json:"original_from"`
	OriginalDate       sql.NullTime `json:"-"`
	Responsible        string       `json:"responsible"`
}

const bounceColumns = `message_id, original_recipient, recipient_domain, action, status_code, smtp_code, diagnostic,
	diagnostic_template, remote_mta, remote_ip, reporting_mta, original_message_id, original_subject, original_from,
	original_date, responsible`

// UpsertBounce stores (or replaces) the bounce details of a message.
func UpsertBounce(db *sql.DB, b *Bounce) error {
	b.OriginalDate = utcNullTime(b.OriginalDate)
	_, err := db.Exec(
		`INSERT OR REPLACE INTO bounces (`+bounceColumns+`)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		b.MessageID, b.OriginalRecipient, b.RecipientDomain, b.Action, b.StatusCode, b.SMTPCode, b.Diagnostic,
		b.DiagnosticTemplate, b.RemoteMTA, b.RemoteIP, b.ReportingMTA, b.OriginalMessageID, b.OriginalSubject,
		b.OriginalFrom, b.OriginalDate, b.Responsible,
	)
	return err
}

// GetBounceByMessageID returns the bounce details of a message (sql.ErrNoRows when absent).
func GetBounceByMessageID(db *sql.DB, messageID int64) (*Bounce, error) {
	return scanBounce(db.QueryRow(`SELECT `+bounceColumns+` FROM bounces WHERE message_id = ?`, messageID))
}

// GroupBounceStats summarizes the bounces of a group for display.
type GroupBounceStats struct {
	Recipients []string `json:"recipients"`
	RemoteIPs  []string `json:"remote_ips"`
	RemoteMTAs []string `json:"remote_mtas"`
}

// GroupStats returns the distinct recipients, remote IPs and remote MTAs of a group.
func GroupStats(db *sql.DB, groupKey string) (*GroupBounceStats, error) {
	st := &GroupBounceStats{Recipients: []string{}, RemoteIPs: []string{}, RemoteMTAs: []string{}}
	for _, q := range []struct {
		col string
		dst *[]string
	}{
		{"original_recipient", &st.Recipients},
		{"remote_ip", &st.RemoteIPs},
		{"remote_mta", &st.RemoteMTAs},
	} {
		rows, err := db.Query(`SELECT DISTINCT b.`+q.col+` FROM bounces b JOIN messages m ON m.id = b.message_id
			WHERE m.group_key = ? AND b.`+q.col+` <> '' ORDER BY b.`+q.col, groupKey)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				rows.Close()
				return nil, err
			}
			*q.dst = append(*q.dst, v)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return st, nil
}

func scanBounce(s rowScanner) (*Bounce, error) {
	b := &Bounce{}
	if err := s.Scan(&b.MessageID, &b.OriginalRecipient, &b.RecipientDomain, &b.Action, &b.StatusCode, &b.SMTPCode,
		&b.Diagnostic, &b.DiagnosticTemplate, &b.RemoteMTA, &b.RemoteIP, &b.ReportingMTA, &b.OriginalMessageID,
		&b.OriginalSubject, &b.OriginalFrom, &b.OriginalDate, &b.Responsible); err != nil {
		return nil, err
	}
	return b, nil
}

// nullTime converts a time to sql.NullTime (zero -> NULL).
func nullTime(t time.Time) sql.NullTime {
	if t.IsZero() {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: t.UTC(), Valid: true}
}

// NullTime is the exported form of nullTime for callers in other packages.
func NullTime(t time.Time) sql.NullTime { return nullTime(t) }

// utcNullTime normalizes a valid NullTime to UTC (and drops the monotonic
// clock reading) so the driver stores a canonical, sortable text value.
func utcNullTime(t sql.NullTime) sql.NullTime {
	if !t.Valid {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: t.Time.UTC().Round(0), Valid: true}
}
