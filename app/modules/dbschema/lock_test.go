package dbschema

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, registered as "sqlite"

	"mailcare/app/modules/filelock"
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

// holdLock takes the lock of path as another process would (another open
// file) and returns its release.
func holdLock(t *testing.T, path string) func() {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := filelock.TryLock(f); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			_ = filelock.Unlock(f)
			f.Close()
		})
	}
	t.Cleanup(release)
	return release
}

// TestApplyLockedLeftLockFile: a lock file left behind by a process that
// ended (no lock is held on it) is no obstacle: it is locked at once, and
// removed afterwards.
func TestApplyLockedLeftLockFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mailcare.db")
	if err := os.WriteFile(path+LockSuffix, []byte("12345 2020-01-01T00:00:00Z token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := ApplyLocked(context.Background(), openAt(t, path), Master(), path); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("took %v: the left file was waited for", d)
	}
	lockGone(t, path)
}

// TestApplyLockedWaitsForHolder: a lock held by another process is waited
// for as long as it is held (here far longer than any former limit of the
// wait would allow relative to the poll), applied as soon as it is
// released; a canceled wait ends at once; a database that is up to date
// takes no lock at all.
func TestApplyLockedWaitsForHolder(t *testing.T) {
	saved := lockPoll
	t.Cleanup(func() { lockPoll = saved })
	lockPoll = 5 * time.Millisecond

	path := filepath.Join(t.TempDir(), "mailcare.db")
	release := holdLock(t, path+LockSuffix)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	if _, err := ApplyLocked(ctx, openAt(t, path), Master(), path); !errors.Is(err, context.Canceled) {
		t.Errorf("held lock, canceled wait: %v", err)
	}

	time.AfterFunc(time.Second, release)
	db := openAt(t, path)
	start := time.Now()
	if applied, err := ApplyLocked(context.Background(), db, Master(), path); err != nil || len(applied) == 0 {
		t.Fatalf("after the release: %v, %v", applied, err)
	}
	if d := time.Since(start); d < 900*time.Millisecond {
		t.Errorf("applied after %v while the lock was held for a second", d)
	}
	lockGone(t, path)

	holdLock(t, path+LockSuffix)
	if applied, err := ApplyLocked(context.Background(), db, Master(), path); err != nil || len(applied) != 0 {
		t.Errorf("up to date: %v, %v; want nothing applied and no wait", applied, err)
	}
}

// TestApplyLockedReleasesOnFailure: a migration that fails still removes
// the lock file and releases the lock.
func TestApplyLockedReleasesOnFailure(t *testing.T) {
	broken := fstest.MapFS{"0001_broken.sql": {Data: []byte("-- +goose Up\nCREATE TABLE (;\n-- +goose Down\n")}}
	path := filepath.Join(t.TempDir(), "mailcare.db")
	if _, err := ApplyLocked(context.Background(), openAt(t, path), broken, path); err == nil {
		t.Fatal("a broken migration was applied")
	}
	lockGone(t, path)
}

// TestAcquireLockRetriesRemovedFile: a waiter whose open file was removed
// by the holder before the release locks the new file at the path, so two
// holders never run at once.
func TestAcquireLockRetriesRemovedFile(t *testing.T) {
	saved := lockPoll
	t.Cleanup(func() { lockPoll = saved })
	lockPoll = 5 * time.Millisecond
	path := filepath.Join(t.TempDir(), "x"+LockSuffix)
	var mu sync.Mutex
	holders, most := 0, 0
	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() {
			release, err := acquireLock(context.Background(), path)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			holders++
			most = max(most, holders)
			mu.Unlock()
			time.Sleep(20 * time.Millisecond)
			mu.Lock()
			holders--
			mu.Unlock()
			release()
		})
	}
	wg.Wait()
	if most != 1 {
		t.Errorf("%d holders at once", most)
	}
}
