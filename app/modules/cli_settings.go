package modules

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"mailcare/app/models"
	"mailcare/app/modules/agent"
)

// --- schedule commands ---

// ScheduleCmd shows or sets the daily check times.
type ScheduleCmd struct {
	Show ScheduleShowCmd `cmd:"" help:"Show the check times"`
	Set  ScheduleSetCmd  `cmd:"" help:"Set the check times (HH:MM ...; none = disable)"`
}

// ScheduleShowCmd prints the check times and the next run.
type ScheduleShowCmd struct {
	JSON bool `help:"Output as JSON"`
}

func (c *ScheduleShowCmd) Run() error {
	db, err := openDBForCLI()
	if err != nil {
		return err
	}
	defer db.Close()
	times := ResolveCheckTimes(db)
	next, ok := NextCheckAt(time.Now(), times)
	if c.JSON {
		var nextOut interface{}
		if ok {
			nextOut = next.UTC().Format("2006-01-02T15:04:05Z07:00")
		}
		printJSON(map[string]interface{}{"check_times": times, "next_check_at": nextOut})
		return nil
	}
	if len(times) == 0 {
		fmt.Println("check times: none (automatic checks are disabled)")
		return nil
	}
	fmt.Printf("check times: %s\n", strings.Join(times, " "))
	fmt.Printf("next check:  %s (when the server is running)\n", next.Format("2006-01-02 15:04"))
	return nil
}

// ScheduleSetCmd replaces the check times.
type ScheduleSetCmd struct {
	Times []string `arg:"" optional:"" help:"Check times (HH:MM); omit to disable automatic checks"`
}

func (c *ScheduleSetCmd) Run() error {
	times, err := ParseCheckTimes(c.Times)
	if err != nil {
		return NewExitError(ExitArgument, err.Error())
	}
	db, err := openDBForCLI()
	if err != nil {
		return err
	}
	defer db.Close()
	if err := SaveCheckTimes(db, times); err != nil {
		return NewExitError(ExitConfig, err.Error())
	}
	if len(times) == 0 {
		fmt.Println("Automatic checks disabled")
		return nil
	}
	fmt.Printf("Check times set to %s (a running server picks this up within %s)\n", strings.Join(times, " "), SchedulerTick)
	return nil
}

// --- settings commands ---

// SettingsCmd shows or changes server settings.
type SettingsCmd struct {
	Show SettingsShowCmd `cmd:"" help:"Show the settings"`
	Set  SettingsSetCmd  `cmd:"" help:"Set a setting (web_listen, web_port, workers, check_times, agent_provider, agent_enabled, agent_keep_days, mail_keep_days, cookie_ttl_hours, smtp_host, smtp_port, smtp_security, smtp_username, smtp_password, smtp_from, public_base_url, notify_enabled, notify_time, notify_interval_days, notify_user_ids)"`
}

// SettingsShowCmd prints every effective setting.
type SettingsShowCmd struct {
	JSON bool `help:"Output as JSON"`
}

