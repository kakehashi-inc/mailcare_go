// Package filelock takes exclusive locks on open files through the
// operating system (flock on Unix, LockFileEx on Windows). The operating
// system releases a lock when its file is closed or its process ends, also
// after a crash, so a lock never goes stale and needs no age or timeout.
package filelock

import "errors"

// ErrHeld is returned by TryLock when another open file holds the lock
// (another process, or another handle of this one).
var ErrHeld = errors.New("the lock is held by another process")
