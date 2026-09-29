-- +goose Up
-- A user who marks a group resolved or ignored chooses what was done or why
-- (a code) and may add a note; every state change is kept as history; and a
-- resolved or ignored group that later receives a notice showing the matter
-- is not settled goes back to the users for a new decision (re-check).
--
-- groups.state_reason   code chosen with the current state (NULL while open)
-- groups.state_note     note written with the current state (NULL when none)
-- groups.needs_recheck  1 while a resolved / ignored group waits for a new
--                       decision; cleared by the next state change (a NOT
--                       NULL column added to an existing table needs a
--                       default, see 0001_init.sql)
-- group_state_changes          every state change of a group (the history)
-- group_state_change_patterns  re-check keys of the members at a change
--
-- The groups that are already resolved or ignored get a history row with
-- their current state and change time (no code, no note, no user). Their
-- re-check keys are computed by the application (mailengine), which fills
-- them when the index is next opened. Their analysis flag is cleared: a
-- resolved or ignored group is analyzed only after it is sent back for a
-- re-check, and reopening it flags it again when its latest report does not
-- cover every pattern.

-- +goose StatementBegin
ALTER TABLE groups ADD COLUMN state_reason VARCHAR(32);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE groups ADD COLUMN state_note TEXT;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE groups ADD COLUMN needs_recheck INTEGER NOT NULL DEFAULT 0;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS group_state_changes (
    id                  INTEGER       PRIMARY KEY AUTOINCREMENT,
    group_key           VARCHAR(16)   NOT NULL REFERENCES groups(group_key) ON DELETE CASCADE,
    state               VARCHAR(32)   NOT NULL,
    reason              VARCHAR(32)   ,
    note                TEXT          ,
    changed_by          VARCHAR(64)   NOT NULL,
    changed_at          DATETIME      NOT NULL
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_group_state_changes_group ON group_state_changes(group_key, id);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS group_state_change_patterns (
    change_id           INTEGER       NOT NULL REFERENCES group_state_changes(id) ON DELETE CASCADE,
    pattern_key         VARCHAR(16)   NOT NULL,
    PRIMARY KEY (change_id, pattern_key)
);
-- +goose StatementEnd

-- +goose StatementBegin
INSERT INTO group_state_changes (group_key, state, reason, note, changed_by, changed_at)
SELECT group_key, state, NULL, NULL, '', COALESCE(state_updated_at, updated_at)
  FROM groups WHERE state <> 'open' ORDER BY group_key;
-- +goose StatementEnd

-- +goose StatementBegin
UPDATE groups SET needs_analysis = 0 WHERE state <> 'open';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS group_state_change_patterns;
-- +goose StatementEnd

-- +goose StatementBegin
DROP INDEX IF EXISTS idx_group_state_changes_group;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE IF EXISTS group_state_changes;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE groups DROP COLUMN needs_recheck;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE groups DROP COLUMN state_note;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE groups DROP COLUMN state_reason;
-- +goose StatementEnd
