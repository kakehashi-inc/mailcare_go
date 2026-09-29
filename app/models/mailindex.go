package models

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, registered as "sqlite"

	"mailcare/app/modules/dbschema"
)

// Per-mailbox index database (data/mails/<address>.sqlite).
//
// The index holds everything MailCare knows about the fetched mail of one
// address: the parsed headers and body layout of every message, the bounce
// details extracted from the daemon notices, the failing records of the
// DMARC aggregate reports, the groups those bounces and records are bundled
// into, their state history and the reports the agent produced. Apart from
// the group states, their history and the reports (carried over by reindex)
// everything can be rebuilt from the raw .eml files.
//
// The schema is managed by goose migrations like the master database's:
// embedded/migrations/index/*.sql, applied by OpenMailIndex every time an index
// is opened (the file records the applied versions in its own
// goose_db_version table). The design rules of the columns are stated in
// embedded/migrations/index/0001_init.sql.
//
// Tables (one model file per table):
//   messages      app/models/message.go       every fetched mail (headers, body layout, detection outcome)
//   bounces       app/models/bounce.go        details extracted from a bounce message (1:1 with its message row)
//   dmarc_records app/models/dmarc_record.go  failing records of a DMARC aggregate report (1:N with its message row)
//   groups        app/models/bounce_group.go  bounces and DMARC records bundled by the unit an administrator acts on
//   agent_reports app/models/agent_report.go  analysis produced by an agent CLI
//   agent_report_patterns app/models/agent_report_pattern.go  member patterns (bounces, DMARC records) a settling report covered
//   group_state_changes   app/models/group_state_change.go    the state history of a group (who set which state, with what choice and note)
//   group_state_change_patterns app/models/group_state_change_pattern.go  re-check keys of the members at a state change

// OpenMailIndex opens (creating when absent) the per-mailbox index at path
// and applies its pending migrations. An index created before the migrations
// were introduced (same tables, no goose_db_version table) is adopted as it
// is: the first migration only creates what is missing.
func OpenMailIndex(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_time_format=sqlite"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := dbschema.Apply(context.Background(), db, dbschema.Index()); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to migrate the mail index: %w", err)
	}
	return db, nil
}

// ClearMailClassification resets the per-message detection outcome (is_bounce,
// bounce_kind, rule, body_source, classified) and removes every bounces and
// dmarc_records row, but keeps the messages, the groups and the agent
// reports. The statements run in one transaction, so the index never holds
// bounce details of messages that count as unclassified. Reclassify recomputes the groups
// afterwards (group keys are deterministic, so a group that comes back keeps
// its state and reports) and DeleteEmptyGroups drops the groups that vanished
// together with their reports (cascade).
func ClearMailClassification(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM bounces`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM dmarc_records`); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE messages SET is_bounce = 0, bounce_kind = '', rule = '', body_source = '', classified = 0`); err != nil {
		return err
	}
	return tx.Commit()
}

// --- Helpers shared by the models ---

// Execer is the part of database/sql the model functions need to run their
// statements; both *sql.DB and *sql.Tx satisfy it, so a caller can run
// several model calls inside one transaction (the grouping phase writes the
// group, the bounce details and the detection outcome of one message
// atomically) while the plain callers keep passing the database.
type Execer interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// likeEscape is the escape character of the LIKE patterns built from user
// input (see likeContains).
const likeEscape = `\`

// likeContains turns a search string into a LIKE pattern that matches the
// string anywhere in a value, escaping the LIKE wildcards ("%", "_") and the
// escape character itself so that they match literally. The pattern must be
// used with ESCAPE '\' (the likeEscapeClause constant).
func likeContains(q string) string {
	r := strings.NewReplacer(likeEscape, likeEscape+likeEscape, "%", likeEscape+"%", "_", likeEscape+"_")
	return "%" + r.Replace(q) + "%"
}

// likeEscapeClause is the ESCAPE clause that goes with likeContains.
const likeEscapeClause = ` ESCAPE '\'`

// marshalJSON encodes v for a JSON column ("{}" on failure so the column
// always holds valid JSON).
func marshalJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// unmarshalJSON decodes a JSON column into v; an empty or invalid value leaves v untouched.
func unmarshalJSON(s string, v any) {
	if s == "" || s == "{}" {
		return
	}
	_ = json.Unmarshal([]byte(s), v)
}

// sqliteTimeLayouts are the textual forms a DATETIME value can take when it
// comes back from an expression (MIN/MAX/...) instead of a typed column: the
// modernc driver writes time.Time values with the first layout; the other
// layouts are accepted defensively for values written by SQL expressions
// such as datetime('now') (no column has a default value, the application
// writes every DATETIME as time.Time).
var sqliteTimeLayouts = []string{
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02T15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05Z",
}

// ParseSQLiteTime converts a DATETIME value scanned as text (aggregate
// expressions such as MIN(date) carry no declared type, so the driver returns a
// string) into a sql.NullTime. Unparsable or NULL values yield an invalid time.
func ParseSQLiteTime(v sql.NullString) sql.NullTime {
	if !v.Valid || v.String == "" {
		return sql.NullTime{}
	}
	for _, layout := range sqliteTimeLayouts {
		if t, err := time.Parse(layout, v.String); err == nil {
			return sql.NullTime{Time: t.UTC(), Valid: true}
		}
	}
	return sql.NullTime{}
}

// utcNullTime normalizes a valid NullTime to UTC (and drops the monotonic
// clock reading) so the driver stores a canonical, sortable text value.
func utcNullTime(t sql.NullTime) sql.NullTime {
	if !t.Valid {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: t.Time.UTC().Round(0), Valid: true}
}

// NullTime converts a time to sql.NullTime (zero -> NULL).
func NullTime(t time.Time) sql.NullTime {
	if t.IsZero() {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: t.UTC(), Valid: true}
}
