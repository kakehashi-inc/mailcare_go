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
	"mailcare/app/modules/agent"
	"mailcare/app/modules/message"
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
		return 0, message.New("validation.common.numberOutOfRange", fmt.Sprintf("workers must be an integer between 1 and %d", MaxWorkers)).
			With("min", 1).With("max", MaxWorkers)
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
// validates the name against the registered providers. Model names and
// reasoning levels differ between providers, so switching to another
// provider clears both (callers that set them too set them afterwards).
func SetAgentProvider(db *sql.DB, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errAgentProviderEmpty
	}
	if name != ResolveAgentProvider(db) {
		if err := models.DeleteSetting(db, SettingAgentModel); err != nil {
			return err
		}
		if err := models.DeleteSetting(db, SettingAgentReasoningEffort); err != nil {
			return err
		}
	}
	return PersistSetting(db, SettingAgentProvider, name, name == DefaultAgentProvider)
}

// ResolveAgentModel returns the model passed to the agent CLI ("" = the
// CLI's own default). A stored value that is no longer valid is ignored.
func ResolveAgentModel(db *sql.DB) string {
	model := strings.TrimSpace(models.GetSetting(db, SettingAgentModel))
	if agent.ValidateModel(model) != nil {
		return ""
	}
	return model
}

// SetAgentModel validates and persists the agent model ("" restores the CLI
// default by removing the setting). The saved reasoning level must stay
// valid for the new model (CheckAgentSettings); otherwise nothing is saved.
func SetAgentModel(db *sql.DB, model string) error {
	model = strings.TrimSpace(model)
	if err := agent.ValidateModel(model); err != nil {
		return err
	}
	if err := CheckAgentSettings(ResolveAgentProvider(db), model, ResolveAgentReasoningEffort(db)); err != nil {
		return err
	}
	return PersistSetting(db, SettingAgentModel, model, model == "")
}

// ResolveAgentReasoningEffort returns the reasoning level passed to the
// agent CLI ("" = the CLI's own setting). A stored value of the wrong shape
// is ignored.
func ResolveAgentReasoningEffort(db *sql.DB) string {
	level := strings.TrimSpace(models.GetSetting(db, SettingAgentReasoningEffort))
	if agent.ValidateReasoningEffort(level) != nil {
		return ""
	}
	return level
}

// SetAgentReasoningEffort validates the reasoning level against the
// configured provider and model (CheckAgentSettings) and persists it (""
// restores the CLI's own setting by removing the setting).
func SetAgentReasoningEffort(db *sql.DB, level string) error {
	level = strings.TrimSpace(level)
	if err := CheckAgentSettings(ResolveAgentProvider(db), ResolveAgentModel(db), level); err != nil {
		return err
	}
	return PersistSetting(db, SettingAgentReasoningEffort, level, level == "")
}

// ResolveAgentLimitedUntil returns when the agent's usage limit is lifted
// (ok false when no limit with a known end is recorded).
func ResolveAgentLimitedUntil(db *sql.DB) (time.Time, bool) {
	v := strings.TrimSpace(models.GetSetting(db, SettingAgentLimitedUntil))
	if v == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// SetAgentLimitedUntil records the end of the agent's usage limit; the zero
// time removes the record.
func SetAgentLimitedUntil(db *sql.DB, until time.Time) error {
	return PersistSetting(db, SettingAgentLimitedUntil, until.UTC().Format(time.RFC3339), until.IsZero())
}

// SaveAgentSettings changes any of the agent provider, model and reasoning
// level at once (nil = unchanged): the resulting combination is checked
// first (CheckAgentSettings) and nothing is saved when it is refused. A
// provider change without a model or level clears them, as SetAgentProvider
// does. The provider name must be registered (checked by the caller).
func SaveAgentSettings(db *sql.DB, provider, model, level *string) error {
	finalProvider := ResolveAgentProvider(db)
	finalModel := ResolveAgentModel(db)
	finalLevel := ResolveAgentReasoningEffort(db)
	if provider != nil {
		name := strings.TrimSpace(*provider)
		if name == "" {
			return errAgentProviderEmpty
		}
		if name != finalProvider {
			finalModel, finalLevel = "", ""
		}
		finalProvider = name
	}
	if model != nil {
		finalModel = strings.TrimSpace(*model)
	}
	if level != nil {
		finalLevel = strings.TrimSpace(*level)
	}
	if err := CheckAgentSettings(finalProvider, finalModel, finalLevel); err != nil {
		return err
	}
	if err := PersistSetting(db, SettingAgentProvider, finalProvider, finalProvider == DefaultAgentProvider); err != nil {
		return err
	}
	if err := PersistSetting(db, SettingAgentModel, finalModel, finalModel == ""); err != nil {
		return err
	}
	return PersistSetting(db, SettingAgentReasoningEffort, finalLevel, finalLevel == "")
}

var errAgentProviderEmpty = message.New("validation.agent.providerUnknown", "agent provider must not be empty")

// CheckAgentSettings validates a combination of agent provider, model and
// reasoning level before any of them is saved: the model's shape and the
// reasoning level against what the model accepts (agent.CheckReasoningEffort;
// a level the model is known not to accept is refused).
func CheckAgentSettings(provider, model, level string) error {
	if err := agent.ValidateModel(strings.TrimSpace(model)); err != nil {
		return err
	}
	return agent.CheckReasoningEffort(provider, model, level)
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

// ValidateAgentKeepDays checks the retention of the agent run directories
// in days (MinAgentKeepDays..MaxAgentKeepDays).
func ValidateAgentKeepDays(n int) error {
	if n < MinAgentKeepDays || n > MaxAgentKeepDays {
		return agentKeepDaysInvalid()
	}
	return nil
}

func agentKeepDaysInvalid() error {
	return message.New("validation.common.numberOutOfRange",
		fmt.Sprintf("agent_keep_days must be an integer between %d and %d", MinAgentKeepDays, MaxAgentKeepDays)).
		With("min", MinAgentKeepDays).With("max", MaxAgentKeepDays)
}

// ParseAgentKeepDays parses the retention given as text.
func ParseAgentKeepDays(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0, agentKeepDaysInvalid()
	}
	if err := ValidateAgentKeepDays(n); err != nil {
		return 0, err
	}
	return n, nil
}

// ResolveAgentKeepDays returns how many days the workspace directory of an
// analysis run is kept (saved > default). A saved value outside the bounds
// falls back to the default.
func ResolveAgentKeepDays(db *sql.DB) int {
	n := resolveInt(db, SettingAgentKeepDays, 0, DefaultAgentKeepDays)
	if ValidateAgentKeepDays(n) != nil {
		return DefaultAgentKeepDays
	}
	return n
}

// SaveAgentKeepDays persists the retention (non-default only).
func SaveAgentKeepDays(db *sql.DB, n int) error {
	if err := ValidateAgentKeepDays(n); err != nil {
		return err
	}
	return PersistSetting(db, SettingAgentKeepDays, strconv.Itoa(n), n == DefaultAgentKeepDays)
}

// --- Mail retention ---

// ValidateMailKeepDays checks the retention of fetched mails in days
// (MinMailKeepDays..MaxMailKeepDays).
func ValidateMailKeepDays(n int) error {
	if n < MinMailKeepDays || n > MaxMailKeepDays {
		return mailKeepDaysInvalid()
	}
	return nil
}

func mailKeepDaysInvalid() error {
	return message.New("validation.common.numberOutOfRange",
		fmt.Sprintf("mail_keep_days must be an integer between %d and %d", MinMailKeepDays, MaxMailKeepDays)).
		With("min", MinMailKeepDays).With("max", MaxMailKeepDays)
}

// ParseMailKeepDays parses the retention given as text.
func ParseMailKeepDays(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0, mailKeepDaysInvalid()
	}
	if err := ValidateMailKeepDays(n); err != nil {
		return 0, err
	}
	return n, nil
}

