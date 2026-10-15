package models

import (
	"database/sql"
	"strings"
	"time"
)

// The kinds of an own_source_lookups row.
const (
	OwnSourceKindSMTPHost   = "smtp_host"   // the SMTP host of a mailbox: its addresses
	OwnSourceKindSPF        = "spf"         // the domain of a mailbox: what its SPF record lists
	OwnSourceKindGlobalHost = "global_host" // a host name of own_sending_hosts: its addresses
)

// OwnSourceLookup is a row of own_source_lookups (master database): the
// last complete lookup of one source of our own sending servers (design
// document "mail classification" 2.6 and 4.4), shared by every mailbox and
// run. Name is the SMTP host, the domain or the host name of the setting
// (lower-case, no trailing dot), Addresses the addresses and CIDR prefixes
// found (canonical text) and Hosts the host names found (the a and mx terms
// of an SPF record; empty for the other kinds). Both lists are stored one
// entry per line.
type OwnSourceLookup struct {
	ID        int64
	Kind      string
	Name      string
	Addresses []string
	Hosts     []string
	FetchedAt time.Time
}

const ownSourceLookupColumns = `id, kind, name, addresses, hosts, fetched_at`

// ReplaceOwnSourceLookup stores the row of a source in place of the one
// stored before (the same kind and name). FetchedAt is normalized to UTC
// (the current time when zero).
func ReplaceOwnSourceLookup(db Execer, s *OwnSourceLookup) error {
	if s.FetchedAt.IsZero() {
		s.FetchedAt = time.Now()
	}
	s.FetchedAt = s.FetchedAt.UTC().Round(0)
	_, err := db.Exec(`INSERT INTO own_source_lookups (kind, name, addresses, hosts, fetched_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (kind, name) DO UPDATE SET addresses = excluded.addresses, hosts = excluded.hosts, fetched_at = excluded.fetched_at`,
		s.Kind, s.Name, strings.Join(s.Addresses, "\n"), strings.Join(s.Hosts, "\n"), s.FetchedAt)
	return err
}

// GetOwnSourceLookup returns the row of a source (sql.ErrNoRows when there
// is none).
func GetOwnSourceLookup(db *sql.DB, kind, name string) (*OwnSourceLookup, error) {
	return scanOwnSourceLookup(db.QueryRow(`SELECT `+ownSourceLookupColumns+` FROM own_source_lookups
		WHERE kind = ? AND name = ?`, kind, name))
}

// ListOwnSourceLookups returns every row, oldest id first.
func ListOwnSourceLookups(db *sql.DB) ([]*OwnSourceLookup, error) {
	rows, err := db.Query(`SELECT ` + ownSourceLookupColumns + ` FROM own_source_lookups ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*OwnSourceLookup
	for rows.Next() {
		s, err := scanOwnSourceLookup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// DeleteOwnSourceLookup removes one row.
func DeleteOwnSourceLookup(db Execer, id int64) error {
	_, err := db.Exec(`DELETE FROM own_source_lookups WHERE id = ?`, id)
	return err
}

// DeleteOwnSourceLookupsOlderThan removes the rows fetched before cutoff and
// returns how many went (the daily cleanup).
func DeleteOwnSourceLookupsOlderThan(db Execer, cutoff time.Time) (int64, error) {
	res, err := db.Exec(`DELETE FROM own_source_lookups WHERE fetched_at < ?`, cutoff.UTC())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteOwnSourceLookupsNewerThan removes the rows dated after cutoff (the
// clock was set back since they were written) and returns how many went
// (the daily cleanup).
func DeleteOwnSourceLookupsNewerThan(db Execer, cutoff time.Time) (int64, error) {
	res, err := db.Exec(`DELETE FROM own_source_lookups WHERE fetched_at > ?`, cutoff.UTC())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func scanOwnSourceLookup(s rowScanner) (*OwnSourceLookup, error) {
	r := &OwnSourceLookup{}
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
