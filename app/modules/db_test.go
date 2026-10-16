package modules

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"mailcare/app/modules/dbschema"
)

func TestOpenDBRestrictsFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no POSIX permission bits")
	}
	db := newTestDB(t)
	dbPath, err := ResolveDBPath()
	if err != nil {
		t.Fatal(err)
	}
	// A world-readable database left behind (e.g. by a permissive umask) is
	// fixed on open; the side files follow.
	for _, name := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		if _, err := os.Stat(name); err != nil {
			continue
		}
		if err := os.Chmod(name, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := RestrictDBFiles(dbPath); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		st, err := os.Stat(name)
		if err != nil {
			continue
		}
		if st.Mode().Perm() != dbFilePerm {
			t.Errorf("%s has mode %04o, want %04o", filepath.Base(name), st.Mode().Perm(), dbFilePerm)
		}
	}
	db.Close()
	// OpenDB itself applies the restriction.
	if err := os.Chmod(dbPath, 0o666); err != nil {
		t.Fatal(err)
	}
	again, err := OpenDB("")
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if st, _ := os.Stat(dbPath); st.Mode().Perm() != dbFilePerm {
		t.Errorf("after OpenDB the database has mode %04o", st.Mode().Perm())
	}
	// Missing files (the side files exist only while SQLite uses them) are
	// not an error.
	if err := RestrictDBFiles(filepath.Join(t.TempDir(), "absent.db")); err != nil {
		t.Errorf("missing files: %v", err)
	}
}

func TestDataDirPermissionWarning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no POSIX permission bits")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if w := DataDirPermissionWarning(dir); w != "" {
		t.Errorf("private directory warned: %s", w)
	}
	for _, mode := range []os.FileMode{0o750, 0o755, 0o770, 0o707} {
		if err := os.Chmod(dir, mode); err != nil {
			t.Fatal(err)
		}
		w := DataDirPermissionWarning(dir)
		if w == "" || !strings.Contains(w, dir) || !strings.Contains(w, "chmod 700") {
			t.Errorf("mode %04o: warning %q", mode, w)
		}
	}
	if w := DataDirPermissionWarning(filepath.Join(dir, "missing")); w != "" {
		t.Errorf("a missing directory must not warn: %s", w)
	}
}

// TestOpenDBConcurrent opens an un-migrated master database from several
// openers at once (the server starting while "service status" opens it):
// every migration is recorded once and the lock file next to the database
// is gone afterwards.
func TestOpenDBConcurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mailcare.db")
	const openers = 6
	errs := make([]error, openers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range openers {
		wg.Go(func() {
			<-start
			db, err := OpenDB(path)
			if err == nil {
				db.Close()
			}
			errs[i] = err
		})
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("opener %d: %v", i, err)
		}
	}
	db, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var duplicated int
	if err := db.QueryRow(`SELECT COUNT(*) FROM (SELECT version_id FROM goose_db_version GROUP BY version_id HAVING COUNT(*) > 1)`).Scan(&duplicated); err != nil || duplicated != 0 {
		t.Errorf("%d versions recorded more than once (%v)", duplicated, err)
	}
	if _, err := os.Stat(path + dbschema.LockSuffix); !os.IsNotExist(err) {
		t.Errorf("the lock file is left behind: %v", err)
	}
}
