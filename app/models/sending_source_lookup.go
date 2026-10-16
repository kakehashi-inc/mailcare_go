package models

import (
	"database/sql"
	"strings"
	"time"
)

// The kinds of a sending_source_lookups row.
const (
	SendingSourceKindSMTPHost   = "smtp_host"   // the SMTP host of a mailbox: its addresses
	SendingSourceKindSPF        = "spf"         // the domain of a mailbox: what its SPF record lists
	SendingSourceKindGlobalHost = "global_host" // a host name of sending_source_hosts: its addresses
)

// SendingSourceLookup is a row of sending_source_lookups (master database): the
// last complete lookup of one source of the sending sources (design
// document "mail classification" 2.6 and 4.4), shared by every mailbox and
// run. Name is the SMTP host, the domain or the host name of the setting
// (lower-case, no trailing dot), Addresses the addresses and CIDR prefixes
// found (canonical text) and Hosts the host names found (the a and mx terms
// of an SPF record; empty for the other kinds). Both lists are stored one
// entry per line.
type SendingSourceLookup struct {
	ID        int64
	Kind      string
	Name      string
	Addresses []string
	Hosts     []string
	FetchedAt time.Time
}

const sendingSourceLookupColumns = `id, kind, name, addresses, hosts, fetched_at`

// ReplaceSendingSourceLookup stores the row of a source in place of the one
// stored before (the same kind and name), unless the new row lists nothing
// (no address, no host name) while the stored one lists something and was
// fetched at keepSince or later (a row dated after now included): an empty
// answer never replaces a recent non-empty one. The zero keepSince replaces
// any row. The condition is part of the statement (the update of the upsert
// is conditional), so that a run storing an empty answer cannot replace the
// row another run stored a moment before. written reports whether the row
// was stored. FetchedAt is normalized to UTC (the current time when zero).
func ReplaceSendingSourceLookup(db Execer, s *SendingSourceLookup, keepSince time.Time) (written bool, err error) {
	if s.FetchedAt.IsZero() {
		s.FetchedAt = time.Now()
	}
	s.FetchedAt = s.FetchedAt.UTC().Round(0)
	query := `INSERT INTO sending_source_lookups (kind, name, addresses, hosts, fetched_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (kind, name) DO UPDATE SET addresses = excluded.addresses, hosts = excluded.hosts, fetched_at = excluded.fetched_at`
	args := []any{s.Kind, s.Name, strings.Join(s.Addresses, "\n"), strings.Join(s.Hosts, "\n"), s.FetchedAt}
	if !keepSince.IsZero() {
		query += `
		WHERE NOT (excluded.addresses = '' AND excluded.hosts = ''
			AND (sending_source_lookups.addresses <> '' OR sending_source_lookups.hosts <> '')
			AND sending_source_lookups.fetched_at >= ?)`
		args = append(args, keepSince.UTC().Round(0))
	}
	res, err := db.Exec(query, args...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// GetSendingSourceLookup returns the row of a source (sql.ErrNoRows when there
// is none).
func GetSendingSourceLookup(db *sql.DB, kind, name string) (*SendingSourceLookup, error) {
	return scanSendingSourceLookup(db.QueryRow(`SELECT `+sendingSourceLookupColumns+` FROM sending_source_lookups
		WHERE kind = ? AND name = ?`, kind, name))
}

// ListSendingSourceLookups returns every row, oldest id first.
func ListSendingSourceLookups(db *sql.DB) ([]*SendingSourceLookup, error) {
	rows, err := db.Query(`SELECT ` + sendingSourceLookupColumns + ` FROM sending_source_lookups ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*SendingSourceLookup
	for rows.Next() {
		s, err := scanSendingSourceLookup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// DeleteSendingSourceLookup removes one row.
func DeleteSendingSourceLookup(db Execer, id int64) error {
	_, err := db.Exec(`DELETE FROM sending_source_lookups WHERE id = ?`, id)
	return err
}

// DeleteSendingSourceLookupsOlderThan removes the rows fetched before cutoff and
// returns how many went (the daily cleanup).
func DeleteSendingSourceLookupsOlderThan(db Execer, cutoff time.Time) (int64, error) {
	res, err := db.Exec(`DELETE FROM sending_source_lookups WHERE fetched_at < ?`, cutoff.UTC())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteSendingSourceLookupsNewerThan removes the rows dated after cutoff (the
// clock was set back since they were written) and returns how many went
// (the daily cleanup).
func DeleteSendingSourceLookupsNewerThan(db Execer, cutoff time.Time) (int64, error) {
	res, err := db.Exec(`DELETE FROM sending_source_lookups WHERE fetched_at > ?`, cutoff.UTC())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func scanSendingSourceLookup(s rowScanner) (*SendingSourceLookup, error) {
	r := &SendingSourceLookup{}
	var addresses, hosts string
	if err := s.Scan(&r.ID, &r.Kind, &r.Name, &addresses, &hosts, &r.FetchedAt); err != nil {
		return nil, err
	}
	r.Addresses, r.Hosts = splitLines(addresses), splitLines(hosts)
	r.FetchedAt = r.FetchedAt.UTC()
	return r, nil
}

// splitLines returns the non-empty lines of a stored list (nil for "").
func splitLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}
