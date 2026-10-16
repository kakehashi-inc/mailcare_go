package modules

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"mailcare/app/modules/filelock"
)

// Server lock (the system design document (Documents) 9.1): one server per
// data directory. The server holds an exclusive lock on ServerLockFile for
// its whole lifetime (filelock: flock on Unix, LockFileEx on Windows); the
// operating system releases it when the process ends, also after a crash. A
// second start for the same data directory fails before it opens the
// database or touches the control token of the running server.

// ServerLockFile is the lock file of the running server in the data
// directory. A clean shutdown removes it; one left by a crash is simply
// locked again by the next start.
const ServerLockFile = "server.lock"

// ErrServerRunning is returned by AcquireServerLock while another process
// holds the lock.
var ErrServerRunning = errors.New("another MailCare server is running for this data directory")

// AcquireServerLock takes the server lock of the data directory and returns
// the function that releases it (it removes the file while still locked,
// then unlocks and closes it). It never waits: a lock held by another
// process yields ErrServerRunning (with the data directory named).
func AcquireServerLock(dataDir string) (release func(), err error) {
	path := filepath.Join(dataDir, ServerLockFile)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open the server lock %s: %w", path, err)
	}
	if err := filelock.TryLock(f); err != nil {
		f.Close()
		if errors.Is(err, filelock.ErrHeld) {
			return nil, fmt.Errorf("%w (%s); stop it first", ErrServerRunning, dataDir)
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	// The process id helps an operator who finds the file; the lock itself
	// is what counts.
	if err := f.Truncate(0); err == nil {
		_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
	}
	return func() {
		// Removed while still locked, then released. Windows refuses to
		// remove an open file: there it goes once closed.
		removeErr := os.Remove(path)
		_ = filelock.Unlock(f)
		_ = f.Close()
		if removeErr != nil {
			_ = os.Remove(path)
		}
	}, nil
}
