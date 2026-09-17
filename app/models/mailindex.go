package models

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, registered as "sqlite"
)

// Per-mailbox index database (data/mails/<address>.sqlite).
//
// The index holds everything MailCare knows about the fetched mail of one
// address: the parsed headers and body layout of every message, the bounce
// details extracted from the daemon notices, the groups those bounces are
// bundled into and the reports the agent produced. It is derived data: apart
// from the group states and the reports (carried over by reindex) everything
// can be rebuilt from the raw .eml files, so it is NOT managed by goose
// migrations. The schema carries a version in PRAGMA user_version; when the
// stored version is older than MailIndexSchemaVersion the caller must rebuild
// the index from the raw files (OpenMailIndex returns ErrMailIndexOutdated).
//
// Design rules: every attribute is a real column (the index has no JSON
// column) and no column has a default value; text whose length is known is
// declared VARCHAR(n) and text of unpredictable length TEXT (SQLite does not
// enforce the declared length; the length of user input is checked where it
// is entered, mail-derived text is stored whole); enumerations are kept by
// the application constants, not by constraints; every DATETIME column holds
// UTC.
//
// Tables (one model file per table):
//   messages      app/models/message.go       every fetched mail (headers, body layout, detection outcome)
//   bounces       app/models/bounce.go        details extracted from a bounce message (1:1 with its message row)
//   groups        app/models/bounce_group.go  bounces bundled by the unit an administrator acts on
//   agent_reports app/models/agent_report.go  analysis produced by an agent CLI

// MailIndexSchemaVersion is the version stamped into PRAGMA user_version of
// every index file. The initial release ships version 1; a future release that
// changes the index schema raises it, and an index stamped with a lower
// version is rebuilt from the raw files instead of migrated.
const MailIndexSchemaVersion = 1

// ErrMailIndexOutdated is returned by OpenMailIndex when the file was created
// with an older schema and must be rebuilt.
var ErrMailIndexOutdated = errors.New("mail index schema is outdated; rebuild the index")

const mailIndexSchema = `
CREATE TABLE IF NOT EXISTS messages (
    id                  INTEGER       PRIMARY KEY AUTOINCREMENT,
    message_key         VARCHAR(28)   NOT NULL UNIQUE,
    folder              TEXT          NOT NULL,
    uidvalidity         INTEGER       NOT NULL,
    uid                 INTEGER       NOT NULL,
    message_id          TEXT          NOT NULL,
    subject             TEXT          NOT NULL,
    from_address        TEXT          NOT NULL,
    from_name           TEXT          NOT NULL,
    to_address          TEXT          NOT NULL,
    to_name             TEXT          NOT NULL,
    date                DATETIME      NOT NULL,
    received_at         DATETIME      ,
    size                INTEGER       NOT NULL,
    text_count          INTEGER       NOT NULL,
    html_count          INTEGER       NOT NULL,
    body_source         VARCHAR(16)   NOT NULL,
    classified          INTEGER       NOT NULL,
    is_bounce           INTEGER       NOT NULL,
    bounce_kind         VARCHAR(32)   NOT NULL,
    rule                VARCHAR(64)   NOT NULL,
    fetched_at          DATETIME      NOT NULL,
    UNIQUE (folder, uidvalidity, uid)
);
CREATE INDEX IF NOT EXISTS idx_messages_date ON messages(date);
CREATE INDEX IF NOT EXISTS idx_messages_message_id ON messages(message_id);
CREATE INDEX IF NOT EXISTS idx_messages_bounce ON messages(is_bounce, date);
CREATE INDEX IF NOT EXISTS idx_messages_classified ON messages(classified);

CREATE TABLE IF NOT EXISTS bounces (
    id                  INTEGER       PRIMARY KEY REFERENCES messages(id) ON DELETE CASCADE,
    group_key           VARCHAR(16)   NOT NULL,
    recipient           TEXT          NOT NULL,
    recipient_domain    TEXT          NOT NULL,
    action              VARCHAR(32)   NOT NULL,
    status_code         VARCHAR(11)   NOT NULL,
    smtp_code           VARCHAR(3)    NOT NULL,
    diagnostic          TEXT          NOT NULL,
    diagnostic_template TEXT          NOT NULL,
    remote_mta          TEXT          NOT NULL,
    remote_ip           VARCHAR(45)   NOT NULL,
    reporting_mta       TEXT          NOT NULL,
    original_message_id TEXT          NOT NULL,
    original_subject    TEXT          NOT NULL,
    original_from       TEXT          NOT NULL,
    original_date       DATETIME
);
CREATE INDEX IF NOT EXISTS idx_bounces_group ON bounces(group_key);

CREATE TABLE IF NOT EXISTS groups (
    group_key           VARCHAR(16)   NOT NULL PRIMARY KEY,
    category            VARCHAR(64)   NOT NULL,
    actionable          INTEGER       NOT NULL,
    unit_value          TEXT          NOT NULL,
    authority           TEXT          NOT NULL,
    recipient_domain    TEXT          NOT NULL,
    status_code         VARCHAR(11)   NOT NULL,
    diagnostic_template TEXT          NOT NULL,
    responsible         VARCHAR(32)   NOT NULL,
    state               VARCHAR(32)   NOT NULL,
    state_updated_at    DATETIME      ,
    needs_analysis      INTEGER       NOT NULL,
    message_count       INTEGER       NOT NULL,
    recipient_count     INTEGER       NOT NULL,
    remote_ip_count     INTEGER       NOT NULL,
    first_seen          DATETIME      ,
    last_seen           DATETIME      ,
    created_at          DATETIME      NOT NULL,
    updated_at          DATETIME      NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_groups_state ON groups(actionable, state, last_seen);

CREATE TABLE IF NOT EXISTS agent_reports (
    id                  INTEGER       PRIMARY KEY AUTOINCREMENT,
    group_key           VARCHAR(16)   NOT NULL REFERENCES groups(group_key) ON DELETE CASCADE,
    provider            VARCHAR(64)   NOT NULL,
    status              VARCHAR(32)   NOT NULL,
    severity            VARCHAR(32)   NOT NULL,
    responsible         VARCHAR(32)   NOT NULL,
    summary             TEXT          NOT NULL,
    report_markdown     TEXT          NOT NULL,
    error_message       TEXT          NOT NULL,
    message_count       INTEGER       NOT NULL,
    started_at          DATETIME      ,
    finished_at         DATETIME      ,
    created_at          DATETIME      NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_agent_reports_group ON agent_reports(group_key, id);
`

// OpenMailIndex opens (creating when absent) the per-mailbox index at path and
// ensures its schema. A file created with an older schema is left untouched and
// ErrMailIndexOutdated is returned so the caller can delete and rebuild it.
func OpenMailIndex(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_time_format=sqlite"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		db.Close()
		return nil, err
	}
	if version != 0 && version < MailIndexSchemaVersion {
		db.Close()
		return nil, ErrMailIndexOutdated
	}
	if _, err := db.Exec(mailIndexSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create the mail index schema: %w", err)
	}
	if version == 0 {
		if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, MailIndexSchemaVersion)); err != nil {
			db.Close()
			return nil, err
		}
	}
	return db, nil
}

// ClearMailClassification resets the per-message detection outcome (is_bounce,
// bounce_kind, rule, body_source, classified) and removes every bounces row,
// but keeps the messages, the groups and the agent reports. Both statements
// run in one transaction, so the index never holds bounce details of
// messages that count as unclassified. Reclassify recomputes the groups
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
