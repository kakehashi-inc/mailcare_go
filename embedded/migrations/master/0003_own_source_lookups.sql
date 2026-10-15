-- +goose Up
-- The addresses of our own sending servers that the grouping looks up
-- (design document "mail classification" 2.6 and 4.4), one row per source
-- shared by every mailbox: the A and AAAA addresses of the SMTP host of a
-- monitored mail address (kind smtp_host) or of a host name of the setting
-- own_sending_hosts (kind global_host), and what the SPF record of the
-- domain of a monitored mail address lists (kind spf: addresses and the
-- host names of its a and mx terms). Every source is compared for every
-- mailbox; whether the backscatter rule applies is still decided per
-- mailbox (a host name of its own or of the setting), never by a row here.
-- A row written after a complete lookup is used without asking again for 1
-- hour, by every mailbox and run (a row dated after now is never used that
-- way), and stands in for a lookup that fails for 14 days. A complete
-- lookup that found nothing does not replace a row that lists something and
-- is at most 14 days old. The grouping deletes the rows no current source
-- refers to, the daily cleanup the rows older than 90 days and those dated
-- more than 1 day in the future (the clock was set back); until then a row
-- dated in the future may still stand in for a failed lookup.
--
-- own_source_lookups.kind        smtp_host / spf / global_host
-- own_source_lookups.name        the SMTP host, the domain or the host name
--                                of the setting (lower-case, no trailing dot)
-- own_source_lookups.addresses   the addresses and CIDR prefixes found, one
--                                per line (canonical text)
-- own_source_lookups.hosts       the host names found (spf only), one per
--                                line
-- own_source_lookups.fetched_at  when the lookup was completed

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS own_source_lookups (
    id                  INTEGER       PRIMARY KEY AUTOINCREMENT,
    kind                VARCHAR(16)   NOT NULL,
    name                VARCHAR(253)  NOT NULL,
    addresses           TEXT          NOT NULL,
    hosts               TEXT          NOT NULL,
    fetched_at          DATETIME      NOT NULL,
    UNIQUE (kind, name)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS own_source_lookups;
-- +goose StatementEnd
