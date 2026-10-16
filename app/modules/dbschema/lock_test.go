package dbschema

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, registered as "sqlite"
)

// TestMain wires in the files the binary embeds (embed.go), read from the
// repository: the migrations.
func TestMain(m *testing.M) {
	FS = os.DirFS(filepath.Join("..", "..", "..", "embedded"))
	os.Exit(m.Run())
}

// openAt opens a separate database handle on path, the way the
// application does.
func openAt(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_time_format=sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// versionRows returns how many goose_db_version rows each version has.
func versionRows(t *testing.T, db *sql.DB) map[int64]int {
	t.Helper()
	rows, err := db.Query(`SELECT version_id, COUNT(*) FROM goose_db_version GROUP BY version_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var v int64
		var n int
		if err := rows.Scan(&v, &n); err != nil {
			t.Fatal(err)
		}
		out[v] = n
	}
	return out
}

// migrationCount returns the number of migration files of fsys.
func migrationCount(t *testing.T, fsys fs.FS) int {
	t.Helper()
	files, err := fs.Glob(fsys, "*.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("migrations: %v, %v", files, err)
	}
	return len(files)
}

// lockGone fails the test when the lock file of path is left behind.
func lockGone(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path + LockSuffix); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the lock file is left behind: %v", err)
	}
}

// TestApplyLockedConcurrent opens an un-migrated database from several
// handles at once (as two processes would): every version is recorded
// exactly once, every opener ends up with the full schema and the lock file
// is gone afterwards; the same for a mailbox index.
func TestApplyLockedConcurrent(t *testing.T) {
	for name, fsys := range map[string]fs.FS{"master": Master(), "index": Index()} {
		t.Run(name, func(t *testing.T) {
			for round := range 3 {
				path := filepath.Join(t.TempDir(), fmt.Sprintf("db%d.sqlite", round))
				const openers = 8
				dbs := make([]*sql.DB, openers)
				for i := range dbs {
					dbs[i] = openAt(t, path)
				}
				errs := make([]error, openers)
				var wg sync.WaitGroup
				start := make(chan struct{})
				for i := range openers {
					wg.Go(func() {
						<-start
						_, errs[i] = ApplyLocked(context.Background(), dbs[i], fsys, path)
					})
				}
				close(start)
				wg.Wait()
				for i, err := range errs {
					if err != nil {
						t.Fatalf("round %d, opener %d: %v", round, i, err)
					}
				}
				got := versionRows(t, dbs[0])
				if want := migrationCount(t, fsys) + 1; len(got) != want { // version 0 is the row goose adds itself
					t.Errorf("round %d: versions %v, want %d", round, got, want)
				}
				for v, n := range got {
					if n != 1 {
						t.Errorf("round %d: version %d recorded %d times", round, v, n)
					}
				}
				lockGone(t, path)
			}
		})
	}
}

// writeLock writes a lock file as another holder would, dated at.
func writeLock(t *testing.T, path string, at time.Time, content string) {
	t.Helper()
	if content == "" {
		content = fmt.Sprintf("12345 %s othertoken\n", at.UTC().Format(time.RFC3339Nano))
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

// TestApplyLockedTakesOverStaleLock: a lock file older than two minutes
// (its holder died) is taken over, by the time written in it or, when that
// cannot be read, by its modification time, and is gone afterwards.
func TestApplyLockedTakesOverStaleLock(t *testing.T) {
	old := time.Now().Add(-3 * time.Minute)
	for name, content := range map[string]string{"dated": "", "unreadable": "garbage"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "mailcare.db")
			writeLock(t, path+LockSuffix, old, content)
			start := time.Now()
			if _, err := ApplyLocked(context.Background(), openAt(t, path), Master(), path); err != nil {
				t.Fatal(err)
			}
			if d := time.Since(start); d > 10*time.Second {
				t.Errorf("took %v: the stale lock was waited for", d)
			}
			lockGone(t, path)
			matches, _ := filepath.Glob(path + LockSuffix + ".stale-*")
			if len(matches) != 0 {
				t.Errorf("files left behind: %v", matches)
			}
		})
	}
}

// TestApplyLockedWaitsForHolder: a fresh lock of another holder is waited
// for: applied as soon as it is removed, refused (and left alone) when it
// stays for lockWait; a database that is up to date takes no lock at all.
func TestApplyLockedWaitsForHolder(t *testing.T) {
	saved := lockWait
	t.Cleanup(func() { lockWait = saved })

	path := filepath.Join(t.TempDir(), "mailcare.db")
	lock := path + LockSuffix
	writeLock(t, lock, time.Now(), "")
	lockWait = 300 * time.Millisecond
	_, err := ApplyLocked(context.Background(), openAt(t, path), Master(), path)
	if err == nil || !strings.Contains(err.Error(), "another process") {
		t.Errorf("held lock: %v; want refused", err)
	}
	if _, err := os.Stat(lock); err != nil {
		t.Errorf("the lock of the other holder was removed: %v", err)
	}

	lockWait = 10 * time.Second
	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = os.Remove(lock)
	}()
	db := openAt(t, path)
	if applied, err := ApplyLocked(context.Background(), db, Master(), path); err != nil || len(applied) == 0 {
		t.Fatalf("after the release: %v, %v", applied, err)
	}
	lockGone(t, path)

	writeLock(t, lock, time.Now(), "")
	lockWait = 0
	if applied, err := ApplyLocked(context.Background(), db, Master(), path); err != nil || len(applied) != 0 {
		t.Errorf("up to date: %v, %v; want nothing applied and no wait", applied, err)
	}
}

// TestApplyLockedReleasesOnFailure: a migration that fails still removes
// the lock file.
func TestApplyLockedReleasesOnFailure(t *testing.T) {
	broken := fstest.MapFS{"0001_broken.sql": {Data: []byte("-- +goose Up\nCREATE TABLE (;\n-- +goose Down\n")}}
	path := filepath.Join(t.TempDir(), "mailcare.db")
	if _, err := ApplyLocked(context.Background(), openAt(t, path), broken, path); err == nil {
		t.Fatal("a broken migration was applied")
	}
	lockGone(t, path)
}

// TestLockDefaults checks the bounds of the specification: another holder
// is waited for 30 seconds, and a lock older than 2 minutes is stale.
func TestLockDefaults(t *testing.T) {
	if lockWait != 30*time.Second || lockStale != 2*time.Minute {
		t.Errorf("lockWait %v, lockStale %v; want 30s and 2m", lockWait, lockStale)
	}
}

// TestApplyLockedStaleBoundary: a lock written 90 seconds ago is still the
// other holder's (waited for, then refused), and one written 150 seconds ago
// is stale (taken over at once).
func TestApplyLockedStaleBoundary(t *testing.T) {
	saved := lockWait
	t.Cleanup(func() { lockWait = saved })
	lockWait = 300 * time.Millisecond

	path := filepath.Join(t.TempDir(), "mailcare.db")
	writeLock(t, path+LockSuffix, time.Now().Add(-90*time.Second), "")
	if _, err := ApplyLocked(context.Background(), openAt(t, path), Master(), path); err == nil || !strings.Contains(err.Error(), "another process") {
		t.Errorf("lock 90 seconds old: %v; want refused", err)
	}

	path = filepath.Join(t.TempDir(), "mailcare.db")
	writeLock(t, path+LockSuffix, time.Now().Add(-150*time.Second), "")
	if _, err := ApplyLocked(context.Background(), openAt(t, path), Master(), path); err != nil {
		t.Errorf("lock 150 seconds old: %v; want taken over", err)
	}
	lockGone(t, path)
}

// TestReleaseLockKeepsOtherHolder: releasing removes the lock file only
// while it holds the token of the release (a holder that took a stale lock
// over keeps it), and the time of a lock is read only from a line of three
// fields.
func TestReleaseLockKeepsOtherHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mailcare.db") + LockSuffix
	writeLock(t, path, time.Now(), "")
	releaseLock(path, "releasetoken")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the lock of another holder was removed: %v", err)
	}
	releaseLock(path, "othertoken")
	lockGone(t, strings.TrimSuffix(path, LockSuffix))

	at := time.Now().UTC().Format(time.RFC3339Nano)
	for content, want := range map[string]bool{
		"12345 " + at + " token":       true,
		"12345 " + at:                  false,
		"12345 " + at + " token extra": false,
	} {
		if _, ok := lockTime([]byte(content)); ok != want {
			t.Errorf("lockTime(%q) ok = %v, want %v", content, ok, want)
		}
	}
}
