package models

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pressly/goose/v3"

	"mailcare/app/modules/dbschema"
)

// TestSendingSourceLookupReplace checks that a source (kind and name) has one
// row: a replacement updates it in place, the lists round-trip, and the
// same name of another kind is another source.
func TestSendingSourceLookupReplace(t *testing.T) {
	db := openTestMasterDB(t)
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	rows := []*SendingSourceLookup{
		{Kind: SendingSourceKindSMTPHost, Name: "mail.example.jp", Addresses: []string{"192.0.2.25", "2001:db8::25"}, FetchedAt: base},
		{Kind: SendingSourceKindSPF, Name: "example.jp", Addresses: []string{"192.0.2.0/24"}, Hosts: []string{"mx1.example.jp"}, FetchedAt: base},
		{Kind: SendingSourceKindGlobalHost, Name: "mail.example.jp", Addresses: []string{"198.51.100.7"}, FetchedAt: base},
	}
	for _, r := range rows {
		if _, err := ReplaceSendingSourceLookup(db, r, false); err != nil {
			t.Fatalf("replace %s %s: %v", r.Kind, r.Name, err)
		}
	}
	all, err := ListSendingSourceLookups(db)
	if err != nil || len(all) != 3 {
		t.Fatalf("list: %d rows (err %v), want 3", len(all), err)
	}
	for i, r := range rows {
		got, err := GetSendingSourceLookup(db, r.Kind, r.Name)
		r.ID = all[i].ID
		if err != nil || !reflect.DeepEqual(got, r) {
			t.Errorf("get %s %s: %+v (err %v), want %+v", r.Kind, r.Name, got, err, r)
		}
	}

	// A replacement keeps one row per source.
	again := &SendingSourceLookup{Kind: SendingSourceKindSMTPHost, Name: "mail.example.jp", Addresses: []string{"203.0.113.9"}}
	if _, err := ReplaceSendingSourceLookup(db, again, false); err != nil {
		t.Fatal(err)
	}
	got, err := GetSendingSourceLookup(db, SendingSourceKindSMTPHost, "mail.example.jp")
	if err != nil || !reflect.DeepEqual(got.Addresses, []string{"203.0.113.9"}) || got.Hosts != nil ||
		got.FetchedAt.Location() != time.UTC || time.Since(got.FetchedAt) > time.Minute {
		t.Errorf("after the replacement: %+v (err %v)", got, err)
	}
	if all, _ := ListSendingSourceLookups(db); len(all) != 3 {
		t.Errorf("%d rows after the replacement, want 3", len(all))
	}
	if _, err := GetSendingSourceLookup(db, SendingSourceKindSPF, "mail.example.jp"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("another kind: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO sending_source_lookups (kind, name, addresses, hosts, fetched_at) VALUES (?, ?, '', '', ?)`,
		SendingSourceKindSPF, "example.jp", base); err == nil {
		t.Error("a second row of a source was accepted")
	}
	if err := DeleteSendingSourceLookup(db, got.ID); err != nil {
		t.Fatal(err)
	}
	if all, _ := ListSendingSourceLookups(db); len(all) != 2 {
		t.Errorf("%d rows after deleting one", len(all))
	}
}

// TestSendingSourceLookupKeepsNonEmpty checks the condition of the statement:
// with keepNonEmpty a row listing nothing does not replace a non-empty one,
// whatever its age (one dated in the future included), and replaces an
// empty one; without it, it replaces any row; a non-empty row always
// replaces.
func TestSendingSourceLookupKeepsNonEmpty(t *testing.T) {
	db := openTestMasterDB(t)
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name          string
		stored        []string // addresses of the stored row (nil: empty)
		storedHosts   []string
		age           time.Duration
		next          []string // addresses of the new row
		keep          bool
		written       bool
		wantAddresses []string
	}{
		{"empty over a recent non-empty", []string{"192.0.2.25"}, nil, time.Hour, nil, true, false, []string{"192.0.2.25"}},
		{"empty over a year-old non-empty", []string{"192.0.2.25"}, nil, 365 * 24 * time.Hour, nil, true, false, []string{"192.0.2.25"}},
		{"empty over hosts only", nil, []string{"mx1.example.jp"}, time.Hour, nil, true, false, nil},
		{"empty over one dated in the future", []string{"192.0.2.25"}, nil, -time.Hour, nil, true, false, []string{"192.0.2.25"}},
		{"empty over empty", nil, nil, time.Hour, nil, true, true, nil},
		{"empty, not kept", []string{"192.0.2.25"}, nil, time.Hour, nil, false, true, nil},
		{"non-empty over non-empty", []string{"192.0.2.25"}, nil, time.Hour, []string{"198.51.100.7"}, true, true, []string{"198.51.100.7"}},
	} {
		name := "mail.example.jp"
		if _, err := ReplaceSendingSourceLookup(db, &SendingSourceLookup{Kind: SendingSourceKindSPF, Name: name, Addresses: c.stored, Hosts: c.storedHosts,
			FetchedAt: now.Add(-c.age)}, false); err != nil {
			t.Fatal(err)
		}
		written, err := ReplaceSendingSourceLookup(db, &SendingSourceLookup{Kind: SendingSourceKindSPF, Name: name, Addresses: c.next, FetchedAt: now}, c.keep)
		if err != nil || written != c.written {
			t.Errorf("%s: written %v (err %v), want %v", c.name, written, err, c.written)
		}
		got, err := GetSendingSourceLookup(db, SendingSourceKindSPF, name)
		if err != nil || !reflect.DeepEqual(got.Addresses, c.wantAddresses) || got.FetchedAt.Equal(now) != c.written {
			t.Errorf("%s: stored %+v (err %v), want addresses %v, renewed %v", c.name, got, err, c.wantAddresses, c.written)
		}
	}
	// A row listing host names only is not empty: it replaces a non-empty
	// one.
	if _, err := ReplaceSendingSourceLookup(db, &SendingSourceLookup{Kind: SendingSourceKindSPF, Name: "hosts.example.jp", Addresses: []string{"192.0.2.0/24"},
		FetchedAt: now.Add(-time.Hour)}, false); err != nil {
		t.Fatal(err)
	}
	if written, err := ReplaceSendingSourceLookup(db, &SendingSourceLookup{Kind: SendingSourceKindSPF, Name: "hosts.example.jp", Hosts: []string{"mx1.example.jp"},
		FetchedAt: now}, true); err != nil || !written {
		t.Errorf("hosts only over a non-empty: written %v (err %v), want true", written, err)
	}
	if got, err := GetSendingSourceLookup(db, SendingSourceKindSPF, "hosts.example.jp"); err != nil || got.Addresses != nil ||
		!reflect.DeepEqual(got.Hosts, []string{"mx1.example.jp"}) {
		t.Errorf("hosts only over a non-empty: stored %+v (err %v)", got, err)
	}
	// A new source is inserted whatever it lists.
	if written, err := ReplaceSendingSourceLookup(db, &SendingSourceLookup{Kind: SendingSourceKindSMTPHost, Name: "new.example.jp", FetchedAt: now}, true); err != nil || !written {
		t.Errorf("new empty source: written %v (err %v)", written, err)
	}
}

