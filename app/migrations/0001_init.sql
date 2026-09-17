-- +goose Up
-- Initial schema of the master database (data/mailcare.db).
--
-- Migrations are embedded into the binary and applied automatically when the
-- database is opened (see app/modules/db.go). Add a new file per change,
-- numbered sequentially (0002_<description>.sql, ...); never edit an applied
-- migration. Wrap each statement in StatementBegin/StatementEnd so goose can
-- run multi-line statements safely.
--
-- Per-mailbox indexes (data/mails/<address>.sqlite) are NOT managed here: their
-- schema lives in app/models/mailindex.go and is rebuilt from the raw files.

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT    NOT NULL UNIQUE,
    display_name  TEXT    NOT NULL,
    password_hash TEXT    NOT NULL,
    role          TEXT    NOT NULL DEFAULT 'user',
    created_at    DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at    DATETIME NOT NULL DEFAULT (datetime('now')),
    last_login_at DATETIME
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS tokens (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    identifier   TEXT    NOT NULL UNIQUE,
    name         TEXT    NOT NULL,
    token        TEXT    NOT NULL UNIQUE,
    expires_at   DATETIME,
    created_at   DATETIME NOT NULL DEFAULT (datetime('now')),
    last_used_at DATETIME
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_tokens_user_id ON tokens(user_id);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS mailboxes (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    address            TEXT    NOT NULL UNIQUE,
    display_name       TEXT    NOT NULL DEFAULT '',
    imap_host          TEXT    NOT NULL,
    imap_port          INTEGER NOT NULL DEFAULT 993,
    imap_security      TEXT    NOT NULL DEFAULT 'ssl',
    imap_username      TEXT    NOT NULL,
    imap_password_enc  TEXT    NOT NULL,
    folder             TEXT    NOT NULL DEFAULT 'INBOX',
    enabled            INTEGER NOT NULL DEFAULT 1,
    initial_days       INTEGER NOT NULL DEFAULT 90,
    recent_days        INTEGER NOT NULL DEFAULT 30,
    last_checked_at    DATETIME,
    last_check_status  TEXT    NOT NULL DEFAULT '',
    last_check_error   TEXT    NOT NULL DEFAULT '',
    last_uidvalidity   INTEGER NOT NULL DEFAULT 0,
    created_at         DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at         DATETIME NOT NULL DEFAULT (datetime('now'))
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS jobs (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    kind          TEXT    NOT NULL,
    mailbox_id    INTEGER REFERENCES mailboxes(id) ON DELETE SET NULL,
    target        TEXT    NOT NULL DEFAULT '',
    status        TEXT    NOT NULL DEFAULT 'queued',
    progress      TEXT    NOT NULL DEFAULT '',
    result        TEXT    NOT NULL DEFAULT '',
    error_message TEXT    NOT NULL DEFAULT '',
    requested_by  TEXT    NOT NULL DEFAULT '',
    created_at    DATETIME NOT NULL DEFAULT (datetime('now')),
    started_at    DATETIME,
    finished_at   DATETIME
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_jobs_status_created ON jobs(status, created_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS jobs;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE IF EXISTS mailboxes;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE IF EXISTS settings;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE IF EXISTS tokens;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE IF EXISTS users;
-- +goose StatementEnd
