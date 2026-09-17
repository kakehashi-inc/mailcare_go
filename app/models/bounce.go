package models

import (
	"database/sql"
)

// Bounce is the detail extracted from one bounce message (delivery-status part
// and/or body text). It is stored in the detail_info JSON column of the message row
// (messages.detail_info, next to the "rule" name of the detection rule), so there is
// no separate table: the details are read only together with their message,
// and the per-group aggregates (recipients, remote IPs, MTAs) are computed with
// json_extract over the group's messages.
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
	Responsible        string       `json:"responsible"`
	OriginalMessageID  string       `json:"original_message_id"`
	OriginalFrom       string       `json:"original_from"`
	OriginalSubject    string       `json:"original_subject"`
	OriginalDate       sql.NullTime `json:"-"`
}

// bounceJSON is the JSON shape of messages.detail_info (only "rule" for a non-bounce).
type bounceJSON struct {
	OriginalRecipient  string `json:"recipient,omitempty"`
	RecipientDomain    string `json:"recipient_domain,omitempty"`
	Action             string `json:"action,omitempty"`
	StatusCode         string `json:"status_code,omitempty"`
	SMTPCode           string `json:"smtp_code,omitempty"`
	Diagnostic         string `json:"diagnostic,omitempty"`
	DiagnosticTemplate string `json:"diagnostic_template,omitempty"`
	RemoteMTA          string `json:"remote_mta,omitempty"`
	RemoteIP           string `json:"remote_ip,omitempty"`
	ReportingMTA       string `json:"reporting_mta,omitempty"`
	Responsible        string `json:"responsible,omitempty"`
	OriginalMessageID  string `json:"original_message_id,omitempty"`
	OriginalFrom       string `json:"original_from,omitempty"`
	OriginalSubject    string `json:"original_subject,omitempty"`
	OriginalDate       string `json:"original_date,omitempty"`
}

// UpsertBounce stores the bounce details in the message row. Text values are
// truncated to sensible bounds.
func UpsertBounce(db *sql.DB, b *Bounce) error {
	j := bounceJSON{
		OriginalRecipient:  truncateRunes(b.OriginalRecipient, 320),
		RecipientDomain:    truncateRunes(b.RecipientDomain, 253),
		Action:             b.Action,
		StatusCode:         truncateRunes(b.StatusCode, 11),
		SMTPCode:           truncateRunes(b.SMTPCode, 3),
		Diagnostic:         truncateRunes(b.Diagnostic, 4000),
		DiagnosticTemplate: truncateRunes(b.DiagnosticTemplate, 300),
		RemoteMTA:          truncateRunes(b.RemoteMTA, 253),
		RemoteIP:           truncateRunes(b.RemoteIP, 45),
		ReportingMTA:       truncateRunes(b.ReportingMTA, 253),
		Responsible:        b.Responsible,
		OriginalMessageID:  truncateRunes(b.OriginalMessageID, 998),
		OriginalFrom:       truncateRunes(b.OriginalFrom, 500),
		OriginalSubject:    truncateRunes(b.OriginalSubject, 2000),
		OriginalDate:       jsonTime(utcNullTime(b.OriginalDate)),
	}
	// Keep the detection rule name already stored in the column.
	res, err := db.Exec(`UPDATE messages SET detail_info = json_set(?, '$.rule', json_extract(detail_info, '$.rule')) WHERE id = ?`,
		marshalJSON(j), b.MessageID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// GetBounceByMessageID returns the bounce details of a message. sql.ErrNoRows
// is returned when the message does not exist or carries no bounce details.
func GetBounceByMessageID(db *sql.DB, messageID int64) (*Bounce, error) {
	var raw string
	if err := db.QueryRow(`SELECT detail_info FROM messages WHERE id = ?`, messageID).Scan(&raw); err != nil {
		return nil, err
	}
	var j bounceJSON
	unmarshalJSON(raw, &j)
	if j.OriginalRecipient == "" && j.StatusCode == "" && j.Diagnostic == "" && j.RemoteMTA == "" {
		return nil, sql.ErrNoRows
	}
	return &Bounce{
		MessageID: messageID, OriginalRecipient: j.OriginalRecipient, RecipientDomain: j.RecipientDomain,
		Action: j.Action, StatusCode: j.StatusCode, SMTPCode: j.SMTPCode, Diagnostic: j.Diagnostic,
		DiagnosticTemplate: j.DiagnosticTemplate, RemoteMTA: j.RemoteMTA, RemoteIP: j.RemoteIP,
		ReportingMTA: j.ReportingMTA, Responsible: j.Responsible, OriginalMessageID: j.OriginalMessageID,
		OriginalFrom: j.OriginalFrom, OriginalSubject: j.OriginalSubject, OriginalDate: parseJSONTime(j.OriginalDate),
	}, nil
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
		path string
		dst  *[]string
	}{
		{"$.recipient", &st.Recipients},
		{"$.remote_ip", &st.RemoteIPs},
		{"$.remote_mta", &st.RemoteMTAs},
	} {
		rows, err := db.Query(`SELECT DISTINCT json_extract(detail_info, ?) AS v FROM messages
			WHERE group_key = ? AND v IS NOT NULL AND v <> '' ORDER BY v`, q.path, groupKey)
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
