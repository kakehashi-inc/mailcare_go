-- +goose Up
-- Initial schema of a per-mailbox index (data/mails/<address>.sqlite).
--
-- The index holds everything MailCare knows about the fetched mail of one
-- address: the parsed headers and body layout of every message, the bounce
-- details extracted from the daemon notices, the groups those bounces are
-- bundled into and the reports the agent produced. Apart from the group
-- states and the reports, everything can be rebuilt from the raw .eml files
-- (reindex); the migrations only shape the tables. Add a new file per change,
-- numbered sequentially within this directory; never edit an applied one.
--
-- Design rules: every attribute is a real column (no JSON column) and no
-- column has a default value; text whose length is known is declared
-- VARCHAR(n) and text of unpredictable length TEXT; enumerations are kept by
-- the application constants, not by constraints; every DATETIME column holds
-- UTC. Exception: SQLite can add a NOT NULL column to an existing table only
-- with a DEFAULT, so a later migration that adds one gives it the default the
-- existing rows need (the application still writes every column); columns a
-- re-run of the classification must fill are filled by "reclassify".
--
-- The statements use IF NOT EXISTS so that an index created before the
-- migrations were introduced (schema version 1 stamped in PRAGMA
-- user_version, same tables) is adopted as it is.
--
-- messages              every fetched mail (headers, body layout, detection outcome, server deletion)
-- bounces               details extracted from a bounce message (1:1 with its message row)
-- groups                bounces bundled by the unit an administrator acts on
-- agent_reports         analysis produced by an agent CLI
-- agent_report_patterns bounce patterns a completed report covered

-- +goose StatementBegin
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
    server_deleted_at   DATETIME      ,
    UNIQUE (folder, uidvalidity, uid)
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_messages_date ON messages(date);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_messages_message_id ON messages(message_id);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_messages_bounce ON messages(is_bounce, date);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_messages_classified ON messages(classified);
-- +goose StatementEnd

-- +goose StatementBegin
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
    diagnostic_source   VARCHAR(16)   NOT NULL,
    category_rule       VARCHAR(64)   NOT NULL,
    pattern_key         VARCHAR(16)   NOT NULL,
    remote_mta          TEXT          NOT NULL,
    remote_ip           VARCHAR(45)   NOT NULL,
    reporting_mta       TEXT          NOT NULL,
    original_message_id TEXT          NOT NULL,
    original_subject    TEXT          NOT NULL,
    original_from       TEXT          NOT NULL,
    original_date       DATETIME
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_bounces_group ON bounces(group_key, pattern_key);
-- +goose StatementEnd

-- +goose StatementBegin
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
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_groups_state ON groups(actionable, state, last_seen);
-- +goose StatementEnd

-- +goose StatementBegin
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
    confidence          VARCHAR(16)   NOT NULL,
    message_count       INTEGER       NOT NULL,
    model               TEXT          NOT NULL,
    reasoning_effort    VARCHAR(16)   NOT NULL,
    tokens_used         INTEGER       ,
    command_count       INTEGER       ,
    started_at          DATETIME      ,
    finished_at         DATETIME      ,
    created_at          DATETIME      NOT NULL
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_agent_reports_group ON agent_reports(group_key, id);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS agent_report_patterns (
    report_id           INTEGER       NOT NULL REFERENCES agent_reports(id) ON DELETE CASCADE,
    pattern_key         VARCHAR(16)   NOT NULL,
    PRIMARY KEY (report_id, pattern_key)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS agent_report_patterns;
-- +goose StatementEnd

-- +goose StatementBegin
DROP INDEX IF EXISTS idx_agent_reports_group;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE IF EXISTS agent_reports;
-- +goose StatementEnd

-- +goose StatementBegin
DROP INDEX IF EXISTS idx_groups_state;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE IF EXISTS groups;
-- +goose StatementEnd

-- +goose StatementBegin
DROP INDEX IF EXISTS idx_bounces_group;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE IF EXISTS bounces;
-- +goose StatementEnd

-- +goose StatementBegin
DROP INDEX IF EXISTS idx_messages_classified;
-- +goose StatementEnd

-- +goose StatementBegin
DROP INDEX IF EXISTS idx_messages_bounce;
-- +goose StatementEnd

-- +goose StatementBegin
DROP INDEX IF EXISTS idx_messages_message_id;
-- +goose StatementEnd

-- +goose StatementBegin
DROP INDEX IF EXISTS idx_messages_date;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE IF EXISTS messages;
-- +goose StatementEnd
