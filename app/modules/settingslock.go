package modules

import (
	"database/sql"
	"sync"
)

// settingsMu serializes, within the process, every change of the settings
// table together with the reading of the saved values the change is
// validated against: the HTTP settings, the CLI "settings set"
// (ApplySetting), the notification settings, the agent settings and the
// bookkeeping writes. Without it two concurrent requests could each read the
// other's old values, pass the validation of a combination (the DNS check
// switches and resolvers, the agent provider, model and reasoning level, the
// SMTP connection and its stored password) and together store one that the
// validation refuses. One process owns the database: while the server runs
// the CLI hands its commands to it. The lock is not reentrant: what holds it
// (UpdateSettings, withSettingsLock) must not call a function that takes it.
var settingsMu sync.Mutex

// UpdateSettings runs fill with a new SettingsUpdate and saves what it
// gathered in one transaction (persistSettings), all while holding the
// settings lock, so that the saved values fill reads to validate the
// combination cannot change before the writes commit. An error of fill is
// returned as it is and nothing is saved. fill must not call another
// function that changes the settings.
func UpdateSettings(db *sql.DB, fill func(u *SettingsUpdate) error) error {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	var u SettingsUpdate
	if err := fill(&u); err != nil {
		return err
	}
	if settingsFilled != nil {
		settingsFilled()
	}
	return persistSettings(db, u.writes)
}

// settingsFilled, when set (tests only), runs in UpdateSettings between the
// validation and the writes, under the lock.
var settingsFilled func()

// withSettingsLock runs fn while holding the settings lock: the writes of
// the settings that do not go through a SettingsUpdate.
func withSettingsLock(fn func() error) error {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	return fn()
}
