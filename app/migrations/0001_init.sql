-- +goose Up
-- Initial schema of the master database (data/mailcare.db).
--
-- Migrations are embedded into the binary and applied automatically when the
-- database is opened (see app/modules/db.go). Add a new file per change,
-- numbered sequentially (0002_<description>.sql, ...); never edit an applied
-- migration. Wrap each statement in StatementBegin/StatementEnd so goose can
-- run multi-line statements safely.
--
-- Design rules: no column has a default value (the application writes every
-- column); text whose length is known is declared VARCHAR(n) and text of
-- unpredictable length TEXT (SQLite does not enforce the declared length; the
-- length of user input is checked where it is entered); enumerations are kept
-- by the application constants, not by constraints. The only JSON column is mailboxes.detail_info
-- (mail server connection settings, with a "protocol" key); the name never
-- ties the column to one protocol.
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
    id                  INTEGER       PRIMARY KEY AUTOINCREMENT,
    username            VARCHAR(64)   NOT NULL UNIQUE,
    display_name        VARCHAR(128)  NOT NULL,
    email               VARCHAR(254)  NOT NULL,
    language            VARCHAR(16)   NOT NULL,
    timezone            VARCHAR(64)   NOT NULL,
    theme               VARCHAR(32)   NOT NULL,
    password_hash       VARCHAR(60)   NOT NULL,
    role                VARCHAR(32)   NOT NULL,
    created_at          DATETIME      NOT NULL,
    updated_at          DATETIME      NOT NULL,
    last_login_at       DATETIME
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS tokens (
    id                  INTEGER       PRIMARY KEY AUTOINCREMENT,
    identifier          TEXT          NOT NULL UNIQUE,
    name                TEXT          NOT NULL,
    token               TEXT          NOT NULL UNIQUE,
    is_default          INTEGER       NOT NULL,
    expires_at          DATETIME,
    created_at          DATETIME      NOT NULL
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS settings (
    key                 VARCHAR(64)   NOT NULL PRIMARY KEY,
    value               TEXT          NOT NULL
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS mailboxes (
    id                  INTEGER       PRIMARY KEY AUTOINCREMENT,
    address             VARCHAR(254)  NOT NULL UNIQUE,
    display_name        VARCHAR(128)  NOT NULL,
    enabled             INTEGER       NOT NULL,
    detail_info         TEXT          NOT NULL,
    last_fetched_at     DATETIME      ,
    last_fetch_error    TEXT          NOT NULL,
    created_at          DATETIME      NOT NULL,
    updated_at          DATETIME      NOT NULL
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS jobs (
    id                  INTEGER       PRIMARY KEY AUTOINCREMENT,
    kind                VARCHAR(32)   NOT NULL,
    mailbox_id          INTEGER       REFERENCES mailboxes(id) ON DELETE SET NULL,
    parent_id           INTEGER       REFERENCES jobs(id) ON DELETE SET NULL,
    target              VARCHAR(320)  NOT NULL,
    status              VARCHAR(32)   NOT NULL,
    requested_by        VARCHAR(80)   NOT NULL,
    progress            TEXT          NOT NULL,
    result              TEXT          NOT NULL,
    error_message       TEXT          NOT NULL,
    created_at          DATETIME      NOT NULL,
    started_at          DATETIME      ,
    finished_at         DATETIME
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
