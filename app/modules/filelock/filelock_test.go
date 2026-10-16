package filelock

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A lock is held against every other open file of the same path until it
// is released.
func TestTryLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	open := func() *os.File {
		t.Helper()
		f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { f.Close() })
		return f
	}
	a, b := open(), open()
	if err := TryLock(a); err != nil {
		t.Fatal(err)
	}
	if err := TryLock(b); !errors.Is(err, ErrHeld) {
		t.Errorf("second lock: %v, want ErrHeld", err)
	}
	if err := Unlock(a); err != nil {
		t.Fatal(err)
	}
	if err := TryLock(b); err != nil {
		t.Errorf("after the release: %v", err)
	}
}