// TestSendingSourceLookupConcurrentWriters races runs storing the same source,
// half of them an empty answer, each through a separate database handle
// (run with -race): the non-empty answer always survives, since an empty one
// never replaces it, whatever the order.
func TestSendingSourceLookupConcurrentWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mailcare.db")
	open := func() *sql.DB {
		db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_time_format=sqlite")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		return db
	}
	if _, err := dbschema.Apply(context.Background(), open(), dbschema.Master()); err != nil {
		t.Fatal(err)
	}
	for round := range 5 {
		name := fmt.Sprintf("mail%d.example.jp", round)
		const writers = 8
		var wg sync.WaitGroup
		errs := make([]error, writers)
		start := make(chan struct{})
		for i := range writers {
			db := open()
			wg.Go(func() {
				<-start
				row := &SendingSourceLookup{Kind: SendingSourceKindSMTPHost, Name: name}
				if i%2 == 0 {
					row.Addresses = []string{"192.0.2.25"}
				}
				_, errs[i] = ReplaceSendingSourceLookup(db, row, true)
			})
		}
		close(start)
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("round %d, writer %d: %v", round, i, err)
			}
		}
		got, err := GetSendingSourceLookup(open(), SendingSourceKindSMTPHost, name)
		if err != nil || !reflect.DeepEqual(got.Addresses, []string{"192.0.2.25"}) {
			t.Errorf("round %d: stored %+v (err %v), want the non-empty answer", round, got, err)
		}
	}
}

