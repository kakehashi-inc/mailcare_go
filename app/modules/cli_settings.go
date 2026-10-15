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
	Set  SettingsSetCmd  `cmd:"" help:"Set a setting (web_listen, web_port, workers, check_times, agent_provider, agent_enabled, agent_keep_days, mail_keep_days, cleanup_time, own_sending_ipv4, own_sending_ipv6, own_sending_hosts, dns_check_enabled, dns_check_resolvers, dns_check_system_resolver_only, cookie_ttl_hours, smtp_host, smtp_port, smtp_security, smtp_username, smtp_password, smtp_from, public_base_url, notify_enabled, notify_time, notify_interval_days, notify_user_ids)"`
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
	own, err := ResolveOwnSending(db)
	if err != nil {
		return NewExitError(ExitGeneral, err.Error())
	}
	dnsCheck := ResolveDNSCheck(db)
	values := map[string]interface{}{
		SettingWebListen:            ResolveWebListen(db, ""),
		SettingWebPort:              ResolveWebPort(db, 0),
		SettingWorkers:              ResolveWorkers(db),
		SettingCheckTimes:           ResolveCheckTimes(db),
		SettingAgentProvider:        provider,
		SettingAgentModel:           ResolveAgentModel(db),
		SettingAgentReasoningEffort: ResolveAgentReasoningEffort(db),
		SettingAgentEnabled:         ResolveAgentEnabled(db),
		SettingAgentKeepDays:        ResolveAgentKeepDays(db),
		SettingMailKeepDays:         ResolveMailKeepDays(db),
		SettingCleanupTime:          ResolveCleanupTime(db),
		SettingRecheckDays:          ResolveRecheckDays(db),
		SettingCookieTTLHours:       ResolveCookieTTLHours(db),
		SettingSMTPHost:             notify.SMTP.Host,
		SettingSMTPPort:             notify.SMTP.Port,
		SettingSMTPSecurity:         notify.SMTP.Security,
		SettingSMTPUsername:         notify.SMTP.Username,
		"smtp_password_set":         notify.SMTPPasswordSet,
		SettingSMTPFrom:             notify.SMTP.From,
		SettingPublicBaseURL:        notify.PublicBaseURL,
		"effective_base_url":        EffectiveBaseURL(db, notify),
		SettingNotifyEnabled:        notify.Enabled,
		SettingNotifyTime:           notify.Time,
		SettingNotifyInterval:       notify.IntervalDays,
		SettingNotifyUserIDs:        notify.UserIDs,
		SettingNotifyLastSent:       rfc3339OrNull(notify.LastSentAt),
		// The date of the last daily cleanup is recorded by the scheduler
		// and shown for information only.
		SettingCleanupLastRunDate: stringOrNull(models.GetSetting(db, SettingCleanupLastRunDate)),
		// Own sending servers and the DNS lookup failure verification; the
		// resolvers are kept but not used while
		// dns_check_system_resolver_only is on.
		SettingOwnSendingIPv4:  own.IPv4,
		SettingOwnSendingIPv6:  own.IPv6,
		SettingOwnSendingHosts: own.Hosts,
		// The SMTP hosts of the mailboxes (mailbox update --smtp-host), own
		// sending host names next to own_sending_hosts; shown, not set here.
		"own_sending_mailbox_hosts": own.MailboxHosts,
		// The addresses without a host name of their own while
		// own_sending_hosts is empty: their notices about mail we did not
		// send are not judged (shown, derived).
		"own_sending_mailboxes_without_host": own.MailboxesWithoutHost,
		SettingDNSCheckEnabled:               dnsCheck.Enabled,
		SettingDNSCheckResolvers:             dnsCheck.Resolvers,
		SettingDNSCheckSystemResolverOnly:    dnsCheck.SystemOnly,
	}
	if probe, err := ResolveProbeSettings(db, nil); err == nil {
		values[SettingProbeEnabled] = probe.Enabled
		values[SettingProbeAddress] = probe.Address
		values[SettingProbeIMAPHost] = probe.Conn.ImapHost
		values[SettingProbeIMAPPort] = probe.Conn.ImapPort
		values[SettingProbeIMAPSecurity] = probe.Conn.ImapSecurity
		values[SettingProbeIMAPUsername] = probe.Conn.ImapUsername
		values["probe_imap_password_set"] = probe.PasswordSet
		values[SettingProbeIMAPFolder] = probe.Conn.Folder
		values[SettingProbeSendTimes] = probe.SendTimes
		values[SettingProbeDelayMinutes] = probe.DelayMinutes
		values[SettingProbeFailureMinutes] = probe.FailureMinutes
		values[SettingProbeLastSendAt] = rfc3339OrNull(probe.LastSendAt)
		values[SettingProbeNextCheckAt] = rfc3339OrNull(probe.NextCheckAt)
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
	model := ResolveAgentModel(db)
	if model == "" {
		model = "(CLI default)"
	}
	fmt.Printf("%-18s %s\n", SettingAgentModel+":", model)
	effort := ResolveAgentReasoningEffort(db)
	if effort == "" {
		effort = "(CLI setting)"
	}
	fmt.Printf("%-18s %s\n", SettingAgentReasoningEffort+":", effort)
	fmt.Printf("%-18s %v\n", SettingAgentEnabled+":", values[SettingAgentEnabled])
	fmt.Printf("%-18s %v\n", SettingAgentKeepDays+":", values[SettingAgentKeepDays])
	fmt.Printf("%-18s %v\n", SettingMailKeepDays+":", values[SettingMailKeepDays])
	fmt.Printf("%-18s %v\n", SettingCookieTTLHours+":", values[SettingCookieTTLHours])
	recheck := ResolveRecheckDays(db)
	parts := make([]string, 0, len(recheck))
	for _, category := range KnownCategories() {
		parts = append(parts, fmt.Sprintf("%s=%d", category, recheck[category]))
	}
	fmt.Printf("%-18s %s\n", SettingRecheckDays+":", strings.Join(parts, ", "))
	fmt.Println()
	fmt.Printf("%-31s %s\n", SettingOwnSendingIPv4+":", displayList(own.IPv4, "(not set)"))
	fmt.Printf("%-31s %s\n", SettingOwnSendingIPv6+":", displayList(own.IPv6, "(not set)"))
	fmt.Printf("%-31s %s\n", SettingOwnSendingHosts+":", displayList(own.Hosts, "(not set)"))
	fmt.Printf("%-31s %s\n", "own_sending_mailbox_hosts:", displayList(own.MailboxHosts, "(none: set with mailbox update --smtp-host)"))
	if len(own.MailboxesWithoutHost) > 0 {
		fmt.Printf("%-31s %s (backscatter is not judged for them: set mailbox update --smtp-host or own_sending_hosts)\n",
			"mailboxes without a host name:", strings.Join(own.MailboxesWithoutHost, ", "))
	}
	fmt.Printf("%-31s %v\n", SettingDNSCheckEnabled+":", dnsCheck.Enabled)
	resolvers := displayList(dnsCheck.Resolvers, DNSCheckResolversNone)
	if dnsCheck.SystemOnly {
		resolvers += " (not used: only the OS resolver is queried)"
	}
	fmt.Printf("%-31s %s\n", SettingDNSCheckResolvers+":", resolvers)
	fmt.Printf("%-31s %v\n", SettingDNSCheckSystemResolverOnly+":", dnsCheck.SystemOnly)
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
	printProbeSettings(db)
	fmt.Println()
	lastCleanup := models.GetSetting(db, SettingCleanupLastRunDate)
	if lastCleanup == "" {
		lastCleanup = "-"
	}
	fmt.Printf("%-22s %v\n", SettingCleanupTime+":", values[SettingCleanupTime])
	fmt.Printf("%-22s %s\n", SettingCleanupLastRunDate+":", lastCleanup)
	return nil
}

