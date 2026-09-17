package modules

import (
	"database/sql"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"mailcare/app/models"
)

// Settings resolution: explicit argument > saved setting > code default. A
// setting is persisted only when it differs from the code default; a value
// equal to the default deletes the row instead, so a later change to a default
// takes effect on its own (a baked-in default would otherwise win).

// PersistSetting stores value under key, or deletes the row when isDefault.
func PersistSetting(db *sql.DB, key, value string, isDefault bool) error {
	if isDefault {
		return models.DeleteSetting(db, key)
	}
	return models.SetSetting(db, key, value)
}

// persistOrLog wraps PersistSetting for callers that cannot fail on it.
func persistOrLog(db *sql.DB, key, value string, isDefault bool) {
	if err := PersistSetting(db, key, value, isDefault); err != nil {
		log.Printf("failed to persist setting %s: %v", key, err)
	}
}

func resolveStr(db *sql.DB, key, arg, fallback string) string {
	if arg != "" {
		return arg
	}
	if saved := models.GetSetting(db, key); saved != "" {
		return saved
	}
	return fallback
}

func resolveInt(db *sql.DB, key string, arg, fallback int) int {
	if arg != 0 {
		return arg
	}
	if saved := models.GetSetting(db, key); saved != "" {
		if v, err := strconv.Atoi(saved); err == nil && v > 0 {
			return v
		}
	}
	return fallback
}

// ResolveWebListen returns the Web listen address (arg > saved > default).
func ResolveWebListen(db *sql.DB, arg string) string {
	return resolveStr(db, SettingWebListen, arg, DefaultWebListenAddr)
}

// ResolveWebPort returns the Web port (arg > saved > default).
func ResolveWebPort(db *sql.DB, arg int) int {
	return resolveInt(db, SettingWebPort, arg, DefaultWebPort)
}

// ResolveCookieTTLHours returns the session cookie lifetime in hours.
func ResolveCookieTTLHours(db *sql.DB) int {
	return resolveInt(db, SettingCookieTTLHours, 0, DefaultCookieTTLHours)
}

// SaveServerSettings persists the listen address and port (non-default only).
func SaveServerSettings(db *sql.DB, webListen string, webPort int) {
	persistOrLog(db, SettingWebListen, webListen, webListen == DefaultWebListenAddr)
	persistOrLog(db, SettingWebPort, strconv.Itoa(webPort), webPort == DefaultWebPort)
}

// --- Workers ---

// ClampWorkers bounds a worker count to 1..MaxWorkers (0 or less means the
// default).
func ClampWorkers(n int) int {
	switch {
	case n <= 0:
		return DefaultWorkers
	case n > MaxWorkers:
		return MaxWorkers
	}
	return n
}

// ParseWorkers parses a worker count given as text (1..MaxWorkers).
func ParseWorkers(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 1 || n > MaxWorkers {
		return 0, fmt.Errorf("workers must be an integer between 1 and %d", MaxWorkers)
	}
	return n, nil
}

// ResolveWorkers returns the number of jobs that run at once (arg > saved >
// default), clamped to 1..MaxWorkers.
func ResolveWorkers(db *sql.DB) int {
	return ClampWorkers(resolveInt(db, SettingWorkers, 0, DefaultWorkers))
}

// SaveWorkers persists the worker count (non-default only).
func SaveWorkers(db *sql.DB, n int) error {
	n = ClampWorkers(n)
	return PersistSetting(db, SettingWorkers, strconv.Itoa(n), n == DefaultWorkers)
}

// ResolveAgentProvider returns the configured agent provider name.
func ResolveAgentProvider(db *sql.DB) string {
	return resolveStr(db, SettingAgentProvider, "", DefaultAgentProvider)
}

// SetAgentProvider persists the agent provider (non-default only). The caller
// validates the name against the registered providers.
func SetAgentProvider(db *sql.DB, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("agent provider must not be empty")
	}
	return PersistSetting(db, SettingAgentProvider, name, name == DefaultAgentProvider)
}

