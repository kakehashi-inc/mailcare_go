package models

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/pressly/goose/v3"

	"mailcare/app/modules/dbschema"
)

// TestOwnSourceLookupReplace checks that a source (kind and name) has one
// row: a replacement updates it in place, the lists round-trip, and the
// same name of another kind is another source.
func TestOwnSourceLookupReplace(t *testing.T) {
	db := openTestMasterDB(t)
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	rows := []*OwnSourceLookup{
		{Kind: OwnSourceKindSMTPHost, Name: "mail.example.jp", Addresses: []string{"192.0.2.25", "2001:db8::25"}, FetchedAt: base},
		{Kind: OwnSourceKindSPF, Name: "example.jp", Addresses: []string{"192.0.2.0/24"}, Hosts: []string{"mx1.example.jp"}, FetchedAt: base},
		{Kind: OwnSourceKindGlobalHost, Name: "mail.example.jp", Addresses: []string{"198.51.100.7"}, FetchedAt: base},
	}
	for _, r := range rows {
		if err := ReplaceOwnSourceLookup(db, r); err != nil {
			t.Fatalf("replace %s %s: %v", r.Kind, r.Name, err)
		}
	}
	all, err := ListOwnSourceLookups(db)
	if err != nil || len(all) != 3 {
		t.Fatalf("list: %d rows (err %v), want 3", len(all), err)
	}
	for i, r := range rows {
		got, err := GetOwnSourceLookup(db, r.Kind, r.Name)
		r.ID = all[i].ID
		if err != nil || !reflect.DeepEqual(got, r) {
			t.Errorf("get %s %s: %+v (err %v), want %+v", r.Kind, r.Name, got, err, r)
		}
	}

	// A replacement keeps one row per source.
	again := &OwnSourceLookup{Kind: OwnSourceKindSMTPHost, Name: "mail.example.jp", Addresses: []string{"203.0.113.9"}}
	if err := ReplaceOwnSourceLookup(db, again); err != nil {
		t.Fatal(err)
	}
	got, err := GetOwnSourceLookup(db, OwnSourceKindSMTPHost, "mail.example.jp")
	if err != nil || !reflect.DeepEqual(got.Addresses, []string{"203.0.113.9"}) || got.Hosts != nil ||
		got.FetchedAt.Location() != time.UTC || time.Since(got.FetchedAt) > time.Minute {
		t.Errorf("after the replacement: %+v (err %v)", got, err)
	}
	if all, _ := ListOwnSourceLookups(db); len(all) != 3 {
		t.Errorf("%d rows after the replacement, want 3", len(all))
	}
	if _, err := GetOwnSourceLookup(db, OwnSourceKindSPF, "mail.example.jp"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("another kind: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO own_source_lookups (kind, name, addresses, hosts, fetched_at) VALUES (?, ?, '', '', ?)`,
		OwnSourceKindSPF, "example.jp", base); err == nil {
		t.Error("a second row of a source was accepted")
	}
	if err := DeleteOwnSourceLookup(db, got.ID); err != nil {
		t.Fatal(err)
	}
	if all, _ := ListOwnSourceLookups(db); len(all) != 2 {
		t.Errorf("%d rows after deleting one", len(all))
	}
}

// TestOwnSourceLookupCleanup checks the removal of the old rows and of the
// rows dated in the future.
func TestOwnSourceLookupCleanup(t *testing.T) {
	db := openTestMasterDB(t)
	now := time.Now().UTC()
	for _, r := range []*OwnSourceLookup{
		{Kind: OwnSourceKindSMTPHost, Name: "old.example.jp", FetchedAt: now.Add(-91 * 24 * time.Hour)},
		{Kind: OwnSourceKindSMTPHost, Name: "new.example.jp", FetchedAt: now},
		{Kind: OwnSourceKindSMTPHost, Name: "future.example.jp", FetchedAt: now.Add(25 * time.Hour)},
		{Kind: OwnSourceKindSMTPHost, Name: "soon.example.jp", FetchedAt: now.Add(23 * time.Hour)},
	} {
		if err := ReplaceOwnSourceLookup(db, r); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := DeleteOwnSourceLookupsOlderThan(db, now.Add(-90*24*time.Hour)); err != nil || n != 1 {
		t.Errorf("older than 90 days: %d removed (err %v), want 1", n, err)
	}
	if n, err := DeleteOwnSourceLookupsNewerThan(db, now.Add(24*time.Hour)); err != nil || n != 1 {
		t.Errorf("more than a day in the future: %d removed (err %v), want 1", n, err)
	}
	all, err := ListOwnSourceLookups(db)
	if err != nil || len(all) != 2 || all[0].Name != "new.example.jp" || all[1].Name != "soon.example.jp" {
		t.Errorf("after the cleanup: %+v (err %v)", all, err)
	}
}

// TestOwnSourceLookupsMigrationDown checks that the migration can be rolled
// back and applied again, leaving the earlier ones alone.
func TestOwnSourceLookupsMigrationDown(t *testing.T) {
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
	if count("own_source_lookups") != 1 {
		t.Fatal("no table after up")
	}
	if _, err := p.DownTo(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if count("own_source_lookups") != 0 || count("dns_checks") != 1 {
		t.Fatal("rolling back 0003 dropped the wrong tables")
	}
	if _, err := dbschema.Apply(ctx, db, dbschema.Master()); err != nil {
		t.Fatal(err)
	}
	if count("own_source_lookups") != 1 {
		t.Fatal("no table after up again")
	}
}
