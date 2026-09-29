package models

import (
	"database/sql"
	"fmt"
	"time"
)

// DMARCRecord is a row of the per-mailbox dmarc_records table: one record of
// a DMARC aggregate report that failed DMARC (neither DKIM nor SPF passed
// with alignment), filed into a group like a bounce. A report mail can hold
// several failing records in different groups, so the rows are 1:N with
// their message (deleted with it); records that passed are not kept. The
// report metadata is repeated on every row of the report.
type DMARCRecord struct {
	ID           int64  `json:"-"`
	MessageID    int64  `json:"-"` // = messages.id of the report mail
	GroupKey     string `json:"group_key"`
	CategoryRule string `json:"category_rule"` // the category rule that filed the record
	// DiagnosticTemplate is the shape of the failure (the aligned results
	// and the disposition, mailengine.DMARCTemplate); the pattern key is
	// derived from it.
	DiagnosticTemplate string       `json:"diagnostic_template"`
	PatternKey         string       `json:"pattern_key"`
	ReportOrg          string       `json:"report_org"` // report_metadata/org_name: the receiver that reported
	ReportID           string       `json:"report_id"`
	BeginAt            sql.NullTime `json:"-"` // report_metadata/date_range
	EndAt              sql.NullTime `json:"-"`
	PolicyDomain       string       `json:"policy_domain"` // policy_published/domain
	Policy             string       `json:"policy"`        // policy_published/p
	HeaderFrom         string       `json:"header_from"`
	EnvelopeFrom       string       `json:"envelope_from"`
	SourceIP           string       `json:"source_ip"`
	MessageCount       int          `json:"message_count"` // row/count: messages the receiver saw
	Disposition        string       `json:"disposition"`   // none | quarantine | reject
	DKIMResult         string       `json:"dkim_result"`   // policy_evaluated/dkim
	SPFResult          string       `json:"spf_result"`    // policy_evaluated/spf
	DKIMAuth           string       `json:"dkim_auth"`     // auth_results/dkim as "domain result, ..." ("none" when absent)
	SPFAuth            string       `json:"spf_auth"`      // auth_results/spf as "domain result, ..." ("none" when absent)
}

const dmarcRecordColumns = `id, message_id, group_key, category_rule, diagnostic_template, pattern_key, report_org, report_id, begin_at,
	end_at, policy_domain, policy, header_from, envelope_from, source_ip, message_count, disposition, dkim_result,
	spf_result, dkim_auth, spf_auth`

