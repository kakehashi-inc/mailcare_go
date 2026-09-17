package models

import (
	"database/sql"
)

// Bounce is a row of the per-mailbox bounces table: the details extracted from
// one bounce message (delivery-status part and/or body text). It shares its id
// with the messages row it belongs to (1:1; deleted with it) and names the
// group the bounce was bundled into (GroupKey, "" while ungrouped). Only
// messages detected as failed / delayed notices have a row; auto replies,
// daemon mail without failure evidence (kind "other", success DSNs included)
// and ordinary mail do not.
type Bounce struct {
	ID                 int64        `json:"-"` // = messages.id
	GroupKey           string       `json:"group_key"`
	Recipient          string       `json:"recipient"` // the failed recipient address
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
}

const bounceColumns = `id, group_key, recipient, recipient_domain, action, status_code, smtp_code, diagnostic,
	diagnostic_template, remote_mta, remote_ip, reporting_mta, original_message_id, original_subject, original_from,
	original_date`

// UpsertBounce stores (or replaces) the bounce details of message b.ID.
func UpsertBounce(db Execer, b *Bounce) error {
	b.OriginalDate = utcNullTime(b.OriginalDate)
	_, err := db.Exec(
		`INSERT INTO bounces (`+bounceColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET group_key = excluded.group_key, recipient = excluded.recipient,
		   recipient_domain = excluded.recipient_domain, action = excluded.action, status_code = excluded.status_code,
		   smtp_code = excluded.smtp_code, diagnostic = excluded.diagnostic,
		   diagnostic_template = excluded.diagnostic_template, remote_mta = excluded.remote_mta,
		   remote_ip = excluded.remote_ip, reporting_mta = excluded.reporting_mta,
		   original_message_id = excluded.original_message_id, original_subject = excluded.original_subject,
		   original_from = excluded.original_from, original_date = excluded.original_date`,
		b.ID, b.GroupKey, b.Recipient, b.RecipientDomain, b.Action, b.StatusCode, b.SMTPCode, b.Diagnostic,
		b.DiagnosticTemplate, b.RemoteMTA, b.RemoteIP, b.ReportingMTA, b.OriginalMessageID, b.OriginalSubject,
		b.OriginalFrom, b.OriginalDate,
	)
	return err
}

// DeleteBounce removes the bounce details of a message, if any (a message
// that turned out not to be a grouped bounce keeps no row).
func DeleteBounce(db Execer, messageID int64) error {
	_, err := db.Exec(`DELETE FROM bounces WHERE id = ?`, messageID)
	return err
}

// GetBounceByMessageID returns the bounce details of a message (sql.ErrNoRows
// when the message has none).
func GetBounceByMessageID(db *sql.DB, messageID int64) (*Bounce, error) {
	b := &Bounce{}
	if err := db.QueryRow(`SELECT `+bounceColumns+` FROM bounces WHERE id = ?`, messageID).Scan(
		&b.ID, &b.GroupKey, &b.Recipient, &b.RecipientDomain, &b.Action, &b.StatusCode, &b.SMTPCode, &b.Diagnostic,
		&b.DiagnosticTemplate, &b.RemoteMTA, &b.RemoteIP, &b.ReportingMTA, &b.OriginalMessageID, &b.OriginalSubject,
		&b.OriginalFrom, &b.OriginalDate); err != nil {
		return nil, err
	}
	return b, nil
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
		column string
		dst    *[]string
	}{
		{"recipient", &st.Recipients},
		{"remote_ip", &st.RemoteIPs},
		{"remote_mta", &st.RemoteMTAs},
	} {
		rows, err := db.Query(`SELECT DISTINCT `+q.column+` FROM bounces WHERE group_key = ? AND `+q.column+` <> ''
			ORDER BY `+q.column, groupKey)
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
