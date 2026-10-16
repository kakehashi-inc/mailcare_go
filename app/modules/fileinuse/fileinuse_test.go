package fileinuse

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// A failure because the file is in use is retried for as long as it lasts
// (reported once), any other failure ends it at once, and so does the end
// of the context.
func TestRetry(t *testing.T) {
	busy := errors.New("in use")
	savedInterval, savedInUse := Interval, inUse
	Interval = time.Millisecond
	inUse = func(err error) bool { return errors.Is(err, busy) }
	t.Cleanup(func() { Interval, inUse = savedInterval, savedInUse })

	var waits []error
	calls := 0
	err := Retry(context.Background(), func() error {
		calls++
		if calls < 300 {
			return busy
		}
		return nil
	}, func(err error) { waits = append(waits, err) })
	if err != nil || calls != 300 {
		t.Errorf("in use: %v after %d calls, want success at the 300th", err, calls)
	}
	if len(waits) != 1 || !errors.Is(waits[0], busy) {
		t.Errorf("waiting reported %v, want once", waits)
	}
	calls = 0
	other := errors.New("permission")
	if err := Retry(context.Background(), func() error { calls++; return other }, nil); !errors.Is(err, other) || calls != 1 {
		t.Errorf("other failure: %v after %d calls, want it at once", err, calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	if err := Retry(ctx, func() error { return busy }, nil); !errors.Is(err, context.Canceled) || !errors.Is(err, busy) {
		t.Errorf("canceled wait: %v", err)
	}
}

// The files a failed operation names are found in the error.
func TestErrorPaths(t *testing.T) {
	dir := t.TempDir()
	missing := dir + "/missing"
	_, err := os.Open(missing)
	if got := errorPaths(err); len(got) != 1 || got[0] != missing {
		t.Errorf("open: %v", got)
	}
	err = os.Rename(missing, dir+"/other")
	if got := errorPaths(err); len(got) != 2 || got[0] != missing || got[1] != dir+"/other" {
		t.Errorf("rename: %v", got)
	}
	if got := errorPaths(errors.New("plain")); got != nil {
		t.Errorf("plain error: %v", got)
	}
}