func (c *SettingsShowCmd) Run() error {
	db, err := openDBForCLI()
	if err != nil {
		return err
	}
	defer db.Close()
	dataDir, _ := DataDir()
	provider := ResolveAgentProvider(db)
	notify, err := ResolveNotificationSettings(db, nil)
	if err != nil {
		return NewExitError(ExitConfig, err.Error())
	}
	values := map[string]interface{}{
		SettingWebListen:      ResolveWebListen(db, ""),
		SettingWebPort:        ResolveWebPort(db, 0),
		SettingWorkers:        ResolveWorkers(db),
		SettingCheckTimes:     ResolveCheckTimes(db),
		SettingAgentProvider:  provider,
		SettingAgentEnabled:   ResolveAgentEnabled(db),
		SettingAgentKeepDays:  ResolveAgentKeepDays(db),
		SettingMailKeepDays:   ResolveMailKeepDays(db),
		SettingCookieTTLHours: ResolveCookieTTLHours(db),
		SettingSMTPHost:       notify.SMTP.Host,
		SettingSMTPPort:       notify.SMTP.Port,
		SettingSMTPSecurity:   notify.SMTP.Security,
		SettingSMTPUsername:   notify.SMTP.Username,
		"smtp_password_set":   notify.SMTPPasswordSet,
		SettingSMTPFrom:       notify.SMTP.From,
		SettingPublicBaseURL:  notify.PublicBaseURL,
		"effective_base_url":  EffectiveBaseURL(db, notify),
		SettingNotifyEnabled:  notify.Enabled,
		SettingNotifyTime:     notify.Time,
		SettingNotifyInterval: notify.IntervalDays,
		SettingNotifyUserIDs:  notify.UserIDs,
		SettingNotifyLastSent: rfc3339OrNull(notify.LastSentAt),
		// The date of the last daily cleanup is recorded by the scheduler
		// and shown for information only.
		SettingCleanupLastRunDate: stringOrNull(models.GetSetting(db, SettingCleanupLastRunDate)),
	}
	if c.JSON {
		values["data_dir"] = dataDir
		values["providers"] = agent.Providers()
		if next, ok := NextNotifyAt(time.Now(), notify); ok {
			values["next_send_at"] = next.UTC().Format(time.RFC3339)
		} else {
			values["next_send_at"] = nil
		}
		printJSON(values)
		return nil
	}
	fmt.Printf("%-18s %s\n", "data_dir:", dataDir)
	fmt.Printf("%-18s %v\n", SettingWebListen+":", values[SettingWebListen])
	fmt.Printf("%-18s %v\n", SettingWebPort+":", values[SettingWebPort])
	fmt.Printf("%-18s %v\n", SettingWorkers+":", values[SettingWorkers])
	fmt.Printf("%-18s %s\n", SettingCheckTimes+":", DisplayCheckTimes(ResolveCheckTimes(db)))
	available := "not available"
	if agent.ProviderAvailable(provider) {
		available = "available"
	}
	fmt.Printf("%-18s %s (%s)\n", SettingAgentProvider+":", provider, available)
	fmt.Printf("%-18s %v\n", SettingAgentEnabled+":", values[SettingAgentEnabled])
	fmt.Printf("%-18s %v\n", SettingAgentKeepDays+":", values[SettingAgentKeepDays])
	fmt.Printf("%-18s %v\n", SettingMailKeepDays+":", values[SettingMailKeepDays])
	fmt.Printf("%-18s %v\n", SettingCookieTTLHours+":", values[SettingCookieTTLHours])
	fmt.Println()
	fmt.Printf("%-22s %v\n", SettingSMTPHost+":", notify.SMTP.Host)
	fmt.Printf("%-22s %v\n", SettingSMTPPort+":", notify.SMTP.Port)
	fmt.Printf("%-22s %v\n", SettingSMTPSecurity+":", notify.SMTP.Security)
	fmt.Printf("%-22s %v\n", SettingSMTPUsername+":", notify.SMTP.Username)
	password := "(not set)"
	if notify.SMTPPasswordSet {
		password = "(set)"
	}
	fmt.Printf("%-22s %s\n", "smtp_password:", password)
	fmt.Printf("%-22s %v\n", SettingSMTPFrom+":", notify.SMTP.From)
	fmt.Printf("%-22s %v (effective: %s)\n", SettingPublicBaseURL+":", notify.PublicBaseURL, EffectiveBaseURL(db, notify))
	fmt.Printf("%-22s %v\n", SettingNotifyEnabled+":", notify.Enabled)
	fmt.Printf("%-22s %v\n", SettingNotifyTime+":", notify.Time)
	fmt.Printf("%-22s %v\n", SettingNotifyInterval+":", notify.IntervalDays)
	fmt.Printf("%-22s %v\n", SettingNotifyUserIDs+":", FormatNotifyUserIDs(notify.UserIDs))
	fmt.Printf("%-22s %s\n", "notify_last_sent_at:", formatNullTime(notify.LastSentAt))
	if next, ok := NextNotifyAt(time.Now(), notify); ok {
		fmt.Printf("%-22s %s (when the server is running)\n", "next_send_at:", next.Format("2006-01-02 15:04"))
	} else {
		fmt.Printf("%-22s none (notifications are disabled)\n", "next_send_at:")
	}
	fmt.Println()
	lastCleanup := models.GetSetting(db, SettingCleanupLastRunDate)
	if lastCleanup == "" {
		lastCleanup = "-"
	}
	fmt.Printf("%-22s %s (the daily cleanup runs when the date changes)\n", SettingCleanupLastRunDate+":", lastCleanup)
	return nil
}

