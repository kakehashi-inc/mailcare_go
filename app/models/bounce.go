package models

import (
	"database/sql"
	"sort"
	"strings"
	"time"
)

// Bounce is a row of the per-mailbox bounces table: the details extracted from
// one bounce message (delivery-status part and/or body text). It shares its id
// with the messages row it belongs to (1:1; deleted with it) and names the
// group the bounce was bundled into (GroupKey, "" while ungrouped). Only
// messages detected as failed / delayed notices have a row; auto replies,
// daemon mail without failure evidence (kind "other", success DSNs included)
// and ordinary mail do not.
type Bounce struct {
	ID                 int64  `json:"-"` // = messages.id
	GroupKey           string `json:"group_key"`
	Recipient          string `json:"recipient"` // the failed recipient address
	RecipientDomain    string `json:"recipient_domain"`
	Action             string `json:"action"` // failed | delayed | delivered | relayed | expanded | ""
	StatusCode         string `json:"status_code"`
	SMTPCode           string `json:"smtp_code"`
	Diagnostic         string `json:"diagnostic"`
	DiagnosticTemplate string `json:"diagnostic_template"`
	// DiagnosticSource names where Diagnostic was taken from: "dsn" (the
	// delivery-status part), a body section ("text:2", "html:1", numbered
	// like the section files) or "" when the notice carried no diagnostic.
	DiagnosticSource string `json:"diagnostic_source"`
	// CategoryRule is the name of the category rule that filed the bounce
	// into its group (the reason for its category and actionability).
	CategoryRule string `json:"category_rule"`
	// PatternKey identifies the pattern of the bounce inside its group
	// (status code, diagnostic template, remote MTA, kind of source).
	PatternKey        string       `json:"pattern_key"`
	RemoteMTA         string       `json:"remote_mta"`
	RemoteIP          string       `json:"remote_ip"`
	ReportingMTA      string       `json:"reporting_mta"`
	OriginalMessageID string       `json:"original_message_id"`
	OriginalSubject   string       `json:"original_subject"`
	OriginalFrom      string       `json:"original_from"`
	OriginalDate      sql.NullTime `json:"-"`
}

const bounceColumns = `id, group_key, recipient, recipient_domain, action, status_code, smtp_code, diagnostic,
	diagnostic_template, diagnostic_source, category_rule, pattern_key, remote_mta, remote_ip, reporting_mta,
	original_message_id, original_subject, original_from, original_date`

