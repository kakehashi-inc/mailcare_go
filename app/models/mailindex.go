package models

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, registered as "sqlite"
)

// Per-mailbox index database (data/mails/<address>.sqlite).
//
// The index is derived data: everything in it can be rebuilt from the raw
// message files (reindex), so it is NOT managed by goose migrations. Instead the
// schema carries a version in PRAGMA user_version; when the stored version is
// older than MailIndexSchemaVersion the caller must rebuild the index from the
// raw files (OpenMailIndex returns ErrMailIndexOutdated).
//
// Design rules: scalar attributes of a row are real columns; the structured
// bounce extraction (many optional fields, one per message, never searched)
// is the JSON column messages.detail_info. JSON columns are always named
// "details" so a column name never ties a table to one use. SQLite ignores declared text sizes, so
// bounded columns carry CHECK constraints and the writers truncate to those
// bounds. Every DATETIME column holds UTC.
//
// Tables (one model file per table):
//   messages      app/models/message.go       every fetched mail; bounce details in messages.detail_info (bounce.go)
//                                             (the detection rule name is kept inside that JSON as "rule")
//   groups        app/models/bounce_group.go  bounces bundled by the unit an administrator acts on
//   agent_reports app/models/agent_report.go  analysis produced by an agent CLI

// MailIndexSchemaVersion is bumped whenever the index schema changes.
const MailIndexSchemaVersion = 3

// ErrMailIndexOutdated is returned by OpenMailIndex when the file was created
// with an older schema and must be rebuilt.
var ErrMailIndexOutdated = errors.New("mail index schema is outdated; rebuild the index")

