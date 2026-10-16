package models

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pressly/goose/v3"

	"mailcare/app/modules/dbschema"
)

// openTestMasterDB opens a fresh master database the way OpenDB does (same
// DSN, migrations applied); package modules cannot be imported here.
func openTestMasterDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mailcare.db")
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_time_format=sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := dbschema.Apply(context.Background(), db, dbschema.Master()); err != nil {
		t.Fatal(err)
	}
	return db
}

// farFuture is an upper bound of the window past every row of the tests
// that are about its start only.
var farFuture = time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)

// TestDNSCheckFindEarliest checks the reuse rule: the earliest outcome of
// the same name and type checked at or after the given time, a row exactly
// at that time included, and nothing for another type or an older row only.
func TestDNSCheckFindEarliest(t *testing.T) {
	db := openTestMasterDB(t)
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	rows := []*DNSCheck{
		{Name: "example.jp", QType: "MX", Outcome: "not_found", Detail: "old", CheckedAt: base.Add(-48 * time.Hour)},
		{Name: "example.jp", QType: "MX", Outcome: "broken", Detail: "later", CheckedAt: base.Add(5 * time.Hour)},
		{Name: "example.jp", QType: "MX", Outcome: "exists", Detail: "at", CheckedAt: base},
		{Name: "example.jp", QType: "A", Outcome: "exists", Detail: "other type", CheckedAt: base.Add(time.Hour)},
		// A fractional second sorts after the whole second.
		{Name: "frac.example.jp", QType: "MX", Outcome: "exists", Detail: "frac", CheckedAt: base.Add(1500 * time.Millisecond)},
		{Name: "frac.example.jp", QType: "MX", Outcome: "broken", Detail: "whole", CheckedAt: base.Add(2 * time.Second)},
	}
	for _, r := range rows {
		if err := InsertDNSCheck(db, r); err != nil {
			t.Fatal(err)
		}
		if r.ID == 0 {
			t.Fatalf("no id for %+v", r)
		}
	}

	got, err := FindDNSCheck(db, "example.jp", "MX", base, farFuture, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Detail != "at" || got.Outcome != "exists" || !got.CheckedAt.Equal(base) || got.CheckedAt.Location() != time.UTC {
		t.Errorf("at notBefore: %+v", got)
	}
	if got, err := FindDNSCheck(db, "example.jp", "MX", base.Add(time.Second), farFuture, false); err != nil || got.Detail != "later" {
		t.Errorf("after notBefore: %+v, %v", got, err)
	}
	if got, err := FindDNSCheck(db, "example.jp", "MX", base.Add(-72*time.Hour), farFuture, false); err != nil || got.Detail != "old" {
		t.Errorf("earliest: %+v, %v", got, err)
	}
	// A time zone other than UTC is the same instant.
	jst := time.FixedZone("JST", 9*3600)
	if got, err := FindDNSCheck(db, "example.jp", "MX", base.In(jst), farFuture, false); err != nil || got.Detail != "at" {
		t.Errorf("JST notBefore: %+v, %v", got, err)
	}
	if got, err := FindDNSCheck(db, "frac.example.jp", "MX", base.Add(time.Second), farFuture, false); err != nil || got.Detail != "frac" {
		t.Errorf("fractional second: %+v, %v", got, err)
	}
	if _, err := FindDNSCheck(db, "example.jp", "MX", base.Add(6*time.Hour), farFuture, false); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("nothing after: %v", err)
	}
	if _, err := FindDNSCheck(db, "example.jp", "AAAA", base.Add(-72*time.Hour), farFuture, false); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("other type: %v", err)
	}

	n, err := DeleteDNSChecksOlderThan(db, base)
	if err != nil || n != 1 {
		t.Fatalf("delete older: %d, %v", n, err)
	}
	if got, err := FindDNSCheck(db, "example.jp", "MX", base.Add(-72*time.Hour), farFuture, false); err != nil || got.Detail != "at" {
		t.Errorf("after the cleanup: %+v, %v", got, err)
	}
}

