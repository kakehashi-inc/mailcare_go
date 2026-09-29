-- +goose Up
-- The auto-reply rule is split in three by the evidence it matched on:
-- "Auto-Submitted: auto-replied" keeps "auto_reply", an out-of-office
-- subject alone becomes "auto_reply_subject" and "Auto-Submitted:
-- auto-generated" alone becomes "auto_generated". The server retention
-- deletes only "auto_reply" of the three from the IMAP server. Rows
-- classified before the split cannot tell them apart, so they are handed
-- back to the grouping phase (classified = 0), which classifies them again
-- on the next sync or group run. Until then they are no server deletion
-- candidates (only classified rows are).

-- +goose StatementBegin
UPDATE messages SET classified = 0 WHERE bounce_kind = 'auto_reply';
-- +goose StatementEnd

-- +goose Down
-- Nothing to undo: the rows are classified again by the grouping phase.