// ResolveMailKeepDays returns how many days a fetched mail is kept (saved >
// default). A saved value outside the bounds falls back to the default.
func ResolveMailKeepDays(db *sql.DB) int {
	n := resolveInt(db, SettingMailKeepDays, 0, DefaultMailKeepDays)
	if ValidateMailKeepDays(n) != nil {
		return DefaultMailKeepDays
	}
	return n
}

// SaveMailKeepDays persists the retention (non-default only).
func SaveMailKeepDays(db *sql.DB, n int) error {
	if err := ValidateMailKeepDays(n); err != nil {
		return err
	}
	return PersistSetting(db, SettingMailKeepDays, strconv.Itoa(n), n == DefaultMailKeepDays)
}

// --- Cleanup time ---

// ParseCleanupTime checks one HH:MM time (local wall clock) and returns it
// normalized.
func ParseCleanupTime(s string) (string, error) {
	times, err := ParseCheckTimes([]string{s})
	if err != nil || len(times) != 1 {
		return "", message.New("validation.common.timeFormat", fmt.Sprintf("invalid cleanup_time %q (use one HH:MM time, 00:00-23:59)", strings.TrimSpace(s)))
	}
	return times[0], nil
}

// ResolveCleanupTime returns the time of the daily cleanup (saved >
// default). A saved value that fails to parse falls back to the default
// with a log line.
func ResolveCleanupTime(db *sql.DB) string {
	saved, found, err := models.GetSettingStrict(db, SettingCleanupTime)
	if err != nil {
		log.Printf("failed to read cleanup_time: %v", err)
		return DefaultCleanupTime
	}
	if !found {
		return DefaultCleanupTime
	}
	t, err := ParseCleanupTime(saved)
	if err != nil {
		log.Printf("ignoring invalid saved cleanup_time %q: %v", saved, err)
		return DefaultCleanupTime
	}
	return t
}

// SaveCleanupTime persists the time of the daily cleanup (non-default
// only) and returns it normalized.
func SaveCleanupTime(db *sql.DB, s string) (string, error) {
	t, err := ParseCleanupTime(s)
	if err != nil {
		return "", err
	}
	return t, PersistSetting(db, SettingCleanupTime, t, t == DefaultCleanupTime)
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
				return nil, message.New("validation.common.timeFormat", fmt.Sprintf("invalid check time %q (use HH:MM)", s))
			}
			h, err1 := strconv.Atoi(hh)
			m, err2 := strconv.Atoi(mm)
			if err1 != nil || err2 != nil || len(mm) != 2 || len(hh) == 0 || len(hh) > 2 ||
				h < 0 || h > 23 || m < 0 || m > 59 {
				return nil, message.New("validation.common.timeFormat", fmt.Sprintf("invalid check time %q (use HH:MM, 00:00-23:59)", s))
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

// CheckTimesNone is the word "settings set check_times" accepts to disable
// the automatic checks, and what an empty list is displayed as.
const CheckTimesNone = "none"

// DisplayCheckTimes formats check times for people: "none" when disabled.
func DisplayCheckTimes(times []string) string {
	if len(times) == 0 {
		return CheckTimesNone
	}
	return FormatCheckTimes(times)
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