// TestDNSCheckInsertDefaultsTime checks that a zero CheckedAt becomes the
// current time in UTC.
func TestDNSCheckInsertDefaultsTime(t *testing.T) {
	db := openTestMasterDB(t)
	before := time.Now().Add(-time.Second)
	c := &DNSCheck{Name: "example.jp", QType: "MX", Outcome: "exists", Detail: "d"}
	if err := InsertDNSCheck(db, c); err != nil {
		t.Fatal(err)
	}
	if c.CheckedAt.Before(before) || c.CheckedAt.Location() != time.UTC {
		t.Errorf("checked_at %v", c.CheckedAt)
	}
	got, err := FindDNSCheck(db, "example.jp", "MX", before, farFuture, false)
	if err != nil || got.ID != c.ID {
		t.Errorf("find: %+v, %v", got, err)
	}
}

// TestDNSCheckInsertIfAbsent checks that an outcome is stored only when its
// window holds none yet, and that the earliest row of the window is
// returned either way.
func TestDNSCheckInsertIfAbsent(t *testing.T) {
	db := openTestMasterDB(t)
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	window := base.Add(-24 * time.Hour)

	first := &DNSCheck{Name: "example.jp", QType: "MX", Outcome: "exists", Detail: "first", CheckedAt: base}
	got, inserted, err := InsertDNSCheckIfAbsent(db, first, window, farFuture)
	if err != nil || !inserted || first.ID == 0 || got.ID != first.ID || got.Outcome != "exists" {
		t.Fatalf("first: %+v, %v, %v (id %d)", got, inserted, err, first.ID)
	}
	// Another run checked the same name a little later: its outcome is not
	// stored and the first one is returned.
	second := &DNSCheck{Name: "example.jp", QType: "MX", Outcome: "broken", Detail: "second", CheckedAt: base.Add(time.Minute)}
	got, inserted, err = InsertDNSCheckIfAbsent(db, second, window, farFuture)
	if err != nil || inserted || second.ID != 0 || got.ID != first.ID || got.Outcome != "exists" {
		t.Errorf("second: %+v, %v, %v (id %d)", got, inserted, err, second.ID)
	}
	// A window that starts after the first row, and another type, store.
	later := &DNSCheck{Name: "example.jp", QType: "MX", Outcome: "not_found", Detail: "later", CheckedAt: base.Add(48 * time.Hour)}
	if got, inserted, err := InsertDNSCheckIfAbsent(db, later, base.Add(time.Hour), farFuture); err != nil || !inserted || got.ID != later.ID {
		t.Errorf("later window: %+v, %v, %v", got, inserted, err)
	}
	other := &DNSCheck{Name: "example.jp", QType: "A", Outcome: "exists", Detail: "other type", CheckedAt: base.Add(time.Minute)}
	if got, inserted, err := InsertDNSCheckIfAbsent(db, other, window, farFuture); err != nil || !inserted || got.ID != other.ID {
		t.Errorf("other type: %+v, %v, %v", got, inserted, err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM dns_checks`).Scan(&n); err != nil || n != 3 {
		t.Errorf("%d rows (%v), want 3", n, err)
	}
}

// TestDNSCheckWindowEdges checks the edges the reuse rule depends on: a row
// checked exactly at the start of the window counts as present (nothing is
// inserted and it is returned), one a nanosecond before it does not, and of
// two rows checked at the same time the one stored first (the lower id)
// wins.
func TestDNSCheckWindowEdges(t *testing.T) {
	db := openTestMasterDB(t)
	window := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	at := &DNSCheck{Name: "at.example.jp", QType: "MX", Outcome: "broken", Detail: "at", CheckedAt: window}
	before := &DNSCheck{Name: "before.example.jp", QType: "MX", Outcome: "broken", Detail: "before", CheckedAt: window.Add(-time.Nanosecond)}
	for _, c := range []*DNSCheck{at, before} {
		if err := InsertDNSCheck(db, c); err != nil {
			t.Fatal(err)
		}
	}
	got, inserted, err := InsertDNSCheckIfAbsent(db, &DNSCheck{Name: "at.example.jp", QType: "MX", Outcome: "exists", Detail: "new",
		CheckedAt: window.Add(time.Hour)}, window, farFuture)
	if err != nil || inserted || got.ID != at.ID || got.Outcome != "broken" {
		t.Errorf("row at the start of the window: %+v, inserted %v, %v", got, inserted, err)
	}
	fresh := &DNSCheck{Name: "before.example.jp", QType: "MX", Outcome: "exists", Detail: "new", CheckedAt: window.Add(time.Hour)}
	got, inserted, err = InsertDNSCheckIfAbsent(db, fresh, window, farFuture)
	if err != nil || !inserted || got.ID != fresh.ID || got.Outcome != "exists" {
		t.Errorf("row just before the window: %+v, inserted %v, %v", got, inserted, err)
	}

	first := &DNSCheck{Name: "tie.example.jp", QType: "MX", Outcome: "not_found", Detail: "first", CheckedAt: window}
	second := &DNSCheck{Name: "tie.example.jp", QType: "MX", Outcome: "exists", Detail: "second", CheckedAt: window}
	for _, c := range []*DNSCheck{first, second} {
		if err := InsertDNSCheck(db, c); err != nil {
			t.Fatal(err)
		}
	}
	if first.ID >= second.ID {
		t.Fatalf("ids %d, %d", first.ID, second.ID)
	}
	if got, err := FindDNSCheck(db, "tie.example.jp", "MX", window.Add(-time.Hour), farFuture, false); err != nil || got.ID != first.ID || got.Detail != "first" {
		t.Errorf("same checked_at: %+v, %v; want id %d", got, err, first.ID)
	}
	// The same row wins for InsertDNSCheckIfAbsent.
	if got, inserted, err := InsertDNSCheckIfAbsent(db, &DNSCheck{Name: "tie.example.jp", QType: "MX", Outcome: "broken", Detail: "third"},
		window.Add(-time.Hour), farFuture); err != nil || inserted || got.ID != first.ID {
		t.Errorf("same checked_at, insert: %+v, inserted %v, %v; want id %d", got, inserted, err, first.ID)
	}
}

// TestDNSCheckWindowBothEnds checks the two ends of the window of a notice
// (24 hours before it to 48 hours after): a row exactly at either end
// counts, one a nanosecond outside does not, for FindDNSCheck and for
// InsertDNSCheckIfAbsent alike, and a row the caller would store outside
// the window is refused.
func TestDNSCheckWindowBothEnds(t *testing.T) {
	db := openTestMasterDB(t)
	notice := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	from, to := notice.Add(-24*time.Hour), notice.Add(48*time.Hour)
	rows := []*DNSCheck{
		{Name: "start.example.jp", CheckedAt: from},
		{Name: "before.example.jp", CheckedAt: from.Add(-time.Nanosecond)},
		{Name: "end.example.jp", CheckedAt: to},
		{Name: "after.example.jp", CheckedAt: to.Add(time.Nanosecond)},
		// A row checked three days after the notice (a later notice verified
		// the name live) is no evidence for it.
		{Name: "days.example.jp", CheckedAt: notice.Add(72 * time.Hour)},
	}
	for _, r := range rows {
		r.QType, r.Outcome, r.Detail = "MX", "not_found", "stored"
		if err := InsertDNSCheck(db, r); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range rows {
		want := r.Name == "start.example.jp" || r.Name == "end.example.jp"
		got, err := FindDNSCheck(db, r.Name, "MX", from, to, false)
		switch {
		case want && (err != nil || got.ID != r.ID):
			t.Errorf("find %s: %+v, %v; want id %d", r.Name, got, err, r.ID)
		case !want && !errors.Is(err, sql.ErrNoRows):
			t.Errorf("find %s: %+v, %v; want no row", r.Name, got, err)
		}
		fresh := &DNSCheck{Name: r.Name, QType: "MX", Outcome: "exists", Detail: "new", CheckedAt: notice.Add(30 * time.Hour)}
		got, inserted, err := InsertDNSCheckIfAbsent(db, fresh, from, to)
		switch {
		case err != nil:
			t.Errorf("insert %s: %v", r.Name, err)
		case want && (inserted || got.ID != r.ID || got.Outcome != "not_found"):
			t.Errorf("insert %s: %+v, inserted %v; want the stored row %d", r.Name, got, inserted, r.ID)
		case !want && (!inserted || got.ID != fresh.ID || got.Outcome != "exists"):
			t.Errorf("insert %s: %+v, inserted %v; want the new row", r.Name, got, inserted)
		}
	}

	// A separate row exactly at the end of the window is found again.
	atEnd := &DNSCheck{Name: "end-row.example.jp", QType: "MX", Outcome: "exists", Detail: "end", CheckedAt: to}
	if got, inserted, err := InsertDNSCheckIfAbsent(db, atEnd, from, to); err != nil || !inserted || got.ID != atEnd.ID {
		t.Errorf("separate row at the end: %+v, inserted %v, %v", got, inserted, err)
	}
	// Outside the window nothing is stored.
	for _, at := range []time.Time{from.Add(-time.Second), to.Add(time.Second)} {
		out := &DNSCheck{Name: "outside.example.jp", QType: "MX", Outcome: "exists", Detail: "outside", CheckedAt: at}
		if got, inserted, err := InsertDNSCheckIfAbsent(db, out, from, to); err == nil || inserted || got != nil || out.ID != 0 {
			t.Errorf("checked at %v: %+v, inserted %v, %v; want an error", at, got, inserted, err)
		}
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM dns_checks WHERE name = 'outside.example.jp'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d rows outside the window (%v), want 0", n, err)
	}
}

// TestDNSCheckInsertIfAbsentConcurrent races several runs storing the same
// name: exactly one row is stored and every run gets it back.
func TestDNSCheckInsertIfAbsentConcurrent(t *testing.T) {
	db := openTestMasterDB(t)
	window := time.Now().Add(-24 * time.Hour)
	end := time.Now().Add(48 * time.Hour)
	const runs = 8
	ids := make([]int64, runs)
	errs := make([]error, runs)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range runs {
		wg.Go(func() {
			<-start
			c := &DNSCheck{Name: "race.example.jp", QType: "MX", Outcome: "exists", Detail: fmt.Sprintf("run %d", i)}
			got, _, err := InsertDNSCheckIfAbsent(db, c, window, end)
			if err != nil {
				errs[i] = err
				return
			}
			ids[i] = got.ID
		})
	}
	close(start)
	wg.Wait()
	for i := range runs {
		if errs[i] != nil {
			t.Fatalf("run %d: %v", i, errs[i])
		}
		if ids[i] == 0 || ids[i] != ids[0] {
			t.Errorf("run %d got row %d, run 0 row %d", i, ids[i], ids[0])
		}
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM dns_checks WHERE name = 'race.example.jp'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("%d rows (%v), want 1", n, err)
	}
}

// TestDNSCheckDirectKinds checks the two kinds of rows (direct: the
// authoritative servers were asked as well): FindDNSCheck with directOnly
// skips the rows of the resolvers alone, and InsertDNSCheckIfAbsent counts
// only a direct row as present for a direct one, and any row for one of the
// resolvers alone, returning the earliest row of the kind it looked for.
func TestDNSCheckDirectKinds(t *testing.T) {
	db := openTestMasterDB(t)
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	from, to := base.Add(-24*time.Hour), base.Add(48*time.Hour)
	resolvers := &DNSCheck{Name: "example.jp", QType: "MX", Outcome: "not_found", Detail: "resolvers", CheckedAt: base}
	if err := InsertDNSCheck(db, resolvers); err != nil {
		t.Fatal(err)
	}
	if got, err := FindDNSCheck(db, "example.jp", "MX", from, to, false); err != nil || got.ID != resolvers.ID || got.Direct {
		t.Errorf("any kind: %+v, %v", got, err)
	}
	if _, err := FindDNSCheck(db, "example.jp", "MX", from, to, true); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("direct only, none stored: %v", err)
	}

	// A direct row is stored next to the row of the resolvers alone.
	direct := &DNSCheck{Name: "example.jp", QType: "MX", Outcome: "broken", Detail: "direct", Direct: true, CheckedAt: base.Add(time.Hour)}
	got, inserted, err := InsertDNSCheckIfAbsent(db, direct, from, to)
	if err != nil || !inserted || got.ID != direct.ID || !got.Direct || got.Outcome != "broken" {
		t.Fatalf("direct next to resolvers: %+v, inserted %v, %v", got, inserted, err)
	}
	// Another direct row is not: the first direct one wins.
	second := &DNSCheck{Name: "example.jp", QType: "MX", Outcome: "exists", Detail: "second", Direct: true, CheckedAt: base.Add(2 * time.Hour)}
	if got, inserted, err := InsertDNSCheckIfAbsent(db, second, from, to); err != nil || inserted || got.ID != direct.ID {
		t.Errorf("second direct: %+v, inserted %v, %v; want the first direct row", got, inserted, err)
	}
	// Nor another row of the resolvers alone: the earliest row of either
	// kind wins.
	again := &DNSCheck{Name: "example.jp", QType: "MX", Outcome: "exists", Detail: "again", CheckedAt: base.Add(3 * time.Hour)}
	if got, inserted, err := InsertDNSCheckIfAbsent(db, again, from, to); err != nil || inserted || got.ID != resolvers.ID {
		t.Errorf("resolvers again: %+v, inserted %v, %v; want the earliest row", got, inserted, err)
	}
	if got, err := FindDNSCheck(db, "example.jp", "MX", from, to, true); err != nil || got.ID != direct.ID {
		t.Errorf("direct only: %+v, %v", got, err)
	}
	// A row of the resolvers alone is not stored where only a direct one
	// is: that one counts for every notice.
	onlyDirect := &DNSCheck{Name: "other.example.jp", QType: "MX", Outcome: "exists", Detail: "direct", Direct: true, CheckedAt: base}
	if err := InsertDNSCheck(db, onlyDirect); err != nil {
		t.Fatal(err)
	}
	if got, inserted, err := InsertDNSCheckIfAbsent(db, &DNSCheck{Name: "other.example.jp", QType: "MX", Outcome: "not_found", Detail: "resolvers",
		CheckedAt: base.Add(time.Hour)}, from, to); err != nil || inserted || got.ID != onlyDirect.ID || !got.Direct {
		t.Errorf("resolvers where a direct row is: %+v, inserted %v, %v", got, inserted, err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM dns_checks`).Scan(&n); err != nil || n != 3 {
		t.Errorf("%d rows (%v), want 3", n, err)
	}
}

// TestDNSChecksMigrationDown checks that the migration can be rolled back
// (table and index dropped) and applied again, with every column.
func TestDNSChecksMigrationDown(t *testing.T) {
	db := openTestMasterDB(t)
	ctx := context.Background()
	p, err := goose.NewProvider(goose.DialectSQLite3, db, dbschema.Master(), goose.WithDisableGlobalRegistry(true))
	if err != nil {
		t.Fatal(err)
	}
	count := func() int {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name IN ('dns_checks', 'idx_dns_checks_lookup')`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(); n != 2 {
		t.Fatalf("after up: %d objects", n)
	}
	if _, err := p.DownTo(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 0 {
		t.Fatalf("after down: %d objects", n)
	}
	if _, err := dbschema.Apply(ctx, db, dbschema.Master()); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 2 {
		t.Fatalf("after up again: %d objects", n)
	}
	rows, err := db.Query(`SELECT name, "notnull" FROM pragma_table_info('dns_checks') ORDER BY cid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var name string
		var notNull bool
		if err := rows.Scan(&name, &notNull); err != nil {
			t.Fatal(err)
		}
		if !notNull && name != "id" {
			t.Errorf("column %s may be NULL", name)
		}
		columns = append(columns, name)
	}
	if got := fmt.Sprint(columns); got != "[id name qtype outcome detail direct checked_at]" {
		t.Errorf("columns %s", got)
	}
	c := &DNSCheck{Name: "example.jp", QType: "MX", Outcome: "exists", Detail: "d", Direct: true}
	if err := InsertDNSCheck(db, c); err != nil {
		t.Fatal(err)
	}
	if got, err := FindDNSCheck(db, "example.jp", "MX", c.CheckedAt, c.CheckedAt, true); err != nil || !got.Direct {
		t.Errorf("after up again: %+v, %v", got, err)
	}
}
