-- +goose Up
-- DMARC aggregate reports become notices of their own kind (bounce kind
-- "report", rule "dmarc_report") and every record of a report that failed
-- DMARC is filed into a group like a bounce. A report can hold several
-- failing records that belong to different groups, so the records get a
-- table of their own instead of the 1:1 bounces row (dmarc_records, one row
-- per failing record, deleted with its message). The group counters, the
-- pattern coverage and the empty-group cleanup read the members of a group
-- from both tables, each filtered by the group key.
--
-- The classification rules changed as well (DMARC reports; an out-of-office
-- subject together with a null Return-Path or Auto-Submitted is a certain
-- auto-reply; junk). Rows that carry no bounce details (ordinary mail,
-- auto-replies, other daemon mail) are handed back to the grouping phase
-- (classified = 0), which classifies them again on the next sync or group
-- run. Failed / delayed notices keep their bounce details and groups: the
-- junk rules now run before the subject, display-name and body rules, so a
-- mail those rules took for a bounce can be junk under the new rules, and
-- only a reclassification (a full regroup, which rebuilds every group)
-- applies that.

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS dmarc_records (
    id                  INTEGER       PRIMARY KEY AUTOINCREMENT,
    message_id          INTEGER       NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    group_key           VARCHAR(16)   NOT NULL,
    category_rule       VARCHAR(64)   NOT NULL,
    diagnostic_template TEXT          NOT NULL,
    pattern_key         VARCHAR(16)   NOT NULL,
    report_org          TEXT          NOT NULL,
    report_id           TEXT          NOT NULL,
    begin_at            DATETIME      ,
    end_at              DATETIME      ,
    policy_domain       TEXT          NOT NULL,
    policy              VARCHAR(32)   NOT NULL,
    header_from         TEXT          NOT NULL,
    envelope_from       TEXT          NOT NULL,
    source_ip           VARCHAR(45)   NOT NULL,
    message_count       INTEGER       NOT NULL,
    disposition         VARCHAR(32)   NOT NULL,
    dkim_result         VARCHAR(32)   NOT NULL,
    spf_result          VARCHAR(32)   NOT NULL,
    dkim_auth           TEXT          NOT NULL,
    spf_auth            TEXT          NOT NULL
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_dmarc_records_message ON dmarc_records(message_id);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_dmarc_records_group ON dmarc_records(group_key, pattern_key);
-- +goose StatementEnd

-- +goose StatementBegin
UPDATE messages SET classified = 0 WHERE bounce_kind NOT IN ('failed', 'delayed');
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS dmarc_records;
-- +goose StatementEnd