// ResolveAgentEnabled reports whether agent analysis runs automatically after
// a check (default true).
func ResolveAgentEnabled(db *sql.DB) bool {
	return models.GetSetting(db, SettingAgentEnabled) != "0"
}

// SetAgentEnabled persists the agent switch (non-default only).
func SetAgentEnabled(db *sql.DB, enabled bool) error {
	return PersistSetting(db, SettingAgentEnabled, "0", enabled)
}

// ParseBoolSetting accepts 1/0, true/false, yes/no, on/off.
func ParseBoolSetting(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	}
	return false, fmt.Errorf("invalid boolean %q (use 1/0, true/false, yes/no or on/off)", s)
}

// --- Check times ---

// DefaultCheckTimeList returns DefaultCheckTimes as a slice.
func DefaultCheckTimeList() []string {
	times, _ := ParseCheckTimes([]string{DefaultCheckTimes})
	return times
}

// ParseCheckTimes parses check times given as "HH:MM" or "H:MM" entries
// (each entry may itself be a comma-separated list). The result is normalized
// to "HH:MM", sorted and deduplicated. An empty input yields an empty list.
func ParseCheckTimes(inputs []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, input := range inputs {
		for _, raw := range strings.Split(input, ",") {
			s := strings.TrimSpace(raw)
			if s == "" {
				continue
			}
			hh, mm, ok := strings.Cut(s, ":")
			if !ok {
				return nil, fmt.Errorf("invalid check time %q (use HH:MM)", s)
			}
			h, err1 := strconv.Atoi(hh)
			m, err2 := strconv.Atoi(mm)
			if err1 != nil || err2 != nil || len(mm) != 2 || len(hh) == 0 || len(hh) > 2 ||
				h < 0 || h > 23 || m < 0 || m > 59 {
				return nil, fmt.Errorf("invalid check time %q (use HH:MM, 00:00-23:59)", s)
			}
			norm := fmt.Sprintf("%02d:%02d", h, m)
			if !seen[norm] {
				seen[norm] = true
				out = append(out, norm)
			}
		}
	}
	sort.Strings(out)
	if out == nil {
		out = []string{}
	}
	return out, nil
}

// FormatCheckTimes joins normalized check times for storage / display.
func FormatCheckTimes(times []string) string {
	return strings.Join(times, ",")
}

// ResolveCheckTimes returns the configured check times (saved > default). A
// saved value that fails to parse falls back to the default with a log line.
func ResolveCheckTimes(db *sql.DB) []string {
	saved, found, err := models.GetSettingStrict(db, SettingCheckTimes)
	if err != nil {
		log.Printf("failed to read check_times: %v", err)
		return DefaultCheckTimeList()
	}
	if !found {
		return DefaultCheckTimeList()
	}
	times, err := ParseCheckTimes([]string{saved})
	if err != nil {
		log.Printf("ignoring invalid saved check_times %q: %v", saved, err)
		return DefaultCheckTimeList()
	}
	return times
}

// SaveCheckTimes persists normalized check times (non-default only). An empty
// list disables the scheduler and is stored as an empty string.
func SaveCheckTimes(db *sql.DB, times []string) error {
	value := FormatCheckTimes(times)
	return PersistSetting(db, SettingCheckTimes, value, value == DefaultCheckTimes)
}

// NextCheckAt returns the first occurrence of any of the check times (local
// wall clock) strictly after now. ok is false when times is empty.
func NextCheckAt(now time.Time, times []string) (next time.Time, ok bool) {
	now = now.In(time.Local)
	for _, t := range times {
		hh, mm, found := strings.Cut(t, ":")
		if !found {
			continue
		}
		h, err1 := strconv.Atoi(hh)
		m, err2 := strconv.Atoi(mm)
		if err1 != nil || err2 != nil {
			continue
		}
		cand := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, time.Local)
		if !cand.After(now) {
			cand = cand.AddDate(0, 0, 1)
		}
		if !ok || cand.Before(next) {
			next, ok = cand, true
		}
	}
	return next, ok
}
