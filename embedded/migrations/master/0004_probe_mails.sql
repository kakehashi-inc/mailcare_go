-- +goose Up
-- Delivery probe (system design document 7.6): at each probe send time
-- every enabled mailbox that has an SMTP connection sends one probe mail to
-- the probe server (the probe_* settings), and the arrival check looks for
-- them on the probe server over IMAP. One row per probe mail with the time
-- it was sent, so the delay of every mail is measured separately. A row
-- stays open (pending / delayed / failure) until the mail arrives (arrived, or
-- recovered when it was marked or arrived late) or it is resolved (resolved,
-- with a reason: by a user once delayed or failed, or by the system when the
-- probe, the send check or the mailbox is switched off); send_failed records
-- a mail the SMTP server did not accept, closed by hand as well (recovered
-- if it arrives after all). level keeps the worst level reached (delayed /
-- failure), so a recovered or resolved mail still shows that it was delayed
-- or failed. The daily cleanup removes the rows sent before the mail
-- retention (mail_keep_days, at least 7 days), whatever their status; the
-- rows of a deleted mailbox go with it. The probe mails themselves are
-- deleted from the probe server when their arrival is recorded, and
-- deleted_at is set. One left there (the deletion failed, it arrived after
-- the row was closed) is looked for while deleted_at is NULL: by every
-- check for a row sent in the last day, after that only by the daily sweep
-- (and after a failed deletion) up to 14 days. A copy arriving after
-- deleted_at was set, a probe mail without a row and one of a closed row
-- with another sender are deleted by the rule for unrecorded probe mails
-- (current identifier, a day old, no open row).
--
-- probe_mails.mailbox_id       the mailbox that sent the mail
-- probe_mails.address          its address at the time of sending
-- probe_mails.token            the unique token (16 lower-case hex digits)
--                              in the subject ("[<identifier>:<token>]")
--                              and the X-MailCare-Probe header
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
--                              (INTERNALDATE; the check time when the
--                              server gives none)
-- probe_mails.last_checked_at  the latest arrival check that looked for it
-- probe_mails.error_message    why it could not be sent, or that its token
--                              was found with another sender
-- probe_mails.deleted_at       when the probe mail was deleted from the
--                              probe server (NULL: not deleted, or not
--                              arrived; a row with it set is not looked
--                              for any more)
-- probe_mails.delay_notified_at  when its delay was mailed to the
--                              notification recipients (set with
--                              notified_at as well when it went straight to
--                              failure, so no delay mail follows)
-- probe_mails.notified_at      when its failure (or send failure) was
--                              mailed to the notification recipients
-- probe_mails.resolved_at / resolved_by / resolve_reason / resolve_note
--                              the resolution (resolved_by "" and
--                              resolve_reason probe_disabled: by the system)

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
    deleted_at          DATETIME      ,
    delay_notified_at   DATETIME      ,
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
