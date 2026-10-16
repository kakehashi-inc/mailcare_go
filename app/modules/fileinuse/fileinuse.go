// Package fileinuse waits out the file operations that fail only because
// another process has the file open: on Windows a reader that still holds a
// file makes its removal, or a rename over it, fail until the reader closes
// it (InUse). Outside Windows an open file can be removed and renamed, so no
// failure is of this kind.
package fileinuse

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

// Interval is the pause between two attempts of an operation that failed
// because the file is in use (a variable so that the tests can shorten it).
var Interval = 100 * time.Millisecond

// inUse is InUse; a variable so that the tests can simulate the failure.
var inUse = InUse

// Retry calls op until it succeeds. A failure because another process has
// the file open (InUse) is retried every Interval for as long as it lasts;
// waiting (when not nil) is called once, with the first such failure. Any
// other failure, and the end of ctx, ends it at once.
func Retry(ctx context.Context, op func() error, waiting func(error)) error {
	reported := false
	for {
		err := op()
		if err == nil || !inUse(err) {
			return err
		}
		if !reported && waiting != nil {
			waiting(err)
		}
		reported = true
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: %w", ctx.Err(), err)
		case <-time.After(Interval):
		}
	}
}

// errorPaths returns the files a failed file operation names (the path of
// an *os.PathError, both paths of an *os.LinkError).
func errorPaths(err error) []string {
	var le *os.LinkError
	if errors.As(err, &le) {
		return []string{le.Old, le.New}
	}
	var pe *os.PathError
	if errors.As(err, &pe) {
		return []string{pe.Path}
	}
	return nil
}
