-- +goose Up
-- A notice such as "Name service error for name=example.jp type=MX: Host not
-- found, try again" cannot tell by itself whether the recipient domain does
-- not exist or the resolver of the reporting server failed, so the name is
-- looked up again (design document "mail classification" 4.4) and the
-- outcome decides the category of the notice. The outcomes are kept here, in
-- the master database, so that they are shared by every mailbox and survive
-- a reindex: a notice uses the earliest outcome checked from 24 hours before
-- it was received to 48 hours after (both ends included; a notice older than
-- 48 hours is never looked up live, so a later lookup is no evidence for
-- it). Once a notice has used an outcome, a regroup uses the same row, so a
-- later change of the DNS of the name does not move it. A regroup can still
-- move it to another group: when it was not verified before and a row now
-- falls within its window, when whether its reporting MTA is a sending
-- server or whether it shows mail from the sending sources is judged
-- differently (the sending sources changed, or their addresses could not
-- all be looked up in the run), and when it shows such mail and used a row of the
-- resolvers alone until a row of the authoritative check was stored within
-- its window (see direct below). An inconclusive lookup is not stored. The daily cleanup removes the rows checked before
-- mail_keep_days plus the 24-hour window before a notice: by then every
-- notice that could use them is past the mail retention.
--
-- dns_checks.name        the name looked up (lower-case, no trailing dot)
-- dns_checks.qtype       the record type: MX / A / AAAA
-- dns_checks.outcome     exists / not_found / broken
-- dns_checks.detail      the evidence, one line per resolver and
--                        authoritative server
-- dns_checks.direct      1 when the authoritative servers of the name were
--                        asked as well (the verification of a notice that
--                        shows mail from the sending sources), 0 for the
--                        resolvers alone. A notice that shows such mail
--                        uses a row with 1
--                        (it is verified live with them when there is
--                        none), one with 0 only when that cannot be done
--                        or gives no outcome (too old for a live check,
--                        the check off, the limits of the run, an
--                        inconclusive lookup); any other notice uses either
--                        kind.
-- dns_checks.checked_at  when the name was looked up
--
-- idx_dns_checks_lookup serves the lookup of the earliest outcome of a name
-- and type within the window of a notice.

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS dns_checks (
    id                  INTEGER       PRIMARY KEY AUTOINCREMENT,
    name                VARCHAR(253)  NOT NULL,
    qtype               VARCHAR(8)    NOT NULL,
    outcome             VARCHAR(16)   NOT NULL,
    detail              TEXT          NOT NULL,
    direct              INTEGER       NOT NULL,
    checked_at          DATETIME      NOT NULL
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_dns_checks_lookup ON dns_checks(name, qtype, checked_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_dns_checks_lookup;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE IF EXISTS dns_checks;
-- +goose StatementEnd