// UpsertBounce stores (or replaces) the bounce details of message b.ID.
func UpsertBounce(db Execer, b *Bounce) error {
	b.OriginalDate = utcNullTime(b.OriginalDate)
	_, err := db.Exec(
		`INSERT INTO bounces (`+bounceColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET group_key = excluded.group_key, recipient = excluded.recipient,
		   recipient_domain = excluded.recipient_domain, action = excluded.action, status_code = excluded.status_code,
		   smtp_code = excluded.smtp_code, diagnostic = excluded.diagnostic,
		   diagnostic_template = excluded.diagnostic_template, diagnostic_source = excluded.diagnostic_source,
		   category_rule = excluded.category_rule, pattern_key = excluded.pattern_key, remote_mta = excluded.remote_mta,
		   remote_ip = excluded.remote_ip, reporting_mta = excluded.reporting_mta,
		   original_message_id = excluded.original_message_id, original_subject = excluded.original_subject,
		   original_from = excluded.original_from, original_date = excluded.original_date`,
		b.ID, b.GroupKey, b.Recipient, b.RecipientDomain, b.Action, b.StatusCode, b.SMTPCode, b.Diagnostic,
		b.DiagnosticTemplate, b.DiagnosticSource, b.CategoryRule, b.PatternKey, b.RemoteMTA, b.RemoteIP, b.ReportingMTA,
		b.OriginalMessageID, b.OriginalSubject, b.OriginalFrom, b.OriginalDate,
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
	return scanBounce(db.QueryRow(`SELECT `+bounceColumns+` FROM bounces WHERE id = ?`, messageID))
}

func scanBounce(s rowScanner) (*Bounce, error) {
	b := &Bounce{}
	if err := s.Scan(&b.ID, &b.GroupKey, &b.Recipient, &b.RecipientDomain, &b.Action, &b.StatusCode, &b.SMTPCode,
		&b.Diagnostic, &b.DiagnosticTemplate, &b.DiagnosticSource, &b.CategoryRule, &b.PatternKey, &b.RemoteMTA,
		&b.RemoteIP, &b.ReportingMTA, &b.OriginalMessageID, &b.OriginalSubject, &b.OriginalFrom,
		&b.OriginalDate); err != nil {
		return nil, err
	}
	return b, nil
}

// GroupBounce is a member of a group together with the key, the date and
// the body source (messages.body_source: the body the notice was classified
// on) of its message: what the agent needs to pick and read sample notices.
// A member is a bounce, or a failing record of a DMARC aggregate report
// (DMARC set; the Bounce fields then carry its group, pattern, category
// rule, sending IP (RemoteIP), reporter (ReportingMTA) and the diagnostic
// source DiagnosticSourceDMARC).
type GroupBounce struct {
	Bounce
	MessageKey string
	Date       time.Time
	BodySource string
	DMARC      *DMARCRecord
}

// ListGroupBounces returns every member of a group (its bounces and its
// DMARC records) with its message key and date, newest message first.
func ListGroupBounces(db *sql.DB, groupKey string) ([]*GroupBounce, error) {
	out, err := listGroupBouncesOnly(db, groupKey)
	if err != nil {
		return nil, err
	}
	records, err := groupDMARCRecords(db, groupKey)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return out, nil
	}
	out = append(out, records...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Date.After(out[j].Date) })
	return out, nil
}

// listGroupBouncesOnly returns the bounces of a group, newest message first.
func listGroupBouncesOnly(db *sql.DB, groupKey string) ([]*GroupBounce, error) {
	rows, err := db.Query(`SELECT m.message_key, m.date, m.body_source, `+prefixColumns("b.", bounceColumns)+`
		FROM bounces b JOIN messages m ON m.id = b.id WHERE b.group_key = ? ORDER BY m.date DESC, m.id DESC`, groupKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*GroupBounce
	for rows.Next() {
		gb := &GroupBounce{}
		b := &gb.Bounce
		if err := rows.Scan(&gb.MessageKey, &gb.Date, &gb.BodySource, &b.ID, &b.GroupKey, &b.Recipient, &b.RecipientDomain, &b.Action,
			&b.StatusCode, &b.SMTPCode, &b.Diagnostic, &b.DiagnosticTemplate, &b.DiagnosticSource, &b.CategoryRule,
			&b.PatternKey, &b.RemoteMTA, &b.RemoteIP, &b.ReportingMTA, &b.OriginalMessageID, &b.OriginalSubject,
			&b.OriginalFrom, &b.OriginalDate); err != nil {
			return nil, err
		}
		gb.Date = gb.Date.UTC()
		out = append(out, gb)
	}
	return out, rows.Err()
}

// ListGroupPatternKeys returns the distinct pattern keys of the members of a
// group (bounces and DMARC records), sorted.
func ListGroupPatternKeys(db *sql.DB, groupKey string) ([]string, error) {
	rows, err := db.Query(`SELECT pattern_key FROM bounces WHERE group_key = ?
		UNION SELECT pattern_key FROM dmarc_records WHERE group_key = ?
		ORDER BY pattern_key`, groupKey, groupKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// prefixColumns qualifies every column of a comma-separated column list
// with a table alias ("b." + "id, group_key" -> "b.id, b.group_key").
func prefixColumns(alias, columns string) string {
	parts := strings.Split(columns, ",")
	for i, p := range parts {
		parts[i] = alias + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}

// GroupBounceStats summarizes the members of a group for display: the
// recipients and remote MTAs of its bounces, and the remote IPs of its
// bounces together with the sending IPs of its DMARC records.
type GroupBounceStats struct {
	Recipients []string `json:"recipients"`
	RemoteIPs  []string `json:"remote_ips"`
	RemoteMTAs []string `json:"remote_mtas"`
}

// GroupStats returns the distinct recipients, remote IPs and remote MTAs of a group.
func GroupStats(db *sql.DB, groupKey string) (*GroupBounceStats, error) {
	st := &GroupBounceStats{Recipients: []string{}, RemoteIPs: []string{}, RemoteMTAs: []string{}}
	for _, q := range []struct {
		query string
		dst   *[]string
	}{
		{`SELECT DISTINCT recipient FROM bounces WHERE group_key = ?1 AND recipient <> '' ORDER BY 1`, &st.Recipients},
		{`SELECT remote_ip FROM bounces WHERE group_key = ?1 AND remote_ip <> ''
		  UNION SELECT source_ip FROM dmarc_records WHERE group_key = ?1 AND source_ip <> '' ORDER BY 1`, &st.RemoteIPs},
		{`SELECT DISTINCT remote_mta FROM bounces WHERE group_key = ?1 AND remote_mta <> '' ORDER BY 1`, &st.RemoteMTAs},
	} {
		rows, err := db.Query(q.query, groupKey)
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