// displayList formats a list setting for people: comma-separated, or empty
// when the list is.
func displayList(list []string, empty string) string {
	if len(list) == 0 {
		return empty
	}
	return strings.Join(list, ", ")
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
	Value *string `arg:"" optional:"" help:"New value (an empty string restores the default; check_times also accepts none to disable the automatic checks; own_sending_ipv4, own_sending_ipv6, own_sending_hosts and dns_check_resolvers take a comma-separated list, and dns_check_resolvers accepts none to save an empty list). Omit it for smtp_password to be prompted for the password (hidden on a terminal; a line on piped input)"`
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
		SettingWebListen, SettingWebPort, SettingWorkers, SettingCheckTimes, SettingAgentProvider, SettingAgentModel,
		SettingAgentReasoningEffort, SettingAgentEnabled,
		SettingAgentKeepDays, SettingMailKeepDays, SettingCleanupTime, SettingRecheckDays, SettingCookieTTLHours,
		SettingOwnSendingIPv4, SettingOwnSendingIPv6, SettingOwnSendingHosts,
		SettingDNSCheckEnabled, SettingDNSCheckResolvers, SettingDNSCheckSystemResolverOnly,
		SettingSMTPHost, SettingSMTPPort, SettingSMTPSecurity,
		SettingSMTPUsername,
		settingSMTPPassword,
		SettingSMTPFrom, SettingPublicBaseURL, SettingNotifyEnabled, SettingNotifyTime, SettingNotifyInterval, SettingNotifyUserIDs,
		SettingProbeEnabled, SettingProbeAddress, SettingProbeIMAPHost, SettingProbeIMAPPort, SettingProbeIMAPSecurity,
		SettingProbeIMAPUsername, settingProbeIMAPPassword, SettingProbeIMAPFolder, SettingProbeSendTimes,
		SettingProbeDelayMinutes, SettingProbeFailureMinutes,
	}
}

