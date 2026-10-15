package modules

import (
	"database/sql"
	"fmt"
	"log"
	"net/netip"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"mailcare/app/models"
	"mailcare/app/modules/agent"
	"mailcare/app/modules/mailengine"
	"mailcare/app/modules/wording"
)

// Settings resolution: explicit argument > saved setting > code default. A
// setting is persisted only when it differs from the code default; a value
// equal to the default deletes the row instead, so a later change to a default
// takes effect on its own (a baked-in default would otherwise win).

// PersistSetting stores value under key, or deletes the row when isDefault.
// db may be a transaction (persistSettings).
func PersistSetting(db models.Execer, key, value string, isDefault bool) error {
	if isDefault {
		return models.DeleteSetting(db, key)
	}
	return models.SetSetting(db, key, value)
}

// settingWrite is one change of the settings table: value stored under key,
// or the row removed when isDefault (PersistSetting).
type settingWrite struct {
	key, value string
	isDefault  bool
}

// persistSettings applies writes in one transaction of the master
// database: all of them are saved, or none when one fails (the transaction
// is rolled back), so a failure halfway never leaves a combination the
// validation would refuse. The values are computed, and the saved values
// they depend on read, before: the transaction only writes, so it holds no
// read snapshot that another writer could make stale.
func persistSettings(db *sql.DB, writes []settingWrite) error {
	if len(writes) == 0 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, w := range writes {
		if err := PersistSetting(tx, w.key, w.value, w.isDefault); err != nil {
			return fmt.Errorf("save %s: %w", w.key, err)
		}
	}
	return tx.Commit()
}

// SettingsUpdate gathers the changes of several settings made by one
// request, so that they are saved together (Save: one transaction, all or
// nothing). Every method validates its input, against the saved values
// where the result depends on them, and returns the error that refuses it
// (nothing is added then); nothing is written before Save. The zero value
// is ready to use. The Save* functions of the single settings go through it
// as well.
type SettingsUpdate struct {
	writes []settingWrite
}

// set adds the write of one setting.
func (u *SettingsUpdate) set(key, value string, isDefault bool) {
	u.writes = append(u.writes, settingWrite{key: key, value: value, isDefault: isDefault})
}

// Save writes every change gathered in one transaction (nothing when one
// write fails).
func (u *SettingsUpdate) Save(db *sql.DB) error {
	return persistSettings(db, u.writes)
}

// Workers sets the worker count (clamped like SaveWorkers).
func (u *SettingsUpdate) Workers(n int) {
	n = ClampWorkers(n)
	u.set(SettingWorkers, strconv.Itoa(n), n == DefaultWorkers)
}

// CheckTimes sets the check times, already normalized (ParseCheckTimes); an
// empty list disables the scheduler (see SaveCheckTimes).
func (u *SettingsUpdate) CheckTimes(times []string) {
	value := FormatCheckTimes(times)
	u.set(SettingCheckTimes, value, value == DefaultCheckTimes)
}

// AgentSettings sets any of the agent provider, model and reasoning level
// (nil = unchanged), checked as the combination they give with the saved
// values (see SaveAgentSettings).
func (u *SettingsUpdate) AgentSettings(db *sql.DB, provider, model, level *string) error {
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
	u.set(SettingAgentProvider, finalProvider, finalProvider == DefaultAgentProvider)
	u.set(SettingAgentModel, finalModel, finalModel == "")
	u.set(SettingAgentReasoningEffort, finalLevel, finalLevel == "")
	return nil
}

// AgentEnabled sets the agent switch.
func (u *SettingsUpdate) AgentEnabled(enabled bool) {
	u.set(SettingAgentEnabled, "0", enabled)
}

// AgentKeepDays sets the retention of the agent run directories.
func (u *SettingsUpdate) AgentKeepDays(n int) error {
	if err := ValidateAgentKeepDays(n); err != nil {
		return err
	}
	u.set(SettingAgentKeepDays, strconv.Itoa(n), n == DefaultAgentKeepDays)
	return nil
}

// MailKeepDays sets the retention of the fetched mails.
func (u *SettingsUpdate) MailKeepDays(n int) error {
	if err := ValidateMailKeepDays(n); err != nil {
		return err
	}
	u.set(SettingMailKeepDays, strconv.Itoa(n), n == DefaultMailKeepDays)
	return nil
}

