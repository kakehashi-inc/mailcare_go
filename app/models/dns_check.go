package models

import (
	"database/sql"
	"fmt"
	"time"
)

// DNSCheck is a row of dns_checks (master database): the outcome of looking
// up again a name that a notice reported as a failed DNS lookup (design
// document "mail classification" 4.4). Name is lower-case without the
// trailing dot, QType the kind of verification (NAME, dnscheck.VerifyKind:
// whether mail can be delivered to the name, whatever record type the
// notice named; the rows of the earlier verification per record type, MX /
// A / AAAA, are no longer read and go with the daily cleanup), Outcome
// exists / not_found / broken (an inconclusive lookup is not stored),
// Detail the evidence, one line per resolver (the lookups made and their
// answers) and authoritative server, and Direct whether the authoritative
// servers of the name were asked as well (the verification of a notice that
// shows mail from the sending sources) rather than the resolvers alone. The rows are shared
// by every mailbox and kept across a reindex, so every notice about a name
// in the same period gets the same outcome.
type DNSCheck struct {
	ID        int64
	Name      string
	QType     string
	Outcome   string
	Detail    string
	Direct    bool
	CheckedAt time.Time
}

const dnsCheckColumns = `id, name, qtype, outcome, detail, direct, checked_at`

// InsertDNSCheck stores an outcome and sets c.ID. CheckedAt is normalized to
// UTC (the current time when zero).
func InsertDNSCheck(db Execer, c *DNSCheck) error {
	if c.CheckedAt.IsZero() {
		c.CheckedAt = time.Now()
	}
	c.CheckedAt = c.CheckedAt.UTC().Round(0)
	res, err := db.Exec(`INSERT INTO dns_checks (name, qtype, outcome, detail, direct, checked_at) VALUES (?, ?, ?, ?, ?, ?)`,
		c.Name, c.QType, c.Outcome, c.Detail, c.Direct, c.CheckedAt)
	if err != nil {
		return err
	}
	c.ID, err = res.LastInsertId()
	return err
}

// InsertDNSCheckIfAbsent stores an outcome unless the same name and type
// already has one the notice that verified it would use, checked within the
// window [from, to] (both ends included): for a direct row (c.Direct) a
// direct one, for any other row any one (a notice without the
// authoritative check uses either kind, one with it only a direct one, see
// FindDNSCheck). It returns the earliest
// such row of the window: the row stored now (inserted = true, c.ID set) or
// the one stored first by another run verifying the same name at the same
// time (inserted = false, c.ID left 0). The check and the insert are one
// statement, which SQLite runs under its write lock, so concurrent runs
// never store two rows of one kind for one window. CheckedAt is normalized
// as in InsertDNSCheck and must lie within the window (an error otherwise,
// nothing stored): a row outside it would not be found again by the notice
// that stored it.
func InsertDNSCheckIfAbsent(db *sql.DB, c *DNSCheck, from, to time.Time) (*DNSCheck, bool, error) {
	if c.CheckedAt.IsZero() {
		c.CheckedAt = time.Now()
	}
	c.CheckedAt = c.CheckedAt.UTC().Round(0)
	if c.CheckedAt.Before(from) || c.CheckedAt.After(to) {
		return nil, false, fmt.Errorf("checked_at %s is outside the window %s - %s",
			c.CheckedAt.Format(time.RFC3339Nano), from.UTC().Format(time.RFC3339Nano), to.UTC().Format(time.RFC3339Nano))
	}
	// direct >= 1 matches the direct rows for a direct row, every row else.
	res, err := db.Exec(`INSERT INTO dns_checks (name, qtype, outcome, detail, direct, checked_at)
		SELECT ?, ?, ?, ?, ?, ? WHERE NOT EXISTS (
			SELECT 1 FROM dns_checks WHERE name = ? AND qtype = ? AND checked_at >= ? AND checked_at <= ? AND direct >= ?)`,
		c.Name, c.QType, c.Outcome, c.Detail, c.Direct, c.CheckedAt, c.Name, c.QType, from.UTC(), to.UTC(), c.Direct)
	if err != nil {
		return nil, false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, false, err
	}
	inserted := n == 1
	if inserted {
		if c.ID, err = res.LastInsertId(); err != nil {
			return nil, false, err
		}
	}
	winner, err := FindDNSCheck(db, c.Name, c.QType, from, to, c.Direct)
	if err != nil {
		return nil, false, err
	}
	return winner, inserted, nil
}

// FindDNSCheck returns the earliest outcome for the name and type checked
// within the window [from, to], both ends included, of a direct
// verification only when directOnly, else of either kind (sql.ErrNoRows
// when there is none). A live verification passes the fresh period
// (dnscheck.CacheFreshAge before now up to now), the stored-only mode the
// window of the notice (dnscheck.ReuseRange); the earliest row of the
// window wins, so that runs verifying the same name at once agree.
func FindDNSCheck(db *sql.DB, name, qtype string, from, to time.Time, directOnly bool) (*DNSCheck, error) {
	c := &DNSCheck{}
	err := db.QueryRow(`SELECT `+dnsCheckColumns+` FROM dns_checks
		WHERE name = ? AND qtype = ? AND checked_at >= ? AND checked_at <= ? AND direct >= ?
		ORDER BY checked_at ASC, id ASC LIMIT 1`, name, qtype, from.UTC(), to.UTC(), directOnly).
		Scan(&c.ID, &c.Name, &c.QType, &c.Outcome, &c.Detail, &c.Direct, &c.CheckedAt)
	if err != nil {
		return nil, err
	}
	c.CheckedAt = c.CheckedAt.UTC()
	return c, nil
}

// LatestDNSCheck returns the latest outcome for the name and type, whatever
// its age and kind (sql.ErrNoRows when there is none): the stand-in of a
// live verification that gave nothing.
func LatestDNSCheck(db *sql.DB, name, qtype string) (*DNSCheck, error) {
	c := &DNSCheck{}
	err := db.QueryRow(`SELECT `+dnsCheckColumns+` FROM dns_checks
		WHERE name = ? AND qtype = ?
		ORDER BY checked_at DESC, id DESC LIMIT 1`, name, qtype).
		Scan(&c.ID, &c.Name, &c.QType, &c.Outcome, &c.Detail, &c.Direct, &c.CheckedAt)
	if err != nil {
		return nil, err
	}
	c.CheckedAt = c.CheckedAt.UTC()
	return c, nil
}

// DeleteDNSChecksOlderThan removes the outcomes checked before cutoff and
// returns how many rows went (the daily cleanup passes the mail retention).
func DeleteDNSChecksOlderThan(db Execer, cutoff time.Time) (int64, error) {
	res, err := db.Exec(`DELETE FROM dns_checks WHERE checked_at < ?`, cutoff.UTC())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