// stringOrNull maps an empty string to JSON null.
func stringOrNull(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// SettingsSetCmd changes one setting. The value may be omitted only for
// smtp_password, which is then read from stdin (hidden on a terminal, a
// plain line on piped input, entered twice) so that it never appears in the
// shell history or the process list.
type SettingsSetCmd struct {
	Key   string  `arg:"" help:"Setting key"`
	Value *string `arg:"" optional:"" help:"New value (an empty string restores the default; check_times also accepts none to disable the automatic checks). Omit it for smtp_password to be prompted for the password (hidden on a terminal; a line on piped input)"`
}

func (c *SettingsSetCmd) Run() error {
	key := strings.ToLower(strings.TrimSpace(c.Key))
	var value string
	switch {
	case c.Value != nil:
		value = strings.TrimSpace(*c.Value)
	case key == settingSMTPPassword:
		password, err := promptPassword("SMTP password")
		if err != nil {
			return err
		}
		if password == "" {
			return NewExitErrorf(ExitArgument, "no password given (use %s set %s \"\" to remove the stored password)", "settings", key)
		}
		value = password
	default:
		return NewExitErrorf(ExitArgument, "a value is required for %s (only %s may be omitted to be prompted)", key, settingSMTPPassword)
	}
	db, err := openDBForCLI()
	if err != nil {
		return err
	}
	defer db.Close()
	shown, err := ApplySetting(db, key, value, func() ([]byte, error) { return loadKeyForCLI(db) })
	if err != nil {
		var exitErr *ExitError
		if errors.As(err, &exitErr) {
			return err
		}
		return NewExitError(ExitConfig, err.Error())
	}
	if value == "" {
		fmt.Printf("%s restored to its default\n", key)
		return nil
	}
	if key == SettingWorkers {
		fmt.Printf("(a running server applies the new worker count at its next start; use the Web settings to change it live)\n")
	}
	fmt.Printf("%s = %s\n", key, shown)
	return nil
}

// SettingKeys lists the keys "settings set" accepts.
func SettingKeys() []string {
	return []string{
		SettingWebListen, SettingWebPort, SettingWorkers, SettingCheckTimes, SettingAgentProvider, SettingAgentEnabled,
		SettingAgentKeepDays, SettingMailKeepDays, SettingCookieTTLHours, SettingSMTPHost, SettingSMTPPort, SettingSMTPSecurity,
		SettingSMTPUsername,
		settingSMTPPassword,
		SettingSMTPFrom, SettingPublicBaseURL, SettingNotifyEnabled, SettingNotifyTime, SettingNotifyInterval, SettingNotifyUserIDs,
	}
}

// settingSMTPPassword is the "settings set" key of the SMTP password; the
// value is encrypted and stored under SettingSMTPPasswordEnc.
const settingSMTPPassword = "smtp_password"

// ApplySetting validates and stores one setting given as text and returns
// the value as stored (for display). An empty value restores the default.
// loadKey supplies the master key when the SMTP password is set (nil when a
// caller never sets it). Validation failures are ExitErrors with
// ExitArgument.
func ApplySetting(db *sql.DB, key, value string, loadKey func() ([]byte, error)) (string, error) {
	key = strings.ToLower(strings.TrimSpace(key))
	value = strings.TrimSpace(value)
	if !slices.Contains(SettingKeys(), key) {
		return "", NewExitErrorf(ExitArgument, "unknown setting %q (known: %s)", key, strings.Join(SettingKeys(), ", "))
	}
	if value == "" {
		stored := key
		if key == settingSMTPPassword {
			stored = SettingSMTPPasswordEnc
		}
		return "", models.DeleteSetting(db, stored)
	}
	argErr := func(err error) (string, error) { return "", NewExitError(ExitArgument, err.Error()) }
	switch key {
	case SettingWebListen:
		return value, PersistSetting(db, key, value, value == DefaultWebListenAddr)
	case SettingWebPort, SettingCookieTTLHours:
		n, perr := strconv.Atoi(value)
		if perr != nil || n < 1 || (key == SettingWebPort && n > 65535) {
			return "", NewExitErrorf(ExitArgument, "%s must be a positive integer", key)
		}
		def := DefaultWebPort
		if key == SettingCookieTTLHours {
			def = DefaultCookieTTLHours
		}
		return strconv.Itoa(n), PersistSetting(db, key, strconv.Itoa(n), n == def)
	case SettingWorkers:
		n, perr := ParseWorkers(value)
		if perr != nil {
			return argErr(perr)
		}
		return strconv.Itoa(n), SaveWorkers(db, n)
	case SettingCheckTimes:
		// "none" disables the automatic checks ("" restores the default).
		if strings.EqualFold(value, CheckTimesNone) {
			return CheckTimesNone, SaveCheckTimes(db, nil)
		}
		times, perr := ParseCheckTimes([]string{value})
		if perr != nil {
			return argErr(perr)
		}
		return FormatCheckTimes(times), SaveCheckTimes(db, times)
	case SettingAgentProvider:
		if !agent.IsValidProvider(value) {
			names := make([]string, 0)
			for _, p := range agent.Providers() {
				names = append(names, p.Name)
			}
			return "", NewExitErrorf(ExitArgument, "unknown agent provider %q (registered: %s)", value, strings.Join(names, ", "))
		}
		return value, SetAgentProvider(db, value)
	case SettingAgentEnabled:
		enabled, perr := ParseBoolSetting(value)
		if perr != nil {
			return argErr(perr)
		}
		return strconv.FormatBool(enabled), SetAgentEnabled(db, enabled)
	case SettingAgentKeepDays:
		n, perr := ParseAgentKeepDays(value)
		if perr != nil {
			return argErr(perr)
		}
		return strconv.Itoa(n), SaveAgentKeepDays(db, n)
	case SettingMailKeepDays:
		n, perr := ParseMailKeepDays(value)
		if perr != nil {
			return argErr(perr)
		}
		return strconv.Itoa(n), SaveMailKeepDays(db, n)
	}
	// Notification keys share the validation of the Web settings.
	in := &NotificationInput{}
	shown := value
	var masterKey []byte
	switch key {
	case SettingSMTPHost:
		in.SMTPHost = &value
	case SettingSMTPPort:
		n, perr := strconv.Atoi(value)
		if perr != nil {
			return "", NewExitErrorf(ExitArgument, "%s must be an integer between 1 and 65535", key)
		}
		in.SMTPPort = &n
	case SettingSMTPSecurity:
		in.SMTPSecurity = &value
		shown = strings.ToLower(value)
	case SettingSMTPUsername:
		in.SMTPUsername = &value
	case settingSMTPPassword:
		if loadKey == nil {
			return "", errors.New("the master key is required to store the SMTP password")
		}
		k, err := loadKey()
		if err != nil {
			return "", err
		}
		masterKey = k
		in.SMTPPassword = &value
		shown = "(encrypted)"
	case SettingSMTPFrom:
		in.SMTPFrom = &value
		shown = strings.ToLower(value)
	case SettingPublicBaseURL:
		in.PublicBaseURL = &value
		shown = strings.TrimRight(value, "/")
	case SettingNotifyEnabled:
		enabled, perr := ParseBoolSetting(value)
		if perr != nil {
			return argErr(perr)
		}
		in.Enabled = &enabled
		shown = strconv.FormatBool(enabled)
	case SettingNotifyTime:
		norm, perr := ValidateNotifyTime(value)
		if perr != nil {
			return argErr(perr)
		}
		in.Time = &norm
		shown = norm
	case SettingNotifyInterval:
		n, perr := strconv.Atoi(value)
		if perr != nil {
			return "", NewExitErrorf(ExitArgument, "%s must be an integer between %d and %d", key, MinNotifyIntervalDays, MaxNotifyIntervalDays)
		}
		in.IntervalDays = &n
	case SettingNotifyUserIDs:
		var ids []int64
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			id, perr := strconv.ParseInt(part, 10, 64)
			if perr != nil || id <= 0 {
				return "", NewExitErrorf(ExitArgument, "%s must be a comma-separated list of user ids", key)
			}
			ids = append(ids, id)
		}
		in.UserIDs = &ids
	}
	if err := SaveNotificationSettings(db, masterKey, in); err != nil {
		if errors.Is(err, ErrSMTPPasswordRequired) {
			// One key per command: the password cannot come along, so the
			// operator removes it first and sets it again afterwards.
			return "", NewExitErrorf(ExitArgument, "%v (remove the stored password with: settings set %s \"\", change the setting, then set the password again)", err, settingSMTPPassword)
		}
		return argErr(err)
	}
	if key == SettingNotifyUserIDs {
		ids, _ := ValidateNotifyUserIDs(db, *in.UserIDs)
		shown = FormatNotifyUserIDs(ids)
	}
	return shown, nil
}
