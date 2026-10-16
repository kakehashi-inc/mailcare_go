package dbschema

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// renamedMigration is the master migration whose file was renamed before
// the first release; oldMigration and oldTable stand for the file name and
// the table name it had before (only the version number in front of the
// file name is kept).
const (
	renamedMigration = "0003_sending_source_lookups.sql"
	oldMigration     = "0003_old_name_lookups.sql"
	oldTable         = "old_name_lookups"
)

// masterWithOldName returns the master migrations as they were before the
// rename: the same files, with the renamed one under its old name and with
// its old table name.
func masterWithOldName(t *testing.T) fstest.MapFS {
	t.Helper()
	master := Master()
	names, err := fs.Glob(master, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	old := fstest.MapFS{}
	found := false
	for _, name := range names {
		data, err := fs.ReadFile(master, name)
		if err != nil {
			t.Fatal(err)
		}
		if name == renamedMigration {
			found = true
			name = oldMigration
			data = []byte(strings.ReplaceAll(string(data), "sending_source_lookups", oldTable))
		}
		old[name] = &fstest.MapFile{Data: data}
	}
	if !found {
		t.Fatalf("%s is not among the master migrations %v", renamedMigration, names)
	}
	return old
}

// TestRenamedMigrationKeepsItsVersion: goose keys a migration on the number
// in front of its file name, so 0003_sending_source_lookups.sql is version 3
// like the file it replaces, and a database that applied the old file is up
// to date for the renamed one: nothing is applied again, version 3 stays
// recorded once, and the table keeps the old name (renaming it is a manual
// step for such a database). A new database gets the new table.
func TestRenamedMigrationKeepsItsVersion(t *testing.T) {
	ctx := context.Background()
	p, err := newProvider(openAt(t, filepath.Join(t.TempDir(), "probe.db")), Master())
	if err != nil {
		t.Fatal(err)
	}
	var version int64 = -1
	for _, s := range p.ListSources() {
		if filepath.Base(s.Path) == renamedMigration {
			version = s.Version
		}
	}
	if version != 3 {
		t.Fatalf("%s is version %d, want 3", renamedMigration, version)
	}

	// A database migrated with the file under its old name.
	path := filepath.Join(t.TempDir(), "mailcare.db")
	db := openAt(t, path)
	applied, err := ApplyLocked(ctx, db, masterWithOldName(t), path)
	if err != nil || len(applied) != migrationCount(t, Master()) || filepath.Base(applied[len(applied)-1]) != oldMigration {
		t.Fatalf("migrating with the old file name: %v, %v", applied, err)
	}
	if !upToDate(ctx, db, Master(), path) {
		t.Error("a database that applied the old file is not up to date for the renamed one")
	}
	applied, err = ApplyLocked(ctx, db, Master(), path)
	if err != nil || len(applied) != 0 {
		t.Errorf("the renamed migration applied again: %v, %v", applied, err)
	}
	if rows := versionRows(t, db); rows[3] != 1 {
		t.Errorf("version rows %v, want version 3 recorded once", rows)
	}
	for table, want := range map[string]int{oldTable: 1, "sending_source_lookups": 0} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&n); err != nil || n != want {
			t.Errorf("table %s: %d (err %v), want %d", table, n, err, want)
		}
	}

	// A new database.
	path = filepath.Join(t.TempDir(), "new.db")
	db = openAt(t, path)
	if _, err := ApplyLocked(ctx, db, Master(), path); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'sending_source_lookups'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("new database: sending_source_lookups %d (err %v)", n, err)
	}
}
