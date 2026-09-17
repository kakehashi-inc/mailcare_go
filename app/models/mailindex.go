package models

import (
	"database/sql"
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
// Tables (one model file per table):
//   messages      app/models/message.go       every fetched mail (bounce or not)
//   bounces       app/models/bounce.go        extracted details of a bounce mail
//   groups        app/models/bounce_group.go  similar bounces bundled together
//   agent_reports app/models/agent_report.go  analysis produced by an agent CLI

// MailIndexSchemaVersion is bumped whenever the index schema changes.
const MailIndexSchemaVersion = 2

// ErrMailIndexOutdated is returned by OpenMailIndex when the file was created
// with an older schema and must be rebuilt.
var ErrMailIndexOutdated = errors.New("mail index schema is outdated; rebuild the index")

const mailIndexSchema = `
CREATE TABLE IF NOT EXISTS messages (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    message_key      TEXT    NOT NULL UNIQUE,
    uid              INTEGER NOT NULL,
    uidvalidity      INTEGER NOT NULL,
    folder           TEXT    NOT NULL DEFAULT 'INBOX',
    message_id       TEXT    NOT NULL DEFAULT '',
    subject          TEXT    NOT NULL DEFAULT '',
    from_address     TEXT    NOT NULL DEFAULT '',
    from_name        TEXT    NOT NULL DEFAULT '',
    to_address       TEXT    NOT NULL DEFAULT '',
    date             DATETIME,
    received_at      DATETIME,
    size             INTEGER NOT NULL DEFAULT 0,
    has_text         INTEGER NOT NULL DEFAULT 0,
    has_html         INTEGER NOT NULL DEFAULT 0,
    is_bounce        INTEGER NOT NULL DEFAULT 0,
    bounce_kind      TEXT    NOT NULL DEFAULT '',
    classify_reason  TEXT    NOT NULL DEFAULT '',
    group_key        TEXT    NOT NULL DEFAULT '',
    classified       INTEGER NOT NULL DEFAULT 0,
    fetched_at       DATETIME NOT NULL,
    UNIQUE(uidvalidity, uid, folder)
);
CREATE INDEX IF NOT EXISTS idx_messages_classified ON messages(classified);
CREATE INDEX IF NOT EXISTS idx_messages_date ON messages(date);
CREATE INDEX IF NOT EXISTS idx_messages_group ON messages(group_key);
CREATE INDEX IF NOT EXISTS idx_messages_bounce ON messages(is_bounce, date);

CREATE TABLE IF NOT EXISTS bounces (
    message_id           INTEGER PRIMARY KEY REFERENCES messages(id) ON DELETE CASCADE,
    original_recipient   TEXT NOT NULL DEFAULT '',
    recipient_domain     TEXT NOT NULL DEFAULT '',
    action               TEXT NOT NULL DEFAULT '',
    status_code          TEXT NOT NULL DEFAULT '',
    smtp_code            TEXT NOT NULL DEFAULT '',
    diagnostic           TEXT NOT NULL DEFAULT '',
    diagnostic_template  TEXT NOT NULL DEFAULT '',
    remote_mta           TEXT NOT NULL DEFAULT '',
    remote_ip            TEXT NOT NULL DEFAULT '',
    reporting_mta        TEXT NOT NULL DEFAULT '',
    original_message_id  TEXT NOT NULL DEFAULT '',
    original_subject     TEXT NOT NULL DEFAULT '',
    original_from        TEXT NOT NULL DEFAULT '',
    original_date        DATETIME,
    responsible          TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_bounces_recipient ON bounces(original_recipient);
CREATE INDEX IF NOT EXISTS idx_bounces_domain ON bounces(recipient_domain);

CREATE TABLE IF NOT EXISTS groups (
    group_key            TEXT PRIMARY KEY,
    title                TEXT NOT NULL DEFAULT '',
    category             TEXT NOT NULL DEFAULT '',
    unit_value           TEXT NOT NULL DEFAULT '',
    authority            TEXT NOT NULL DEFAULT '',
    actionable           INTEGER NOT NULL DEFAULT 1,
    bounce_kind          TEXT NOT NULL DEFAULT '',
    recipient_domain     TEXT NOT NULL DEFAULT '',
    status_code          TEXT NOT NULL DEFAULT '',
    smtp_code            TEXT NOT NULL DEFAULT '',
    diagnostic_template  TEXT NOT NULL DEFAULT '',
    responsible          TEXT NOT NULL DEFAULT '',
    message_count        INTEGER NOT NULL DEFAULT 0,
    recipient_count      INTEGER NOT NULL DEFAULT 0,
    remote_ip_count      INTEGER NOT NULL DEFAULT 0,
    first_seen           DATETIME,
    last_seen            DATETIME,
    state                TEXT NOT NULL DEFAULT 'open',
    state_updated_at     DATETIME,
    needs_analysis       INTEGER NOT NULL DEFAULT 1,
    created_at           DATETIME NOT NULL,
    updated_at           DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_groups_state ON groups(state, last_seen);
CREATE INDEX IF NOT EXISTS idx_groups_actionable ON groups(actionable, state);

CREATE TABLE IF NOT EXISTS agent_reports (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    group_key        TEXT NOT NULL REFERENCES groups(group_key) ON DELETE CASCADE,
    provider         TEXT NOT NULL,
    status           TEXT NOT NULL,
    summary          TEXT NOT NULL DEFAULT '',
    responsible      TEXT NOT NULL DEFAULT '',
    severity         TEXT NOT NULL DEFAULT '',
    report_markdown  TEXT NOT NULL DEFAULT '',
    error_message    TEXT NOT NULL DEFAULT '',
    message_count    INTEGER NOT NULL DEFAULT 0,
    started_at       DATETIME,
    finished_at      DATETIME,
    created_at       DATETIME NOT NULL
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
	for _, table := range []string{"agent_reports", "groups", "bounces", "messages"} {
		if _, err := db.Exec(`DELETE FROM ` + table); err != nil {
			return err
		}
	}
	return nil
}

// ClearMailClassification removes the per-message classification (bounces
// table and the bounce columns of messages) but keeps the messages, the
// groups and the agent reports. Reclassify recomputes the groups afterwards
// (group keys are deterministic, so a group that comes back keeps its state
// and reports) and DeleteEmptyGroups drops the groups that vanished together
// with their reports (cascade).
func ClearMailClassification(db *sql.DB) error {
	for _, q := range []string{
		`DELETE FROM bounces`,
		`UPDATE messages SET is_bounce = 0, bounce_kind = '', classify_reason = '', group_key = '', classified = 0`,
	} {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	return nil
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
