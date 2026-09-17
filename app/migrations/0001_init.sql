-- +goose Up
-- Initial schema of the master database (data/mailcare.db).
--
-- Migrations are embedded into the binary and applied automatically when the
-- database is opened (see app/modules/db.go). Add a new file per change,
-- numbered sequentially (0002_<description>.sql, ...); never edit an applied
-- migration. Wrap each statement in StatementBegin/StatementEnd so goose can
-- run multi-line statements safely.
--
-- SQLite ignores declared text sizes, so bounded columns carry CHECK
-- constraints instead. Every DATETIME column holds UTC. Information that is
-- never searched, joined or sorted on is kept in JSON columns (json_valid) so
-- the tables stay narrow. Such columns are always named "detail_info" so the
-- column name never ties a table to one protocol or use: mailboxes.detail_info
-- (mail server connection settings, with a "protocol" key) and jobs.detail_info
-- (progress, result, error).
--
-- Per-mailbox indexes (data/mails/<address>.sqlite) are NOT managed here: their
-- schema lives in app/models/mailindex.go and is rebuilt from the raw files.
--
-- users:     people who log in to the Web UI (optional notification address).
-- tokens:    access tokens (template table, reserved for the future API).
-- settings:  key/value store for settings that differ from the code defaults.
-- mailboxes: monitored mail addresses with their IMAP connection settings.
-- jobs:      background work (sync / fetch / group / analyze / reindex /
--            reclassify / notify) with progress and outcome.

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT     NOT NULL UNIQUE CHECK (length(username) BETWEEN 1 AND 64),
    display_name  TEXT     NOT NULL CHECK (length(display_name) BETWEEN 1 AND 128),
    email         TEXT     NOT NULL DEFAULT '' CHECK (length(email) <= 254),
    language      TEXT     NOT NULL DEFAULT 'ja' CHECK (language IN ('ja', 'en')),
    timezone      TEXT     NOT NULL DEFAULT 'Asia/Tokyo' CHECK (length(timezone) BETWEEN 1 AND 64),
    theme         TEXT     NOT NULL DEFAULT 'auto' CHECK (theme IN ('auto', 'light', 'dark')),
    password_hash TEXT     NOT NULL,
    role          TEXT     NOT NULL DEFAULT 'user' CHECK (role IN ('admin', 'user')),
    created_at    DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at    DATETIME NOT NULL DEFAULT (datetime('now')),
    last_login_at DATETIME
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS tokens (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    identifier  TEXT    NOT NULL UNIQUE,
    name        TEXT    NOT NULL,
    token       TEXT    NOT NULL UNIQUE,
    is_default  INTEGER NOT NULL DEFAULT 0,
    expires_at  DATETIME,
    created_at  DATETIME NOT NULL DEFAULT (datetime('now'))
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS mailboxes (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    address          TEXT     NOT NULL UNIQUE CHECK (length(address) BETWEEN 3 AND 254),
    display_name     TEXT     NOT NULL DEFAULT '' CHECK (length(display_name) <= 128),
    enabled          INTEGER  NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    detail_info          TEXT     NOT NULL CHECK (json_valid(detail_info)),
    last_fetched_at  DATETIME,
    last_fetch_error TEXT     NOT NULL DEFAULT '' CHECK (length(last_fetch_error) <= 2000),
    created_at       DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at       DATETIME NOT NULL DEFAULT (datetime('now'))
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS jobs (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    kind         TEXT     NOT NULL CHECK (kind IN ('sync', 'fetch', 'group', 'analyze', 'reindex', 'reclassify', 'notify')),
    mailbox_id   INTEGER  REFERENCES mailboxes(id) ON DELETE SET NULL,
    target       TEXT     NOT NULL DEFAULT '' CHECK (length(target) <= 320),
    status       TEXT     NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'done', 'error', 'canceled')),
    requested_by TEXT     NOT NULL DEFAULT '' CHECK (length(requested_by) <= 80),
    detail_info      TEXT     NOT NULL DEFAULT '{}' CHECK (json_valid(detail_info)),
    created_at   DATETIME NOT NULL DEFAULT (datetime('now')),
    started_at   DATETIME,
    finished_at  DATETIME
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
