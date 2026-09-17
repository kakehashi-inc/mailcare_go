package models

import (
	"database/sql"
	"errors"
	"time"
)

// AgentReport is a row of the per-mailbox agent_reports table: one analysis
// run of an agent CLI over a bounce group. The newest completed report of a
// group is the one shown to users; older rows are kept as history. The
// prompt, the CLI transcript and the report file of a run live in the run's
// workspace directory data/agent/<address>/<group_key>/<id>/.
type AgentReport struct {
	ID             int64        `json:"id"`
	GroupKey       string       `json:"group_key"`
	Provider       string       `json:"provider"`
	Status         string       `json:"status"`   // running | completed | error
	Severity       string       `json:"severity"` // high | medium | low | ""
	Responsible    string       `json:"responsible"`
	Summary        string       `json:"summary"`
	ReportMarkdown string       `json:"report_markdown"`
	ErrorMessage   string       `json:"error_message"`
	MessageCount   int          `json:"message_count"`
	StartedAt      sql.NullTime `json:"-"`
	FinishedAt     sql.NullTime `json:"-"`
	CreatedAt      time.Time    `json:"created_at"`
}

const agentReportColumns = `id, group_key, provider, status, severity, responsible, summary, report_markdown,
	error_message, message_count, started_at, finished_at, created_at`

// InsertAgentReport creates a running report row and fills in its ID.
func InsertAgentReport(db *sql.DB, r *AgentReport) error {
	now := time.Now().UTC()
	if r.Status == "" {
		r.Status = "running"
	}
	res, err := db.Exec(
		`INSERT INTO agent_reports (group_key, provider, status, severity, responsible, summary, report_markdown,
		   error_message, message_count, started_at, created_at)
		 VALUES (?, ?, ?, '', '', '', '', '', ?, ?, ?)`,
		r.GroupKey, r.Provider, r.Status, r.MessageCount, now, now,
	)
	if err != nil {
		return err
	}
	r.ID, _ = res.LastInsertId()
	r.StartedAt = sql.NullTime{Time: now, Valid: true}
	r.CreatedAt = now
	return nil
}

// CompleteAgentReport stores the parsed result of a finished run.
func CompleteAgentReport(db *sql.DB, id int64, summary, responsible, severity, markdown string) error {
	_, err := db.Exec(
		`UPDATE agent_reports SET status = 'completed', summary = ?, responsible = ?, severity = ?, report_markdown = ?,
		   finished_at = ? WHERE id = ?`,
		summary, responsible, severity, markdown, time.Now().UTC(), id,
	)
	return err
}

// FailAgentReport marks a run as failed.
func FailAgentReport(db *sql.DB, id int64, errMsg string) error {
	_, err := db.Exec(`UPDATE agent_reports SET status = 'error', error_message = ?, finished_at = ? WHERE id = ?`,
		errMsg, time.Now().UTC(), id)
	return err
}

// RestoreAgentReport inserts a report row as it was, id included, used when
// an index is rebuilt and the group key came back. The id must be kept
// because the run's workspace directory data/agent/<address>/<group_key>/<id>/
// is named after it (and, being an AUTOINCREMENT column, a fresh id would
// otherwise be assigned).
func RestoreAgentReport(db *sql.DB, r *AgentReport) error {
	if r.ID <= 0 {
		return errors.New("restore agent report: missing id")
	}
	_, err := db.Exec(
		`INSERT INTO agent_reports (id, group_key, provider, status, severity, responsible, summary, report_markdown,
		   error_message, message_count, started_at, finished_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.GroupKey, r.Provider, r.Status, r.Severity, r.Responsible, r.Summary,
		r.ReportMarkdown, r.ErrorMessage, r.MessageCount, utcNullTime(r.StartedAt),
		utcNullTime(r.FinishedAt), r.CreatedAt.UTC(),
	)
	return err
}

// LatestAgentReport returns the newest report of a group regardless of status
// (sql.ErrNoRows when none).
func LatestAgentReport(db *sql.DB, groupKey string) (*AgentReport, error) {
	return scanAgentReport(db.QueryRow(`SELECT `+agentReportColumns+` FROM agent_reports WHERE group_key = ? ORDER BY id DESC LIMIT 1`, groupKey))
}

// LatestCompletedAgentReport returns the newest completed report of a group
// (sql.ErrNoRows when none).
func LatestCompletedAgentReport(db *sql.DB, groupKey string) (*AgentReport, error) {
	return scanAgentReport(db.QueryRow(`SELECT `+agentReportColumns+` FROM agent_reports WHERE group_key = ? AND status = 'completed' ORDER BY id DESC LIMIT 1`, groupKey))
}

// LatestCompletedAgentReports returns the newest completed report per group as a map.
func LatestCompletedAgentReports(db *sql.DB) (map[string]*AgentReport, error) {
	rows, err := db.Query(`SELECT ` + agentReportColumns + ` FROM agent_reports r
		WHERE r.status = 'completed' AND r.id = (SELECT MAX(id) FROM agent_reports WHERE group_key = r.group_key AND status = 'completed')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*AgentReport{}
	for rows.Next() {
		r, err := scanAgentReport(rows)
		if err != nil {
			return nil, err
		}
		out[r.GroupKey] = r
	}
	return out, rows.Err()
}

// ListAgentReports returns every report of a group, newest first.
func ListAgentReports(db *sql.DB, groupKey string) ([]*AgentReport, error) {
	return queryAgentReports(db, `SELECT `+agentReportColumns+` FROM agent_reports WHERE group_key = ? ORDER BY id DESC`, groupKey)
}

// ListAllAgentReports returns every report of the index, oldest first (used to
// carry reports over when the index is rebuilt).
func ListAllAgentReports(db *sql.DB) ([]*AgentReport, error) {
	return queryAgentReports(db, `SELECT `+agentReportColumns+` FROM agent_reports ORDER BY id ASC`)
}

// ResetRunningAgentReports marks reports left running (e.g. after a crash) as error.
func ResetRunningAgentReports(db *sql.DB, reason string) error {
	_, err := db.Exec(`UPDATE agent_reports SET status = 'error', error_message = ?, finished_at = ? WHERE status = 'running'`,
		reason, time.Now().UTC())
	return err
}

func queryAgentReports(db *sql.DB, query string, args ...any) ([]*AgentReport, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*AgentReport
	for rows.Next() {
		r, err := scanAgentReport(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func scanAgentReport(s rowScanner) (*AgentReport, error) {
	r := &AgentReport{}
	if err := s.Scan(&r.ID, &r.GroupKey, &r.Provider, &r.Status, &r.Severity, &r.Responsible, &r.Summary,
		&r.ReportMarkdown, &r.ErrorMessage, &r.MessageCount, &r.StartedAt, &r.FinishedAt, &r.CreatedAt); err != nil {
		return nil, err
	}
	return r, nil
}