// CleanupTime sets the time of the daily cleanup and returns it
// normalized.
func (u *SettingsUpdate) CleanupTime(s string) (string, error) {
	t, err := ParseCleanupTime(s)
	if err != nil {
		return "", err
	}
	u.set(SettingCleanupTime, t, t == DefaultCleanupTime)
	return t, nil
}

// RecheckDays sets the re-check days of the given categories, merged with
// the saved ones of the others (see SaveRecheckDays).
func (u *SettingsUpdate) RecheckDays(db *sql.DB, days map[string]int) error {
	if err := ValidateRecheckDays(days); err != nil {
		return err
	}
	merged := ResolveRecheckDays(db)
	for c, n := range days {
		merged[c] = n
	}
	value := FormatRecheckDays(merged)
	u.set(SettingRecheckDays, value, value == "")
	return nil
}

// OwnSendingIPv4 sets the own sending IPv4 list (see SaveOwnSendingIPv4).
func (u *SettingsUpdate) OwnSendingIPv4(entries []string) error {
	return u.list(ownSendingIPv4Setting, entries)
}

// OwnSendingIPv6 sets the own sending IPv6 list.
func (u *SettingsUpdate) OwnSendingIPv6(entries []string) error {
	return u.list(ownSendingIPv6Setting, entries)
}

// OwnSendingHosts sets the own sending host names.
func (u *SettingsUpdate) OwnSendingHosts(entries []string) error {
	return u.list(ownSendingHostsSetting, entries)
}

// list validates the entries of a list setting and sets them one per line
// (an empty list removes the setting).
func (u *SettingsUpdate) list(ls listSetting, entries []string) error {
	list, err := ls.parse(entries)
	if err != nil {
		return err
	}
	value := strings.Join(list, "\n")
	u.set(ls.key, value, value == "")
	return nil
}

// DNSCheck sets any of the three settings of the DNS lookup failure
// verification (nil = unchanged), checked as the combination they give
// with the saved values (see SaveDNSCheck).
func (u *SettingsUpdate) DNSCheck(db *sql.DB, enabled *bool, resolvers *[]string, systemOnly *bool) error {
	cfg, err := CheckDNSCheckSettings(db, enabled, resolvers, systemOnly)
	if err != nil {
		return err
	}
	if enabled != nil {
		// On is the default: "0" is stored only when off.
		u.set(SettingDNSCheckEnabled, "0", cfg.Enabled)
	}
	if resolvers != nil {
		u.set(SettingDNSCheckResolvers, strings.Join(cfg.Resolvers, "\n"), slices.Equal(cfg.Resolvers, DefaultDNSCheckResolvers()))
	}
	if systemOnly != nil {
		// Off is the default: "1" is stored only when on.
		u.set(SettingDNSCheckSystemResolverOnly, "1", !cfg.SystemOnly)
	}
	return nil
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
		return 0, wording.New("validation.common.numberOutOfRange", fmt.Sprintf("workers must be an integer between 1 and %d", MaxWorkers)).
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
	var u SettingsUpdate
	u.Workers(n)
	return u.Save(db)
}

// ResolveAgentProvider returns the configured agent provider name.
func ResolveAgentProvider(db *sql.DB) string {
	return resolveStr(db, SettingAgentProvider, "", DefaultAgentProvider)
}

// SetAgentProvider persists the agent provider (non-default only). The caller
// validates the name against the registered providers. Model names and
// reasoning levels differ between providers, so switching to another
// provider clears both (callers that set them too set them afterwards), in
// the same transaction as the provider.
func SetAgentProvider(db *sql.DB, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errAgentProviderEmpty
	}
	var u SettingsUpdate
	if name != ResolveAgentProvider(db) {
		u.set(SettingAgentModel, "", true)
		u.set(SettingAgentReasoningEffort, "", true)
	}
	u.set(SettingAgentProvider, name, name == DefaultAgentProvider)
	return u.Save(db)
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
// does. The provider name must be registered (checked by the caller). The
// three are saved together (SettingsUpdate).
func SaveAgentSettings(db *sql.DB, provider, model, level *string) error {
	var u SettingsUpdate
	if err := u.AgentSettings(db, provider, model, level); err != nil {
		return err
	}
	return u.Save(db)
}

