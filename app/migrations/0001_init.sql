-- +goose Up
-- Initial schema.
--
-- Migrations are embedded into the binary and applied automatically when the
-- database is opened (see app/modules/db.go). Add a new file per change,
-- numbered sequentially (0002_<description>.sql, ...); never edit an applied
-- migration. Wrap each statement in StatementBegin/StatementEnd so goose can
-- run multi-line statements safely.
--
-- tokens:   bearer tokens for the API (one is the default token).
-- settings: generic key/value store for server settings that override the
--           code defaults (only non-default values are stored).

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

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS settings;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE IF EXISTS tokens;
-- +goose StatementEnd
