-- +goose Up
-- A delivery status notification can report several recipients, each in a
-- per-recipient block with an action, a status and a diagnostic. The
-- bounces table held one row per message (sharing the id of its messages
-- row), so every block after the first was not judged. It becomes one row
-- per recipient block: a separate id, the message it belongs to (message_id,
-- deleted with it) and the position of the block among the blocks taken
-- (recipient_index, 0 for the first). The existing rows keep their details
-- as the first block of their message (recipient_index 0); the other
-- blocks of messages grouped before are judged at the next Reclassify.
--
-- bounces.message_id    the message the block belongs to
-- bounces.recipient_index  the position of the block (0 = the first; the
--                       group of the message shown with it is the group of
--                       its first block)

-- +goose StatementBegin
CREATE TABLE bounces_by_recipient (
    id                  INTEGER       PRIMARY KEY AUTOINCREMENT,
    message_id          INTEGER       NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    recipient_index     INTEGER       NOT NULL,
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
INSERT INTO bounces_by_recipient (message_id, recipient_index, group_key, recipient, recipient_domain, action, status_code,
    smtp_code, diagnostic, diagnostic_template, diagnostic_source, category_rule, pattern_key, remote_mta, remote_ip,
    reporting_mta, original_message_id, original_subject, original_from, original_date)
SELECT id, 0, group_key, recipient, recipient_domain, action, status_code, smtp_code, diagnostic, diagnostic_template,
    diagnostic_source, category_rule, pattern_key, remote_mta, remote_ip, reporting_mta, original_message_id,
    original_subject, original_from, original_date
FROM bounces ORDER BY id;
-- +goose StatementEnd

-- +goose StatementBegin
DROP INDEX IF EXISTS idx_bounces_group;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE bounces;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE bounces_by_recipient RENAME TO bounces;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE UNIQUE INDEX IF NOT EXISTS idx_bounces_message ON bounces(message_id, recipient_index);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_bounces_group ON bounces(group_key, pattern_key);
-- +goose StatementEnd

-- +goose Down
-- Back to one row per message: the first block of every message stays.

-- +goose StatementBegin
CREATE TABLE bounces_by_message (
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
INSERT INTO bounces_by_message (id, group_key, recipient, recipient_domain, action, status_code, smtp_code, diagnostic,
    diagnostic_template, diagnostic_source, category_rule, pattern_key, remote_mta, remote_ip, reporting_mta,
    original_message_id, original_subject, original_from, original_date)
SELECT message_id, group_key, recipient, recipient_domain, action, status_code, smtp_code, diagnostic,
    diagnostic_template, diagnostic_source, category_rule, pattern_key, remote_mta, remote_ip, reporting_mta,
    original_message_id, original_subject, original_from, original_date
FROM bounces WHERE recipient_index = 0;
-- +goose StatementEnd

-- +goose StatementBegin
DROP INDEX IF EXISTS idx_bounces_group;
-- +goose StatementEnd

-- +goose StatementBegin
DROP INDEX IF EXISTS idx_bounces_message;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE bounces;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE bounces_by_message RENAME TO bounces;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_bounces_group ON bounces(group_key, pattern_key);
-- +goose StatementEnd