// TestSendingSourceLookupCleanup checks that the cleanup removes only the
// rows dated after now, whatever the age of the others.
func TestSendingSourceLookupCleanup(t *testing.T) {
	db := openTestMasterDB(t)
	now := time.Now().UTC()
	for _, r := range []*SendingSourceLookup{
		{Kind: SendingSourceKindSMTPHost, Name: "old.example.jp", FetchedAt: now.Add(-3650 * 24 * time.Hour)},
		{Kind: SendingSourceKindSMTPHost, Name: "new.example.jp", FetchedAt: now.Add(-time.Second)},
		{Kind: SendingSourceKindSMTPHost, Name: "future.example.jp", FetchedAt: now.Add(time.Hour)},
	} {
		if _, err := ReplaceSendingSourceLookup(db, r, false); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := DeleteFutureSendingSourceLookups(db); err != nil || n != 1 {
		t.Errorf("dated in the future: %d removed (err %v), want 1", n, err)
	}
	all, err := ListSendingSourceLookups(db)
	if err != nil || len(all) != 2 || all[0].Name != "old.example.jp" || all[1].Name != "new.example.jp" {
		t.Errorf("after the cleanup: %+v (err %v)", all, err)
	}
}

// TestSendingSourceLookupDeleteKindOlderThan checks the cleanup of the
// reporting MTAs: only the rows of the kind fetched before the cutoff go,
// a row exactly at it stays, and the other kinds stay whatever their age.
func TestSendingSourceLookupDeleteKindOlderThan(t *testing.T) {
	db := openTestMasterDB(t)
	cutoff := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	for _, r := range []*SendingSourceLookup{
		{Kind: SendingSourceKindReporter, Name: "old.example.jp", Addresses: []string{"192.0.2.1"}, FetchedAt: cutoff.Add(-time.Second)},
		{Kind: SendingSourceKindReporter, Name: "at.example.jp", FetchedAt: cutoff},
		{Kind: SendingSourceKindReporter, Name: "new.example.jp", FetchedAt: cutoff.Add(time.Hour)},
		{Kind: SendingSourceKindSMTPHost, Name: "old.example.jp", FetchedAt: cutoff.Add(-3650 * 24 * time.Hour)},
		{Kind: SendingSourceKindGlobalHost, Name: "old.example.jp", FetchedAt: cutoff.Add(-3650 * 24 * time.Hour)},
		{Kind: SendingSourceKindSPF, Name: "example.jp", FetchedAt: cutoff.Add(-3650 * 24 * time.Hour)},
	} {
		if _, err := ReplaceSendingSourceLookup(db, r, false); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := DeleteSendingSourceLookupsOlderThan(db, SendingSourceKindReporter, cutoff); err != nil || n != 1 {
		t.Errorf("removed %d (err %v), want 1", n, err)
	}
	all, err := ListSendingSourceLookups(db)
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, r := range all {
		left = append(left, r.Kind+" "+r.Name)
	}
	want := "reporter at.example.jp, reporter new.example.jp, smtp_host old.example.jp, global_host old.example.jp, spf example.jp"
	if got := strings.Join(left, ", "); got != want {
		t.Errorf("left %s, want %s", got, want)
	}
}

// TestSendingSourceLookupsMigrationDown checks that the migration can be rolled
// back and applied again, leaving the earlier ones alone.
func TestSendingSourceLookupsMigrationDown(t *testing.T) {
	db := openTestMasterDB(t)
	ctx := context.Background()
	p, err := goose.NewProvider(goose.DialectSQLite3, db, dbschema.Master(), goose.WithDisableGlobalRegistry(true))
	if err != nil {
		t.Fatal(err)
	}
	count := func(name string) int {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = ?`, name).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count("sending_source_lookups") != 1 {
		t.Fatal("no table after up")
	}
	if _, err := p.DownTo(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if count("sending_source_lookups") != 0 || count("dns_checks") != 1 {
		t.Fatal("rolling back 0003 dropped the wrong tables")
	}
	if _, err := dbschema.Apply(ctx, db, dbschema.Master()); err != nil {
		t.Fatal(err)
	}
	if count("sending_source_lookups") != 1 {
		t.Fatal("no table after up again")
	}
}