var errAgentProviderEmpty = wording.New("validation.agent.providerUnknown", "agent provider must not be empty")

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
	var u SettingsUpdate
	u.AgentEnabled(enabled)
	return u.Save(db)
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
	return wording.New("validation.common.numberOutOfRange",
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
	var u SettingsUpdate
	if err := u.AgentKeepDays(n); err != nil {
		return err
	}
	return u.Save(db)
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
	return wording.New("validation.common.numberOutOfRange",
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
	var u SettingsUpdate
	if err := u.MailKeepDays(n); err != nil {
		return err
	}
	return u.Save(db)
}

// --- Re-check days ---

// recheckDaysInvalid is the error of a re-check day count out of bounds.
func recheckDaysInvalid(category string) error {
	return wording.New("validation.common.numberOutOfRange",
		fmt.Sprintf("recheck days of %s must be an integer between %d and %d", category,
			mailengine.MinRecheckDays, mailengine.MaxRecheckDays)).
		With("min", mailengine.MinRecheckDays).With("max", mailengine.MaxRecheckDays)
}

// ValidateRecheckDays checks re-check days per category: every category
// must be known and every count within MinRecheckDays..MaxRecheckDays.
func ValidateRecheckDays(days map[string]int) error {
	for category, n := range days {
		if !IsKnownCategory(category) {
			return wording.New("system.invalidRequest", fmt.Sprintf("unknown category %q in recheck_days", category))
		}
		if n < mailengine.MinRecheckDays || n > mailengine.MaxRecheckDays {
			return recheckDaysInvalid(category)
		}
	}
	return nil
}

// ParseRecheckDays parses re-check days given as text: comma-separated
// "category=days" pairs (the saved form and the CLI's; "" = none).
func ParseRecheckDays(s string) (map[string]int, error) {
	days := map[string]int{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		category, value, ok := strings.Cut(part, "=")
		category = strings.TrimSpace(category)
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if !ok || err != nil {
			return nil, wording.New("system.invalidRequest",
				fmt.Sprintf("invalid recheck_days entry %q (use category=days, comma-separated)", part))
		}
		days[category] = n
	}
	return days, ValidateRecheckDays(days)
}

// ResolveRecheckDays returns the re-check days of every category (saved >
// default). A saved value that fails to parse falls back to the defaults
// with a log line.
func ResolveRecheckDays(db *sql.DB) mailengine.RecheckDays {
	days := mailengine.RecheckDays{}
	for _, c := range KnownCategories() {
		days[c] = mailengine.DefaultRecheckDays(c)
	}
	saved := models.GetSetting(db, SettingRecheckDays)
	if saved == "" {
		return days
	}
	parsed, err := ParseRecheckDays(saved)
	if err != nil {
		log.Printf("ignoring invalid saved recheck_days %q: %v", saved, err)
		return days
	}
	for c, n := range parsed {
		days[c] = n
	}
	return days
}

// FormatRecheckDays renders the re-check days that differ from the defaults
// as the saved form ("" when every category has its default).
func FormatRecheckDays(days map[string]int) string {
	var parts []string
	for _, c := range KnownCategories() {
		if n, ok := days[c]; ok && n != mailengine.DefaultRecheckDays(c) {
			parts = append(parts, c+"="+strconv.Itoa(n))
		}
	}
	return strings.Join(parts, ",")
}

// SaveRecheckDays persists the re-check days of the given categories (the
// others keep their current value); only values that differ from the
// defaults are stored.
func SaveRecheckDays(db *sql.DB, days map[string]int) error {
	var u SettingsUpdate
	if err := u.RecheckDays(db, days); err != nil {
		return err
	}
	return u.Save(db)
}

// --- Cleanup time ---

// ParseCleanupTime checks one HH:MM time (local wall clock) and returns it
// normalized.
func ParseCleanupTime(s string) (string, error) {
	times, err := ParseCheckTimes([]string{s})
	if err != nil || len(times) != 1 {
		return "", wording.New("validation.common.timeFormat", fmt.Sprintf("invalid cleanup_time %q (use one HH:MM time, 00:00-23:59)", strings.TrimSpace(s)))
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
	var u SettingsUpdate
	t, err := u.CleanupTime(s)
	if err != nil {
		return "", err
	}
	return t, u.Save(db)
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
				return nil, wording.New("validation.common.timeFormat", fmt.Sprintf("invalid check time %q (use HH:MM)", s))
			}
			h, err1 := strconv.Atoi(hh)
			m, err2 := strconv.Atoi(mm)
			if err1 != nil || err2 != nil || len(mm) != 2 || len(hh) == 0 || len(hh) > 2 ||
				h < 0 || h > 23 || m < 0 || m > 59 {
				return nil, wording.New("validation.common.timeFormat", fmt.Sprintf("invalid check time %q (use HH:MM, 00:00-23:59)", s))
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
	var u SettingsUpdate
	u.CheckTimes(times)
	return u.Save(db)
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

// --- Own sending servers and DNS lookup failure verification ---

// OwnSending is our own sending servers: the saved lists (own_sending_ipv4,
// own_sending_ipv6 and own_sending_hosts: the sources not tied to a mailbox,
// such as an application that delivers directly or an external delivery
// service) and the SMTP hosts of the mailboxes (MailboxHosts). They tell
// whether a return notice is about mail we sent and whether a DNS lookup
// failure was reported by our own server (mailengine, design document "mail
// classification" 2.6 and 4.4). Every list is normalized (canonical IP and
// CIDR text, host names lower-case without the trailing dot) and never nil.
//
// MailboxesWithoutHost is derived, for the settings screen: the addresses
// of the mailboxes (enabled or not, in the order of the list) for which no
// host name is configured, so that the backscatter rule (2.6), which is
// switched on per mailbox by a host name, does not apply to them. It is
// empty while own_sending_hosts holds a host name, which counts for every
// mailbox.
type OwnSending struct {
	IPv4, IPv6, Hosts    []string
	MailboxHosts         []string
	MailboxesWithoutHost []string
}

// AllHosts returns the host names of our own sending servers: the saved
// own_sending_hosts first, then the SMTP hosts of the mailboxes, without
// repeats. Never nil.
func (o OwnSending) AllHosts() []string {
	seen := map[string]bool{}
	out := []string{}
	for _, list := range [][]string{o.Hosts, o.MailboxHosts} {
		for _, h := range list {
			if !seen[h] {
				seen[h] = true
				out = append(out, h)
			}
		}
	}
	return out
}

// DNSCheckConfig is the configuration of the verification of DNS lookup
// failures. Resolvers is never nil; while SystemOnly is on it is not used
// (only the OS resolver is) but kept.
type DNSCheckConfig struct {
	Enabled    bool
	Resolvers  []string
	SystemOnly bool
}

// DNSCheckResolversNone is the word "settings set dns_check_resolvers"
// accepts to save an empty list ("" restores the default list), and what an
// empty list is displayed as.
const DNSCheckResolversNone = "none"

// listSetting is a setting that holds a list, stored one entry per line:
// how an entry is normalized (ok false = invalid), the error of an invalid
// entry and the bound on the number of entries.
type listSetting struct {
	key       string
	max       int
	normalize func(s string) (string, bool)
	invalid   func(line int, value string) error
	tooMany   func() error
}

var (
	ownSendingIPv4Setting = listSetting{
		key: SettingOwnSendingIPv4, max: MaxOwnSendingEntries,
		normalize: func(s string) (string, bool) { return normalizeIPOrPrefix(s, false) },
		invalid: func(line int, value string) error {
			return listEntryInvalid("validation.ownSending.ipv4Invalid", SettingOwnSendingIPv4,
				"an IPv4 address or CIDR (loopback and 0.0.0.0 are not accepted)", line, value)
		},
		tooMany: func() error { return ownSendingTooMany(SettingOwnSendingIPv4) },
	}
	ownSendingIPv6Setting = listSetting{
		key: SettingOwnSendingIPv6, max: MaxOwnSendingEntries,
		normalize: func(s string) (string, bool) { return normalizeIPOrPrefix(s, true) },
		invalid: func(line int, value string) error {
			return listEntryInvalid("validation.ownSending.ipv6Invalid", SettingOwnSendingIPv6,
				"an IPv6 address or CIDR (loopback, ::, zones and IPv4-mapped addresses are not accepted)", line, value)
		},
		tooMany: func() error { return ownSendingTooMany(SettingOwnSendingIPv6) },
	}
	ownSendingHostsSetting = listSetting{
		key: SettingOwnSendingHosts, max: MaxOwnSendingEntries,
		normalize: normalizeOwnHost,
		invalid: func(line int, value string) error {
			return listEntryInvalid("validation.ownSending.hostInvalid", SettingOwnSendingHosts,
				"a host name (letters, digits and hyphens; IP addresses and localhost are not accepted)", line, value)
		},
		tooMany: func() error { return ownSendingTooMany(SettingOwnSendingHosts) },
	}
	dnsCheckResolversSetting = listSetting{
		key: SettingDNSCheckResolvers, max: MaxDNSCheckResolvers,
		normalize: normalizeResolver,
		invalid: func(line int, value string) error {
			return listEntryInvalid("validation.dnsCheck.resolverInvalid", SettingDNSCheckResolvers,
				"the IP address of a resolver (no port or CIDR; loopback and unspecified addresses are not accepted)", line, value)
		},
		tooMany: func() error {
			return wording.New("validation.dnsCheck.resolversTooMany",
				fmt.Sprintf("%s accepts at most %d resolvers", SettingDNSCheckResolvers, MaxDNSCheckResolvers)).
				With("max", MaxDNSCheckResolvers)
		},
	}
	// ownSendingSettings maps the keys of the own sending lists to their
	// rules.
	ownSendingSettings = map[string]listSetting{
		SettingOwnSendingIPv4:  ownSendingIPv4Setting,
		SettingOwnSendingIPv6:  ownSendingIPv6Setting,
		SettingOwnSendingHosts: ownSendingHostsSetting,
	}
)

var errDNSCheckNoResolver = wording.New("validation.dnsCheck.noResolver",
	fmt.Sprintf("%s must list at least one resolver while %s is on and %s is off",
		SettingDNSCheckResolvers, SettingDNSCheckEnabled, SettingDNSCheckSystemResolverOnly))

// listEntryInvalid is the error of an invalid entry of a list setting; line
// is the 1-based line (the position in the comma-separated CLI value).
func listEntryInvalid(key, setting, what string, line int, value string) error {
	value = clipEntry(value)
	return wording.New(key, fmt.Sprintf("%s: entry %d (%q) is not %s", setting, line, value, what)).
		With("line", line).With("value", value)
}

func ownSendingTooMany(setting string) error {
	return wording.New("validation.ownSending.tooMany",
		fmt.Sprintf("%s accepts at most %d entries", setting, MaxOwnSendingEntries)).With("max", MaxOwnSendingEntries)
}

// clipEntry shortens an entry quoted in an error message.
func clipEntry(s string) string {
	const maxRunes = 64
	if r := []rune(s); len(r) > maxRunes {
		return string(r[:maxRunes]) + "..."
	}
	return s
}

// listEntry is one non-blank entry of a list setting and its 1-based line.
type listEntry struct {
	line  int
	value string
}

// splitListEntries splits the inputs of a list setting into trimmed
// entries. Every input may hold several lines; blank lines are skipped but
// counted, so line is the row of the entry in a text area.
func splitListEntries(inputs []string) []listEntry {
	var out []listEntry
	line := 0
	for _, input := range inputs {
		for _, raw := range strings.Split(input, "\n") {
			line++
			if s := strings.TrimSpace(raw); s != "" {
				out = append(out, listEntry{line: line, value: s})
			}
		}
	}
	return out
}

// collect normalizes the entries of the list and drops the duplicates
// (keeping the first). Strict (input to save) stops at the first invalid
// entry and checks the bound; otherwise (a saved value) an entry that is
// not valid any more is skipped with a log line instead of discarding the
// whole list, and only the first max entries are kept (a value edited in
// the database by hand never makes the verification ask thousands of
// resolvers). The bound counts the entries left after de-duplication. The
// result is never nil, so JSON shows it as [].
func (ls listSetting) collect(inputs []string, strict bool) ([]string, error) {
	seen := map[string]bool{}
	out := []string{}
	for _, e := range splitListEntries(inputs) {
		norm, ok := ls.normalize(e.value)
		if !ok {
			if strict {
				return nil, ls.invalid(e.line, e.value)
			}
			log.Printf("ignoring invalid saved %s entry %q", ls.key, e.value)
			continue
		}
		if !seen[norm] {
			seen[norm] = true
			out = append(out, norm)
		}
	}
	if len(out) > ls.max {
		if strict {
			return nil, ls.tooMany()
		}
		log.Printf("ignoring %d saved %s entries over the maximum of %d", len(out)-ls.max, ls.key, ls.max)
		out = out[:ls.max]
	}
	return out, nil
}

// parse validates and normalizes list entries given by a person.
func (ls listSetting) parse(entries []string) ([]string, error) {
	return ls.collect(entries, true)
}

// resolveSaved returns the saved list (empty when not set).
func (ls listSetting) resolveSaved(db *sql.DB) []string {
	list, _ := ls.collect([]string{models.GetSetting(db, ls.key)}, false)
	return list
}

// save validates the entries and persists them one per line (an empty list
// removes the setting).
func (ls listSetting) save(db *sql.DB, entries []string) error {
	var u SettingsUpdate
	if err := u.list(ls, entries); err != nil {
		return err
	}
	return u.Save(db)
}

// usableIP reports whether a can stand for a server: no zone, not loopback,
// not unspecified and not IPv4-mapped. An IPv4-mapped IPv6 address
// (::ffff:203.0.113.5, ::ffff:0:0/96) is refused in every list rather than
// unmapped: it is an IPv4 address written as IPv6, which belongs in the
// IPv4 list, so a list never holds one address in two forms (whoever
// compares an address with the lists unmaps it first).
func usableIP(a netip.Addr) bool {
	return a.Zone() == "" && !a.IsLoopback() && !a.IsUnspecified() && !a.Is4In6()
}

// normalizeIPOrPrefix normalizes an IPv4 (want6 false) or IPv6 address or
// CIDR to the canonical text of net/netip. A CIDR is masked to its network
// address (203.0.113.7/24 -> 203.0.113.0/24) and a CIDR of one address is
// written as the address (203.0.113.7/32 -> 203.0.113.7), so equal entries
// compare equal. The network address must be usable too, which refuses
// 0.0.0.0/0, ::/0 and the loopback networks.
func normalizeIPOrPrefix(s string, want6 bool) (string, bool) {
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return "", false
		}
		p = p.Masked()
		if !usableIP(p.Addr()) || p.Addr().Is6() != want6 {
			return "", false
		}
		if p.IsSingleIP() {
			return p.Addr().String(), true
		}
		return p.String(), true
	}
	a, err := netip.ParseAddr(s)
	if err != nil || !usableIP(a) || a.Is6() != want6 {
		return "", false
	}
	return a.String(), true
}

// normalizeOwnHost checks the host name of one of our own sending servers
// (an entry of own_sending_hosts, or the SMTP host of a mailbox,
// mailboxes.go) and returns it lower-case without one trailing dot. A host name is one or more labels of 1-63 ASCII
// letters, digits and hyphens (not at either end of a label), 253
// characters at most. A single label (mailhost) is accepted: internal MTAs
// often report a name without a domain. A last label of digits only is
// refused so that an IP address is not taken for a host name, and so are
// the loopback names (localhost, *.localhost, localhost.localdomain), which
// name every machine rather than ours.
func normalizeOwnHost(s string) (string, bool) {
	h := strings.TrimSuffix(s, ".")
	if h == "" || len(h) > 253 {
		return "", false
	}
	labels := strings.Split(h, ".")
	for _, label := range labels {
		if !validHostLabel(label) {
			return "", false
		}
	}
	if strings.Trim(labels[len(labels)-1], "0123456789") == "" {
		return "", false
	}
	h = strings.ToLower(h)
	if h == "localhost" || strings.HasSuffix(h, ".localhost") || h == "localhost.localdomain" {
		return "", false
	}
	return h, true
}

// validHostLabel reports whether label is 1-63 ASCII letters, digits and
// hyphens, not starting or ending with a hyphen.
func validHostLabel(label string) bool {
	if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
		return false
	}
	for i := 0; i < len(label); i++ {
		c := label[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// normalizeResolver normalizes the IP address of a resolver: IPv4 or IPv6,
// without port, CIDR or zone, and usable (usableIP).
func normalizeResolver(s string) (string, bool) {
	a, err := netip.ParseAddr(s)
	if err != nil || !usableIP(a) {
		return "", false
	}
	return a.String(), true
}

// ParseOwnSendingIPv4 validates and normalizes own sending IPv4 addresses
// and CIDRs. Every entry may hold several lines; blank lines are skipped,
// duplicates dropped, and at most MaxOwnSendingEntries remain. An empty
// input yields an empty list.
func ParseOwnSendingIPv4(entries []string) ([]string, error) {
	return ownSendingIPv4Setting.parse(entries)
}

// ParseOwnSendingIPv6 is ParseOwnSendingIPv4 for IPv6 addresses and CIDRs.
func ParseOwnSendingIPv6(entries []string) ([]string, error) {
	return ownSendingIPv6Setting.parse(entries)
}

// ParseOwnSendingHosts is ParseOwnSendingIPv4 for host names.
func ParseOwnSendingHosts(entries []string) ([]string, error) {
	return ownSendingHostsSetting.parse(entries)
}

// ResolveOwnSendingIPv4 returns the saved own sending IPv4 addresses and
// CIDRs (empty when not set); saved entries that are no longer valid are
// skipped with a log line.
func ResolveOwnSendingIPv4(db *sql.DB) []string {
	return ownSendingIPv4Setting.resolveSaved(db)
}

// ResolveOwnSendingIPv6 is ResolveOwnSendingIPv4 for IPv6.
func ResolveOwnSendingIPv6(db *sql.DB) []string {
	return ownSendingIPv6Setting.resolveSaved(db)
}

// ResolveOwnSendingHosts is ResolveOwnSendingIPv4 for host names.
func ResolveOwnSendingHosts(db *sql.DB) []string {
	return ownSendingHostsSetting.resolveSaved(db)
}

// ResolveOwnSending returns our own sending servers: the three saved lists
// and the SMTP hosts of every registered mailbox (ownSendingMailboxHosts).
// Every user of the own sending servers takes them from here: the grouping
// (groupOptions, per mailbox: ownSourcesFor), the settings API and
// "settings show". A
// failure to list the mailboxes is returned rather than leaving their host
// names out, which would take a notice our own server issued for one about
// mail we never sent.
func ResolveOwnSending(db *sql.DB) (OwnSending, error) {
	mailboxes, err := models.ListMailboxes(db)
	if err != nil {
		return OwnSending{}, fmt.Errorf("failed to list the mailboxes: %w", err)
	}
	return ownSendingOf(db, mailboxes), nil
}

// ownSendingOf is ResolveOwnSending with the mailboxes already listed.
func ownSendingOf(db *sql.DB, mailboxes []*models.Mailbox) OwnSending {
	o := OwnSending{IPv4: ResolveOwnSendingIPv4(db), IPv6: ResolveOwnSendingIPv6(db),
		Hosts: ResolveOwnSendingHosts(db), MailboxHosts: ownSendingMailboxHosts(mailboxes), MailboxesWithoutHost: []string{}}
	if len(o.Hosts) == 0 {
		o.MailboxesWithoutHost = mailboxesWithoutHost(mailboxes)
	}
	return o
}

// mailboxesWithoutHost returns the addresses of the mailboxes, enabled or
// not and in the order of the list, whose SMTP host is empty or not a valid
// host name (which counts as none, as for the grouping: ownSourcesFor).
// Never nil.
func mailboxesWithoutHost(mailboxes []*models.Mailbox) []string {
	out := []string{}
	for _, mb := range mailboxes {
		if _, ok := normalizeOwnHost(strings.TrimSpace(mb.SmtpHost)); !ok {
			out = append(out, mb.Address)
		}
	}
	return out
}

// ownSendingMailboxHosts returns the SMTP hosts of the mailboxes, enabled or
// not, in the order of the list, without the empty and the repeated ones. A
// stored host that is not a valid host name (edited in the database by
// hand) is skipped with a log line, like an invalid saved list entry. The
// result is never nil, so JSON shows it as [].
func ownSendingMailboxHosts(mailboxes []*models.Mailbox) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, mb := range mailboxes {
		raw := strings.TrimSpace(mb.SmtpHost)
		if raw == "" {
			continue
		}
		host, ok := normalizeOwnHost(raw)
		if !ok {
			log.Printf("ignoring invalid smtp_host %q of mailbox %s", mb.SmtpHost, mb.Address)
			continue
		}
		if !seen[host] {
			seen[host] = true
			out = append(out, host)
		}
	}
	return out
}

// SaveOwnSendingIPv4 validates (ParseOwnSendingIPv4) and persists the own
// sending IPv4 list, one entry per line; an empty list removes the setting.
func SaveOwnSendingIPv4(db *sql.DB, entries []string) error {
	return ownSendingIPv4Setting.save(db, entries)
}

// SaveOwnSendingIPv6 is SaveOwnSendingIPv4 for IPv6.
func SaveOwnSendingIPv6(db *sql.DB, entries []string) error {
	return ownSendingIPv6Setting.save(db, entries)
}

// SaveOwnSendingHosts is SaveOwnSendingIPv4 for host names.
func SaveOwnSendingHosts(db *sql.DB, entries []string) error {
	return ownSendingHostsSetting.save(db, entries)
}

// ParseDNSCheckResolvers validates and normalizes resolver IP addresses
// (IPv4 or IPv6, see normalizeResolver). Every entry may hold several
// lines; blank lines are skipped, duplicates dropped (the order is kept),
// and at most MaxDNSCheckResolvers remain. An empty input yields an empty
// list.
func ParseDNSCheckResolvers(entries []string) ([]string, error) {
	return dnsCheckResolversSetting.parse(entries)
}

// resolveBool returns a "1" / "0" setting (fallback when not set or of
// another shape).
func resolveBool(db *sql.DB, key string, fallback bool) bool {
	switch models.GetSetting(db, key) {
	case "1":
		return true
	case "0":
		return false
	}
	return fallback
}

// ResolveDNSCheckEnabled reports whether DNS lookup failures are verified
// (default true).
func ResolveDNSCheckEnabled(db *sql.DB) bool {
	return resolveBool(db, SettingDNSCheckEnabled, DefaultDNSCheckEnabled)
}

// ResolveDNSCheckSystemResolverOnly reports whether only the OS resolver is
// queried (default false).
func ResolveDNSCheckSystemResolverOnly(db *sql.DB) bool {
	return resolveBool(db, SettingDNSCheckSystemResolverOnly, DefaultDNSCheckSystemResolverOnly)
}

// ResolveDNSCheckResolvers returns the resolvers to query (saved >
// default): no row means DefaultDNSCheckResolvers, a row holding "" an
// explicitly empty list. Saved entries that are no longer valid are skipped
// with a log line.
func ResolveDNSCheckResolvers(db *sql.DB) []string {
	saved, found, err := models.GetSettingStrict(db, SettingDNSCheckResolvers)
	if err != nil {
		log.Printf("failed to read %s: %v", SettingDNSCheckResolvers, err)
		return DefaultDNSCheckResolvers()
	}
	if !found {
		return DefaultDNSCheckResolvers()
	}
	list, _ := dnsCheckResolversSetting.collect([]string{saved}, false)
	return list
}

// ResolveDNSCheck returns the configuration of the DNS lookup failure
// verification.
func ResolveDNSCheck(db *sql.DB) DNSCheckConfig {
	return DNSCheckConfig{
		Enabled:    ResolveDNSCheckEnabled(db),
		Resolvers:  ResolveDNSCheckResolvers(db),
		SystemOnly: ResolveDNSCheckSystemResolverOnly(db),
	}
}

// ValidateDNSCheck checks the combination of the three settings: while the
// verification is on and not limited to the OS resolver, at least one
// resolver must be listed.
func ValidateDNSCheck(enabled, systemOnly bool, resolvers []string) error {
	if enabled && !systemOnly && len(resolvers) == 0 {
		return errDNSCheckNoResolver
	}
	return nil
}

// CheckDNSCheckSettings returns the configuration that changing any of the
// three settings (nil = unchanged, the saved value counts) would give, the
// resolvers normalized (ParseDNSCheckResolvers), or the error that refuses
// the change (ValidateDNSCheck on the resulting combination). Nothing is
// saved.
func CheckDNSCheckSettings(db *sql.DB, enabled *bool, resolvers *[]string, systemOnly *bool) (DNSCheckConfig, error) {
	cfg := ResolveDNSCheck(db)
	if enabled != nil {
		cfg.Enabled = *enabled
	}
	if systemOnly != nil {
		cfg.SystemOnly = *systemOnly
	}
	if resolvers != nil {
		list, err := ParseDNSCheckResolvers(*resolvers)
		if err != nil {
			return DNSCheckConfig{}, err
		}
		cfg.Resolvers = list
	}
	if err := ValidateDNSCheck(cfg.Enabled, cfg.SystemOnly, cfg.Resolvers); err != nil {
		return DNSCheckConfig{}, err
	}
	return cfg, nil
}

// SaveDNSCheck changes any of the three settings at once (nil =
// unchanged): the resulting combination is checked first
// (CheckDNSCheckSettings) and nothing is saved when it is refused. A value
// equal to its default removes the setting; for the resolvers that is the
// default list in its order, and any other list, an empty one included, is
// stored (one address per line). The settings are written in one
// transaction (SettingsUpdate), so a failure never leaves a combination the
// check would refuse.
func SaveDNSCheck(db *sql.DB, enabled *bool, resolvers *[]string, systemOnly *bool) error {
	var u SettingsUpdate
	if err := u.DNSCheck(db, enabled, resolvers, systemOnly); err != nil {
		return err
	}
	return u.Save(db)
}