// ReplaceDMARCRecords stores the failing records of a report mail, replacing
// the ones stored before (none removes them).
func ReplaceDMARCRecords(db Execer, messageID int64, records []*DMARCRecord) error {
	if err := DeleteDMARCRecords(db, messageID); err != nil {
		return err
	}
	for _, r := range records {
		r.MessageID = messageID
		r.BeginAt, r.EndAt = utcNullTime(r.BeginAt), utcNullTime(r.EndAt)
		res, err := db.Exec(
			`INSERT INTO dmarc_records (message_id, group_key, category_rule, diagnostic_template, pattern_key, report_org,
			   report_id, begin_at, end_at, policy_domain, policy, header_from, envelope_from, source_ip, message_count,
			   disposition, dkim_result, spf_result, dkim_auth, spf_auth)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.MessageID, r.GroupKey, r.CategoryRule, r.DiagnosticTemplate, r.PatternKey, r.ReportOrg, r.ReportID, r.BeginAt, r.EndAt,
			r.PolicyDomain, r.Policy, r.HeaderFrom, r.EnvelopeFrom, r.SourceIP, r.MessageCount, r.Disposition,
			r.DKIMResult, r.SPFResult, r.DKIMAuth, r.SPFAuth,
		)
		if err != nil {
			return fmt.Errorf("insert dmarc record: %w", err)
		}
		r.ID, _ = res.LastInsertId()
	}
	return nil
}

// DeleteDMARCRecords removes the records of a report mail, if any.
func DeleteDMARCRecords(db Execer, messageID int64) error {
	_, err := db.Exec(`DELETE FROM dmarc_records WHERE message_id = ?`, messageID)
	return err
}

// ListDMARCRecordsByGroup returns the dmarc_records rows of a group, in
// record id order.
func ListDMARCRecordsByGroup(db *sql.DB, groupKey string) ([]*DMARCRecord, error) {
	rows, err := db.Query(`SELECT `+dmarcRecordColumns+` FROM dmarc_records WHERE group_key = ? ORDER BY id`, groupKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*DMARCRecord
	for rows.Next() {
		r := &DMARCRecord{}
		if err := rows.Scan(&r.ID, &r.MessageID, &r.GroupKey, &r.CategoryRule, &r.DiagnosticTemplate, &r.PatternKey,
			&r.ReportOrg, &r.ReportID, &r.BeginAt, &r.EndAt, &r.PolicyDomain, &r.Policy, &r.HeaderFrom, &r.EnvelopeFrom,
			&r.SourceIP, &r.MessageCount, &r.Disposition, &r.DKIMResult, &r.SPFResult, &r.DKIMAuth, &r.SPFAuth); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// groupDMARCRecords returns the records of a group as GroupBounce entries
// (the form the agent evidence takes): the pattern, the rule, the sending IP
// and the report mail's key and date, with the record itself in DMARC and
// the diagnostic source "dmarc".
func groupDMARCRecords(db *sql.DB, groupKey string) ([]*GroupBounce, error) {
	rows, err := db.Query(`SELECT m.message_key, m.date, m.body_source, `+prefixColumns("d.", dmarcRecordColumns)+`
		FROM dmarc_records d JOIN messages m ON m.id = d.message_id WHERE d.group_key = ?
		ORDER BY m.date DESC, m.id DESC, d.id`, groupKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*GroupBounce
	for rows.Next() {
		gb := &GroupBounce{}
		var date time.Time
		r := &DMARCRecord{}
		if err := rows.Scan(&gb.MessageKey, &date, &gb.BodySource, &r.ID, &r.MessageID, &r.GroupKey, &r.CategoryRule,
			&r.DiagnosticTemplate, &r.PatternKey, &r.ReportOrg, &r.ReportID, &r.BeginAt, &r.EndAt, &r.PolicyDomain, &r.Policy, &r.HeaderFrom,
			&r.EnvelopeFrom, &r.SourceIP, &r.MessageCount, &r.Disposition, &r.DKIMResult, &r.SPFResult, &r.DKIMAuth,
			&r.SPFAuth); err != nil {
			return nil, err
		}
		gb.Date = date.UTC()
		gb.ID = r.MessageID
		gb.GroupKey = r.GroupKey
		gb.CategoryRule = r.CategoryRule
		gb.PatternKey = r.PatternKey
		gb.DiagnosticTemplate = r.DiagnosticTemplate
		gb.Diagnostic = r.Summary()
		gb.RemoteIP = r.SourceIP
		gb.ReportingMTA = r.ReportOrg
		gb.DiagnosticSource = DiagnosticSourceDMARC
		gb.DMARC = r
		out = append(out, gb)
	}
	return out, rows.Err()
}

// Summary is the record on one line: the sending IP and identifiers, the
// count, the results and the disposition (the diagnostic of the member).
func (r *DMARCRecord) Summary() string {
	return fmt.Sprintf("DMARC fail for header_from %s from %s (%d messages, envelope_from %s): dkim %s (%s); spf %s (%s); disposition %s",
		orDash(r.HeaderFrom), orDash(r.SourceIP), r.MessageCount, orDash(r.EnvelopeFrom), orDash(r.DKIMResult), r.DKIMAuth,
		orDash(r.SPFResult), r.SPFAuth, orDash(r.Disposition))
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// DiagnosticSourceDMARC is the diagnostic source of a group member that is a
// DMARC record (same literal as mailengine.DiagnosticSourceDMARC; models
// must not import mailengine).
const DiagnosticSourceDMARC = "dmarc"