// settingProbeIMAPPassword is the "settings set" key of the probe server
// password; the value is encrypted and stored under SettingProbeIMAPPassword.
const settingProbeIMAPPassword = "probe_imap_password"

// probeSettingKeys are the "settings set" keys of the delivery probe.
var probeSettingKeys = []string{
	SettingProbeEnabled, SettingProbeAddress, SettingProbeIMAPHost, SettingProbeIMAPPort, SettingProbeIMAPSecurity,
	SettingProbeIMAPUsername, settingProbeIMAPPassword, SettingProbeIMAPFolder, SettingProbeSendTimes,
	SettingProbeDelayMinutes, SettingProbeFailureMinutes,
}

// applyProbeSetting sets one delivery probe setting through the validation
// of the Web settings (SaveProbeSettings); "" restores the default ("" for
// the address, the host and the username; no password; switched off), and
// probe_send_times "none" sets no send time.
func applyProbeSetting(db *sql.DB, key, value string, loadKey func() ([]byte, error)) (string, error) {
	in := &ProbeInput{}
	shown := value
	var masterKey []byte
	intArg := func(def int, lo, hi int) (*int, error) {
		if value == "" {
			return &def, nil
		}
		n, err := strconv.Atoi(value)
		if err != nil {
			return nil, NewExitErrorf(ExitArgument, "%s must be an integer between %d and %d", key, lo, hi)
		}
		return &n, nil
	}
	var err error
	switch key {
	case SettingProbeEnabled:
		enabled := false
		if value != "" {
			if enabled, err = ParseBoolSetting(value); err != nil {
				return "", NewExitError(ExitArgument, err.Error())
			}
		}
		in.Enabled = &enabled
		shown = strconv.FormatBool(enabled)
	case SettingProbeAddress:
		in.Address = &value
		shown = strings.ToLower(value)
	case SettingProbeIMAPHost:
		in.IMAPHost = &value
	case SettingProbeIMAPPort:
		if in.IMAPPort, err = intArg(DefaultIMAPPort, 1, 65535); err != nil {
			return "", err
		}
		shown = strconv.Itoa(*in.IMAPPort)
	case SettingProbeIMAPSecurity:
		in.IMAPSecurity = &value
		shown = strings.ToLower(value)
	case SettingProbeIMAPUsername:
		in.IMAPUsername = &value
	case settingProbeIMAPPassword:
		if value == "" {
			return "", models.DeleteSetting(db, SettingProbeIMAPPassword)
		}
		if loadKey == nil {
			return "", errors.New("the master key is required to store the probe IMAP password")
		}
		if masterKey, err = loadKey(); err != nil {
			return "", err
		}
		in.IMAPPassword = &value
		shown = "(encrypted)"
	case SettingProbeIMAPFolder:
		in.IMAPFolder = &value
	case SettingProbeSendTimes:
		times := strings.Split(value, ",")
		switch {
		case value == "":
			times = strings.Split(DefaultProbeSendTimes, ",")
		case strings.EqualFold(value, CheckTimesNone):
			times = []string{}
		}
		in.SendTimes = &times
	case SettingProbeDelayMinutes:
		if in.DelayMinutes, err = intArg(DefaultProbeDelayMinutes, MinProbeMinutes, MaxProbeMinutes); err != nil {
			return "", err
		}
		shown = strconv.Itoa(*in.DelayMinutes)
	case SettingProbeFailureMinutes:
		if in.FailureMinutes, err = intArg(DefaultProbeFailureMinutes, MinProbeMinutes, MaxProbeMinutes); err != nil {
			return "", err
		}
		shown = strconv.Itoa(*in.FailureMinutes)
	}
	if err := SaveProbeSettings(db, masterKey, in); err != nil {
		if errors.Is(err, ErrProbeIMAPPasswordRequired) {
			return "", NewExitErrorf(ExitArgument, "%v (remove the stored password with: settings set %s \"\", change the setting, then set the password again)", err, settingProbeIMAPPassword)
		}
		return "", NewExitError(ExitArgument, err.Error())
	}
	if key == SettingProbeSendTimes {
		shown = displayList(ResolveProbeSendTimes(db), CheckTimesNone)
	}
	return shown, nil
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
	switch key {
	case SettingDNSCheckEnabled, SettingDNSCheckResolvers, SettingDNSCheckSystemResolverOnly:
		// Restoring a default may break the rule these share, so "" is
		// checked here too.
		return applyDNSCheckSetting(db, key, value)
	}
	if slices.Contains(probeSettingKeys, key) {
		// The two thresholds and switching the probe on depend on other
		// probe settings, so "" is checked here too.
		return applyProbeSetting(db, key, value, loadKey)
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
	case SettingAgentModel:
		if err := CheckAgentSettings(ResolveAgentProvider(db), value, ResolveAgentReasoningEffort(db)); err != nil {
			return argErr(fmt.Errorf("%w (change %s first)", err, SettingAgentReasoningEffort))
		}
		return value, SetAgentModel(db, value)
	case SettingAgentReasoningEffort:
		if err := CheckAgentSettings(ResolveAgentProvider(db), ResolveAgentModel(db), value); err != nil {
			return argErr(err)
		}
		return value, SetAgentReasoningEffort(db, value)
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
	case SettingCleanupTime:
		norm, perr := ParseCleanupTime(value)
		if perr != nil {
			return argErr(perr)
		}
		_, err := SaveCleanupTime(db, norm)
		return norm, err
	case SettingRecheckDays:
		// "category=days" pairs; the categories not named keep their value.
		days, perr := ParseRecheckDays(value)
		if perr != nil {
			return argErr(perr)
		}
		if err := SaveRecheckDays(db, days); err != nil {
			return "", err
		}
		return FormatRecheckDays(ResolveRecheckDays(db)), nil
	case SettingOwnSendingIPv4, SettingOwnSendingIPv6, SettingOwnSendingHosts:
		// A comma-separated list replaces the saved one ("" clears it).
		ls := ownSendingSettings[key]
		list, perr := ls.parse(strings.Split(value, ","))
		if perr != nil {
			return argErr(perr)
		}
		return displayList(list, "(not set)"), ls.save(db, list)
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

// applyDNSCheckSetting sets one of dns_check_enabled, dns_check_resolvers
// (a comma-separated list, or DNSCheckResolversNone for an empty one) and
// dns_check_system_resolver_only; "" restores its default. The result is
// checked together with the saved values of the other two
// (CheckDNSCheckSettings), so a change that leaves the enabled verification
// without a resolver is an argument error and nothing is saved. A list of
// commas and blanks is refused before that check, with its own message
// whether the verification is on or off.
func applyDNSCheckSetting(db *sql.DB, key, value string) (string, error) {
	var enabled, systemOnly *bool
	var resolvers *[]string
	var shown string
	switch key {
	case SettingDNSCheckEnabled, SettingDNSCheckSystemResolverOnly:
		b := DefaultDNSCheckEnabled
		if key == SettingDNSCheckSystemResolverOnly {
			b = DefaultDNSCheckSystemResolverOnly
		}
		if value != "" {
			parsed, err := ParseBoolSetting(value)
			if err != nil {
				return "", NewExitError(ExitArgument, err.Error())
			}
			b = parsed
		}
		if key == SettingDNSCheckEnabled {
			enabled = &b
		} else {
			systemOnly = &b
		}
		shown = strconv.FormatBool(b)
	case SettingDNSCheckResolvers:
		list := DefaultDNSCheckResolvers()
		switch {
		case strings.EqualFold(value, DNSCheckResolversNone):
			list = []string{}
		case value != "":
			list = strings.Split(value, ",")
			parsed, err := ParseDNSCheckResolvers(list)
			if err != nil {
				return "", NewExitError(ExitArgument, err.Error())
			}
			if len(parsed) == 0 {
				// Only the word none saves an empty list (not a value of
				// commas and blanks).
				return "", NewExitErrorf(ExitArgument, "%s lists no address (use %s to save an empty list)",
					SettingDNSCheckResolvers, DNSCheckResolversNone)
			}
		}
		resolvers = &list
	}
	cfg, err := CheckDNSCheckSettings(db, enabled, resolvers, systemOnly)
	if err != nil {
		return "", NewExitError(ExitArgument, err.Error())
	}
	if resolvers != nil {
		shown = displayList(cfg.Resolvers, DNSCheckResolversNone)
	}
	return shown, SaveDNSCheck(db, enabled, resolvers, systemOnly)
}

// printProbeSettings prints the delivery probe settings ("settings show").
func printProbeSettings(db *sql.DB) {
	probe, err := ResolveProbeSettings(db, nil)
	if err != nil {
		fmt.Printf("%-22s %v\n", "probe:", err)
		return
	}
	password := "(not set)"
	if probe.PasswordSet {
		password = "(set)"
	}
	fmt.Printf("%-22s %v\n", SettingProbeEnabled+":", probe.Enabled)
	fmt.Printf("%-22s %s\n", SettingProbeAddress+":", probe.Address)
	fmt.Printf("%-22s %s\n", SettingProbeIMAPHost+":", probe.Conn.ImapHost)
	fmt.Printf("%-22s %d\n", SettingProbeIMAPPort+":", probe.Conn.ImapPort)
	fmt.Printf("%-22s %s\n", SettingProbeIMAPSecurity+":", probe.Conn.ImapSecurity)
	fmt.Printf("%-22s %s\n", SettingProbeIMAPUsername+":", probe.Conn.ImapUsername)
	fmt.Printf("%-22s %s\n", settingProbeIMAPPassword+":", password)
	fmt.Printf("%-22s %s\n", SettingProbeIMAPFolder+":", probe.Conn.Folder)
	fmt.Printf("%-22s %s\n", SettingProbeSendTimes+":", displayList(probe.SendTimes, CheckTimesNone))
	fmt.Printf("%-22s %d\n", SettingProbeDelayMinutes+":", probe.DelayMinutes)
	fmt.Printf("%-22s %d\n", SettingProbeFailureMinutes+":", probe.FailureMinutes)
	fmt.Printf("%-22s %s\n", "probe_last_send_at:", formatNullTime(probe.LastSendAt))
	fmt.Printf("%-22s %s\n", "probe_next_check_at:", formatNullTime(probe.NextCheckAt))
	if probe.LastCheckError != "" {
		fmt.Printf("%-22s %s\n", "probe_last_check_error:", probe.LastCheckError)
	}
}
