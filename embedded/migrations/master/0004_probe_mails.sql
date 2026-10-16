-- +goose Up
-- Delivery probe (system design document 7.6): at each probe send time
-- every enabled mailbox that has an SMTP connection sends one probe mail to
-- the probe server (the probe_* settings), and the arrival check looks for
-- them on the probe server over IMAP. One row per probe mail: its own send
-- time, so the delay of every mail is measured on its own. A row stays open
-- (pending / delayed / failure) until the mail arrives (arrived, or
-- recovered when it was marked or arrived late) or a user resolves it by
-- hand (resolved, with a reason); send_failed records a mail the SMTP server
-- did not accept and is closed by hand as well. level keeps the worst level
-- reached (delayed / failure), so a recovered or resolved mail still shows
-- that it was delayed or failed. The daily cleanup removes the rows sent
-- before the mail retention (mail_keep_days), whatever their status; the
-- rows of a deleted mailbox go with it. The probe mails themselves are
-- deleted from the probe server when their arrival is recorded (when that
-- failed, by the first arrival check after a send a day later or more).
--
-- probe_mails.mailbox_id       the mailbox that sent the mail
-- probe_mails.address          its address at the time of sending
-- probe_mails.token            the unique token in the subject and the
--                              X-MailCare-Probe header (MCPROBE-<hex>)
-- probe_mails.slot             the send time the mail belongs to
--                              ("YYYY-MM-DD HH:MM", local time of the
--                              server, as in the subject; for a mail sent
--                              by hand the time it was sent)
-- probe_mails.manual           1 = sent by hand (not at a send time)
-- probe_mails.subject          the subject sent
-- probe_mails.sent_at          when the SMTP server accepted the mail (or
--                              refused it)
-- probe_mails.status           pending / delayed / failure / send_failed /
--                              arrived / recovered / resolved
-- probe_mails.level            "" / delayed / failure: the worst level
-- probe_mails.delay_seconds    arrival minus sending once arrived, else the
--                              time without arrival at the latest check
-- probe_mails.arrived_at       when the probe server received it
--                              (INTERNALDATE)
-- probe_mails.last_checked_at  the latest arrival check that looked for it
-- probe_mails.error_message    why it could not be sent
-- probe_mails.notified_at      when its failure was mailed to the
--                              notification recipients
-- probe_mails.resolved_at / resolved_by / resolve_reason / resolve_note
--                              the manual resolution

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS probe_mails (
    id                  INTEGER       PRIMARY KEY AUTOINCREMENT,
    mailbox_id          INTEGER       NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
    address             VARCHAR(254)  NOT NULL,
    token               VARCHAR(64)   NOT NULL UNIQUE,
    slot                VARCHAR(16)   NOT NULL,
    manual              INTEGER       NOT NULL,
    subject             TEXT          NOT NULL,
    sent_at             DATETIME      NOT NULL,
    status              VARCHAR(16)   NOT NULL,
    level               VARCHAR(16)   NOT NULL,
    delay_seconds       INTEGER       NOT NULL,
    arrived_at          DATETIME      ,
    last_checked_at     DATETIME      ,
    error_message       TEXT          NOT NULL,
    notified_at         DATETIME      ,
    resolved_at         DATETIME      ,
    resolved_by         VARCHAR(80)   NOT NULL,
    resolve_reason      VARCHAR(32)   NOT NULL,
    resolve_note        TEXT          NOT NULL,
    created_at          DATETIME      NOT NULL,
    updated_at          DATETIME      NOT NULL
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_probe_mails_status ON probe_mails(status, sent_at);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_probe_mails_mailbox ON probe_mails(mailbox_id, sent_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_probe_mails_mailbox;
-- +goose StatementEnd

-- +goose StatementBegin
DROP INDEX IF EXISTS idx_probe_mails_status;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE IF EXISTS probe_mails;
-- +goose StatementEnd