const mailIndexSchema = `
CREATE TABLE IF NOT EXISTS messages (
    id           INTEGER  PRIMARY KEY AUTOINCREMENT,
    message_key  TEXT     NOT NULL UNIQUE CHECK (length(message_key) BETWEEN 1 AND 64),
    folder       TEXT     NOT NULL DEFAULT 'INBOX' CHECK (length(folder) BETWEEN 1 AND 255),
    uidvalidity  INTEGER  NOT NULL,
    uid          INTEGER  NOT NULL,
    message_id   TEXT     NOT NULL DEFAULT '' CHECK (length(message_id) <= 998),
    subject      TEXT     NOT NULL DEFAULT '' CHECK (length(subject) <= 2000),
    from_address TEXT     NOT NULL DEFAULT '' CHECK (length(from_address) <= 320),
    from_name    TEXT     NOT NULL DEFAULT '' CHECK (length(from_name) <= 500),
    to_address   TEXT     NOT NULL DEFAULT '' CHECK (length(to_address) <= 320),
    to_name      TEXT     NOT NULL DEFAULT '' CHECK (length(to_name) <= 500),
    date         DATETIME NOT NULL,
    received_at  DATETIME,
    size         INTEGER  NOT NULL DEFAULT 0 CHECK (size >= 0),
    has_text     INTEGER  NOT NULL DEFAULT 0 CHECK (has_text IN (0, 1)),
    has_html     INTEGER  NOT NULL DEFAULT 0 CHECK (has_html IN (0, 1)),
    body_source  TEXT     NOT NULL DEFAULT '' CHECK (body_source IN ('', 'text', 'html')),
    is_bounce    INTEGER  NOT NULL DEFAULT 0 CHECK (is_bounce IN (0, 1)),
    bounce_kind  TEXT     NOT NULL DEFAULT '' CHECK (bounce_kind IN ('', 'failed', 'delayed', 'auto_reply', 'other')),
    classified   INTEGER  NOT NULL DEFAULT 0 CHECK (classified IN (0, 1)),
    group_key    TEXT     NOT NULL DEFAULT '' CHECK (length(group_key) <= 16),
    detail_info      TEXT     NOT NULL DEFAULT '{}' CHECK (json_valid(detail_info)),
    fetched_at   DATETIME NOT NULL,
    UNIQUE(folder, uidvalidity, uid)
);
CREATE INDEX IF NOT EXISTS idx_messages_date ON messages(date);
CREATE INDEX IF NOT EXISTS idx_messages_message_id ON messages(message_id);
CREATE INDEX IF NOT EXISTS idx_messages_bounce ON messages(is_bounce, date);
CREATE INDEX IF NOT EXISTS idx_messages_classified ON messages(classified);
CREATE INDEX IF NOT EXISTS idx_messages_group ON messages(group_key);

CREATE TABLE IF NOT EXISTS groups (
    group_key           TEXT     PRIMARY KEY CHECK (length(group_key) = 16),
    category            TEXT     NOT NULL CHECK (category IN ('ip_blocked', 'rate_limited', 'auth_failure', 'sender_blocked',
                                 'content_rejected', 'message_too_large', 'server_config', 'unknown_failure',
                                 'user_unknown', 'mailbox_full', 'mailbox_disabled', 'domain_not_found', 'delivery_delay')),
    actionable          INTEGER  NOT NULL DEFAULT 1 CHECK (actionable IN (0, 1)),
    unit_value          TEXT     NOT NULL DEFAULT '' CHECK (length(unit_value) <= 320),
    authority           TEXT     NOT NULL DEFAULT '' CHECK (length(authority) <= 320),
    recipient_domain    TEXT     NOT NULL DEFAULT '' CHECK (length(recipient_domain) <= 253),
    status_code         TEXT     NOT NULL DEFAULT '' CHECK (length(status_code) <= 11),
    diagnostic_template TEXT     NOT NULL DEFAULT '' CHECK (length(diagnostic_template) <= 300),
    responsible         TEXT     NOT NULL DEFAULT '' CHECK (responsible IN ('', 'sender', 'recipient', 'domain', 'unknown')),
    state               TEXT     NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'resolved', 'ignored')),
    state_updated_at    DATETIME,
    needs_analysis      INTEGER  NOT NULL DEFAULT 1 CHECK (needs_analysis IN (0, 1)),
    message_count       INTEGER  NOT NULL DEFAULT 0 CHECK (message_count >= 0),
    recipient_count     INTEGER  NOT NULL DEFAULT 0 CHECK (recipient_count >= 0),
    remote_ip_count     INTEGER  NOT NULL DEFAULT 0 CHECK (remote_ip_count >= 0),
    first_seen          DATETIME,
    last_seen           DATETIME,
    created_at          DATETIME NOT NULL,
    updated_at          DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_groups_state ON groups(actionable, state, last_seen);

CREATE TABLE IF NOT EXISTS agent_reports (
    id              INTEGER  PRIMARY KEY AUTOINCREMENT,
    group_key       TEXT     NOT NULL REFERENCES groups(group_key) ON DELETE CASCADE,
    provider        TEXT     NOT NULL CHECK (length(provider) BETWEEN 1 AND 32),
    status          TEXT     NOT NULL CHECK (status IN ('running', 'completed', 'error')),
    severity        TEXT     NOT NULL DEFAULT '' CHECK (severity IN ('', 'high', 'medium', 'low')),
    responsible     TEXT     NOT NULL DEFAULT '' CHECK (responsible IN ('', 'sender', 'recipient', 'domain', 'unknown')),
    summary         TEXT     NOT NULL DEFAULT '' CHECK (length(summary) <= 1000),
    report_markdown TEXT     NOT NULL DEFAULT '',
    error_message   TEXT     NOT NULL DEFAULT '' CHECK (length(error_message) <= 2000),
    message_count   INTEGER  NOT NULL DEFAULT 0 CHECK (message_count >= 0),
    started_at      DATETIME,
    finished_at     DATETIME,
    created_at      DATETIME NOT NULL
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

// ClearMailIndex removes every row of every table (used by reindex before the
// raw files are re-imported). The schema is kept.
func ClearMailIndex(db *sql.DB) error {
	for _, table := range []string{"agent_reports", "groups", "messages"} {
		if _, err := db.Exec(`DELETE FROM ` + table); err != nil {
			return err
		}
	}
	return nil
}

// ClearMailClassification resets the per-message classification (bounce
// details, is_bounce, group membership, classified flag and the classification
// fields of meta) but keeps the messages, the groups and the agent reports.
// Reclassify recomputes the groups afterwards (group keys are deterministic, so
// a group that comes back keeps its state and reports) and DeleteEmptyGroups
// drops the groups that vanished together with their reports (cascade).
func ClearMailClassification(db *sql.DB) error {
	_, err := db.Exec(`UPDATE messages SET is_bounce = 0, bounce_kind = '', classified = 0, group_key = '', detail_info = '{}'`)
	return err
}

// --- JSON and time helpers shared by the index models ---

// marshalJSON encodes v for a JSON column ("{}" on failure so the CHECK holds).
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

// jsonTime is the RFC 3339 (UTC) form of a time inside a JSON column ("" for none).
func jsonTime(t sql.NullTime) string {
	if !t.Valid {
		return ""
	}
	return t.Time.UTC().Format(time.RFC3339Nano)
}

// parseJSONTime is the inverse of jsonTime.
func parseJSONTime(s string) sql.NullTime {
	if s == "" {
		return sql.NullTime{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: t.UTC(), Valid: true}
}

// sqliteTimeLayouts are the textual forms a DATETIME value can take when it
// comes back from an expression (MIN/MAX/...) instead of a typed column: the
// modernc driver writes time.Time values with the first layout, and rows
// created by SQL defaults use the plain "datetime('now')" form.
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

// truncateRunes shortens s to at most n runes (bounded text columns).
func truncateRunes(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
