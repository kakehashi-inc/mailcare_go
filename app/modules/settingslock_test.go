package modules

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestConcurrentDNSCheckSaves races the reproduced pair of updates (run with
// -race): from the check on with the OS resolver only, one update switches
// the OS-resolver-only mode off (valid with the saved resolvers), the other
// clears the resolvers (valid with the saved mode), one through the CLI and
// one as the HTTP settings do. Each alone is accepted; together they would
// store a combination ValidateDNSCheck refuses (on, no resolver, the mode
// off). The settings lock makes the second read the values the first saved,
// so it is refused and the stored combination stays valid. The hook holds
// the first update between its validation and its writes for a while, which
// without the lock lets the second validate against the old values every
// time.
func TestConcurrentDNSCheckSaves(t *testing.T) {
	for round := range 3 {
		db := newTestDB(t)
		on, only := true, true
		if err := SaveDNSCheck(db, &on, nil, &only); err != nil {
			t.Fatal(err)
		}
		var filled atomic.Int32
		settingsFilled = func() {
			filled.Add(1)
			deadline := time.Now().Add(300 * time.Millisecond)
			for filled.Load() < 2 && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
		}
		t.Cleanup(func() { settingsFilled = nil })

		var wg sync.WaitGroup
		errs := make([]error, 2)
		start := make(chan struct{})
		wg.Go(func() {
			<-start
			_, errs[0] = ApplySetting(db, SettingDNSCheckSystemResolverOnly, "0", nil)
		})
		wg.Go(func() {
			<-start
			errs[1] = UpdateSettings(db, func(u *SettingsUpdate) error {
				return u.DNSCheck(db, nil, &[]string{}, nil)
			})
		})
		close(start)
		wg.Wait()
		settingsFilled = nil

		cfg := ResolveDNSCheck(db)
		if err := ValidateDNSCheck(cfg.Enabled, cfg.SystemOnly, cfg.Resolvers); err != nil {
			t.Fatalf("round %d: stored %+v, which the validation refuses (%v); errors %v", round, cfg, err, errs)
		}
		if (errs[0] == nil) == (errs[1] == nil) {
			t.Errorf("round %d: errors %v; want exactly one update refused", round, errs)
		}
	}
}

// TestWithSettingsLockExcludesUpdates: while a write holds the settings
// lock (withSettingsLock), an update (UpdateSettings) waits for it to end.
func TestWithSettingsLockExcludesUpdates(t *testing.T) {
	db := newTestDB(t)
	entered, release := make(chan struct{}), make(chan struct{})
	held := make(chan error, 1)
	go func() {
		held <- withSettingsLock(func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	updated := make(chan error, 1)
	go func() {
		updated <- UpdateSettings(db, func(u *SettingsUpdate) error {
			u.Workers(2)
			return nil
		})
	}()
	select {
	case err := <-updated:
		close(release)
		t.Fatalf("an update ended while the settings lock was held (err %v)", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-held; err != nil {
		t.Fatal(err)
	}
	if err := <-updated; err != nil {
		t.Fatal(err)
	}
}
