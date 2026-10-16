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
	"mailcare/app/modules/mailengine"
	"mailcare/app/modules/wording"
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
	Set  SettingsSetCmd  `cmd:"" help:"Set a setting (web_listen, web_port, workers, check_times, agent_provider, agent_enabled, agent_keep_days, mail_keep_days, cleanup_time, sending_source_ipv4, sending_source_ipv6, sending_source_hosts, dns_check_enabled, dns_check_resolvers, dns_check_system_resolver_only, cookie_ttl_hours, smtp_host, smtp_port, smtp_security, smtp_username, smtp_password, smtp_from, public_base_url, notify_enabled, notify_time, notify_interval_days, notify_user_ids)"`
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
	sources, err := ResolveSendingSources(db)
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
		SettingNotifyTime:           notify.Times,
		SettingNotifyInterval:       notify.IntervalDays,
		SettingNotifyUserIDs:        notify.UserIDs,
		SettingNotifyLastSent:       rfc3339OrNull(notify.LastSentAt),
		// The date of the last daily cleanup is recorded by the scheduler
		// and shown for information only.
		SettingCleanupLastRunDate: stringOrNull(models.GetSetting(db, SettingCleanupLastRunDate)),
		// Sending sources and the DNS lookup failure verification; the
		// resolvers are kept but not used while
		// dns_check_system_resolver_only is on.
		SettingSendingSourceIPv4:  sources.IPv4,
		SettingSendingSourceIPv6:  sources.IPv6,
		SettingSendingSourceHosts: sources.Hosts,
		// The SMTP hosts of the mailboxes (mailbox update --smtp-host), source
		// host names next to sending_source_hosts; shown, not set here.
		"sending_source_mailbox_hosts": sources.MailboxHosts,
		// The addresses without an SMTP host while sending_source_hosts is
		// empty: their notices about mail never sent from the sending sources
		// are not judged (shown, derived).
		"sending_source_mailboxes_without_host": sources.MailboxesWithoutHost,
		SettingDNSCheckEnabled:                  dnsCheck.Enabled,
		SettingDNSCheckResolvers:                dnsCheck.Resolvers,
		SettingDNSCheckSystemResolverOnly:       dnsCheck.SystemOnly,
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
		values[SettingProbeSubjectTag] = probe.SubjectTag
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
	fmt.Printf("%-31s %s\n", SettingSendingSourceIPv4+":", displayList(sources.IPv4, "(not set)"))
	fmt.Printf("%-31s %s\n", SettingSendingSourceIPv6+":", displayList(sources.IPv6, "(not set)"))
	fmt.Printf("%-31s %s\n", SettingSendingSourceHosts+":", displayList(sources.Hosts, "(not set)"))
	fmt.Printf("%-31s %s\n", "sending_source_mailbox_hosts:", displayList(sources.MailboxHosts, "(none: set with mailbox update --smtp-host)"))
	if len(sources.MailboxesWithoutHost) > 0 {
		fmt.Printf("%-31s %s (backscatter is not judged for them: set mailbox update --smtp-host or sending_source_hosts)\n",
			"mailboxes without a host name:", strings.Join(sources.MailboxesWithoutHost, ", "))
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
	fmt.Printf("%-22s %v\n", SettingNotifyTime+":", FormatCheckTimes(notify.Times))
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
	Value *string `arg:"" optional:"" help:"New value (an empty string restores the default; check_times also accepts none to disable the automatic checks; sending_source_ipv4, sending_source_ipv6, sending_source_hosts and dns_check_resolvers take a comma-separated list, and dns_check_resolvers accepts none to save an empty list). Omit it for smtp_password or probe_imap_password to be prompted for the password (hidden on a terminal; a line on piped input)"`
}

func (c *SettingsSetCmd) Run() error {
	key := strings.ToLower(strings.TrimSpace(c.Key))
	var value string
	switch {
	case c.Value != nil:
		value = strings.TrimSpace(*c.Value)
	case key == settingSMTPPassword || key == settingProbeIMAPPassword:
		label := "SMTP password"
		if key == settingProbeIMAPPassword {
			label = "Probe server password"
		}
		password, err := promptPassword(label)
		if err != nil {
			return err
		}
		if password == "" {
			return NewExitErrorf(ExitArgument, "no password given (use %s set %s \"\" to remove the stored password)", "settings", key)
		}
		value = password
	default:
		return NewExitErrorf(ExitArgument, "a value is required for %s (only %s and %s may be omitted to be prompted)", key,
			settingSMTPPassword, settingProbeIMAPPassword)
	}
	db, err := openDBForCLI()
	if err != nil {
		return err
	}
	defer db.Close()
	// A change that needs the SMTP password with it (a username set, or the
	// connection of a stored password changed) asks for it.
	askSMTPPassword := func() (string, error) { return promptPassword("SMTP password") }
	shown, err := applySetting(db, key, value, func() ([]byte, error) { return loadKeyForCLI(db) }, askSMTPPassword)
	if err != nil {
		var exitErr *ExitError
		if errors.As(err, &exitErr) {
			return err
		}
		return NewExitError(ExitConfig, err.Error())
	}
	if slices.Contains(probeSettingKeys, key) {
		if probe, err := ResolveProbeSettings(db, nil); err == nil && probe.Conn.ImapSecurity == IMAPSecurityNone &&
			(probe.Conn.ImapUsername != "" || probe.PasswordSet) {
			warnPlaintextCredentials("the probe server")
		}
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
		SettingSendingSourceIPv4, SettingSendingSourceIPv6, SettingSendingSourceHosts,
		SettingDNSCheckEnabled, SettingDNSCheckResolvers, SettingDNSCheckSystemResolverOnly,
		SettingSMTPHost, SettingSMTPPort, SettingSMTPSecurity,
		SettingSMTPUsername,
		settingSMTPPassword,
		SettingSMTPFrom, SettingPublicBaseURL, SettingNotifyEnabled, SettingNotifyTime, SettingNotifyInterval, SettingNotifyUserIDs,
		SettingProbeEnabled, SettingProbeAddress, SettingProbeIMAPHost, SettingProbeIMAPPort, SettingProbeIMAPSecurity,
		SettingProbeIMAPUsername, settingProbeIMAPPassword, SettingProbeIMAPFolder, SettingProbeSendTimes,
		SettingProbeDelayMinutes, SettingProbeFailureMinutes, SettingProbeSubjectTag,
	}
}

// settingProbeIMAPPassword is the "settings set" key of the probe server
// password; the value is encrypted and stored under SettingProbeIMAPPassword.
const settingProbeIMAPPassword = "probe_imap_password"

// probeSettingKeys are the "settings set" keys of the delivery probe.
var probeSettingKeys = []string{
	SettingProbeEnabled, SettingProbeAddress, SettingProbeIMAPHost, SettingProbeIMAPPort, SettingProbeIMAPSecurity,
	SettingProbeIMAPUsername, settingProbeIMAPPassword, SettingProbeIMAPFolder, SettingProbeSendTimes,
	SettingProbeDelayMinutes, SettingProbeFailureMinutes, SettingProbeSubjectTag,
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
			in.ClearIMAPPassword = true
			break
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
	case SettingProbeSubjectTag:
		// Trimmed here as Probe() trims it, so the value shown is the one saved.
		if value = strings.TrimSpace(value); value == "" {
			value = DefaultProbeSubjectTag
		}
		in.SubjectTag = &value
		shown = value
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
			return "", NewExitErrorf(ExitArgument, "%v (while the probe is on, switch it off first with: settings set %s 0; then remove the stored password with: settings set %s \"\", change the setting, set the password again and switch the probe on)",
				err, SettingProbeEnabled, settingProbeIMAPPassword)
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
// the value as stored (for display). An empty value restores the default,
// through the same validation. loadKey supplies the master key when the
// SMTP password is set (nil when a caller never sets it). Validation
// failures are ExitErrors with ExitArgument.
func ApplySetting(db *sql.DB, key, value string, loadKey func() ([]byte, error)) (string, error) {
	return applySetting(db, key, value, loadKey, nil)
}

// applySetting is ApplySetting that, given askSMTPPassword, asks for the
// SMTP password when a change of the SMTP host, port, security or username
// needs it (one key per command, so the password cannot come as another
// argument) and saves both together.
func applySetting(db *sql.DB, key, value string, loadKey func() ([]byte, error), askSMTPPassword func() (string, error)) (string, error) {
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
	// "" restores the default through the same validation as any other
	// value, so a default that breaks a rule shared with other settings is
	// refused like any other value.
	argErr := func(err error) (string, error) { return "", NewExitError(ExitArgument, err.Error()) }
	switch key {
	case SettingWebListen:
		addr, perr := ParseWebListen(value)
		if perr != nil {
			return argErr(perr)
		}
		return addr, persistLocked(db, key, addr, addr == DefaultWebListenAddr)
	case SettingWebPort, SettingCookieTTLHours:
		def := DefaultWebPort
		if key == SettingCookieTTLHours {
			def = DefaultCookieTTLHours
		}
		n := def
		if value != "" {
			var perr error
			if n, perr = strconv.Atoi(value); perr != nil || n < 1 || (key == SettingWebPort && n > 65535) {
				return "", NewExitErrorf(ExitArgument, "%s must be a positive integer", key)
			}
		}
		return strconv.Itoa(n), persistLocked(db, key, strconv.Itoa(n), n == def)
	case SettingWorkers:
		n := DefaultWorkers
		if value != "" {
			var perr error
			if n, perr = ParseWorkers(value); perr != nil {
				return argErr(perr)
			}
		}
		return strconv.Itoa(n), SaveWorkers(db, n)
	case SettingCheckTimes:
		// "none" disables the automatic checks ("" restores the default).
		switch {
		case value == "":
			return FormatCheckTimes(DefaultCheckTimeList()), SaveCheckTimes(db, DefaultCheckTimeList())
		case strings.EqualFold(value, CheckTimesNone):
			return CheckTimesNone, SaveCheckTimes(db, nil)
		}
		times, perr := ParseCheckTimes([]string{value})
		if perr != nil {
			return argErr(perr)
		}
		return FormatCheckTimes(times), SaveCheckTimes(db, times)
	case SettingAgentProvider:
		if value == "" {
			// Switching provider clears the model and the reasoning level of
			// the previous one (SetAgentProvider).
			value = DefaultAgentProvider
		}
		if !agent.IsValidProvider(value) {
			names := make([]string, 0)
			for _, p := range agent.Providers() {
				names = append(names, p.Name)
			}
			return "", NewExitErrorf(ExitArgument, "unknown agent provider %q (registered: %s)", value, strings.Join(names, ", "))
		}
		return value, SetAgentProvider(db, value)
	case SettingAgentModel:
		// Checked against the saved reasoning level under the settings lock
		// (SetAgentModel); a refusal is an argument error.
		if err := SetAgentModel(db, value); err != nil {
			if _, refused := wording.As(err); refused {
				return argErr(fmt.Errorf("%w (change %s first)", err, SettingAgentReasoningEffort))
			}
			return "", err
		}
		return value, nil
	case SettingAgentReasoningEffort:
		if err := SetAgentReasoningEffort(db, value); err != nil {
			if _, refused := wording.As(err); refused {
				return argErr(err)
			}
			return "", err
		}
		return value, nil
	case SettingAgentEnabled:
		enabled := true
		if value != "" {
			var perr error
			if enabled, perr = ParseBoolSetting(value); perr != nil {
				return argErr(perr)
			}
		}
		return strconv.FormatBool(enabled), SetAgentEnabled(db, enabled)
	case SettingAgentKeepDays:
		n := DefaultAgentKeepDays
		if value != "" {
			var perr error
			if n, perr = ParseAgentKeepDays(value); perr != nil {
				return argErr(perr)
			}
		}
		return strconv.Itoa(n), SaveAgentKeepDays(db, n)
	case SettingMailKeepDays:
		n := DefaultMailKeepDays
		if value != "" {
			var perr error
			if n, perr = ParseMailKeepDays(value); perr != nil {
				return argErr(perr)
			}
		}
		return strconv.Itoa(n), SaveMailKeepDays(db, n)
	case SettingCleanupTime:
		if value == "" {
			value = DefaultCleanupTime
		}
		norm, perr := ParseCleanupTime(value)
		if perr != nil {
			return argErr(perr)
		}
		_, err := SaveCleanupTime(db, norm)
		return norm, err
	case SettingRecheckDays:
		// "category=days" pairs; the categories not named keep their value
		// ("" restores the default of every category).
		days, perr := ParseRecheckDays(value)
		if perr != nil {
			return argErr(perr)
		}
		if value == "" {
			for _, c := range KnownCategories() {
				days[c] = mailengine.DefaultRecheckDays(c)
			}
		}
		if err := SaveRecheckDays(db, days); err != nil {
			return "", err
		}
		return FormatRecheckDays(ResolveRecheckDays(db)), nil
	case SettingSendingSourceIPv4, SettingSendingSourceIPv6, SettingSendingSourceHosts:
		// A comma-separated list replaces the saved one ("" clears it).
		ls := sendingSourceSettings[key]
		var entries []string
		if value != "" {
			entries = strings.Split(value, ",")
		}
		list, perr := ls.parse(entries)
		if perr != nil {
			return argErr(perr)
		}
		return displayList(list, "(not set)"), ls.save(db, list)
	}
	// Notification keys share the validation of the Web settings, "" too
	// (the default of each: no host, sender or username, the default port,
	// connection security, time and interval, notifications off, no
	// recipient; the password removed).
	in := &NotificationInput{}
	shown := value
	var masterKey []byte
	switch key {
	case SettingSMTPHost:
		in.SMTPHost = &value
	case SettingSMTPPort:
		n := DefaultSMTPPort
		if value != "" {
			var perr error
			if n, perr = strconv.Atoi(value); perr != nil {
				return "", NewExitErrorf(ExitArgument, "%s must be an integer between 1 and 65535", key)
			}
		}
		in.SMTPPort = &n
	case SettingSMTPSecurity:
		in.SMTPSecurity = &value
		shown = strings.ToLower(value)
	case SettingSMTPUsername:
		in.SMTPUsername = &value
	case settingSMTPPassword:
		if value == "" {
			in.ClearSMTPPassword = true
			break
		}
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
		enabled := false
		if value != "" {
			var perr error
			if enabled, perr = ParseBoolSetting(value); perr != nil {
				return argErr(perr)
			}
		}
		in.Enabled = &enabled
		shown = strconv.FormatBool(enabled)
	case SettingNotifyTime:
		times, perr := ParseNotifyTimes([]string{value})
		if perr != nil {
			return argErr(perr)
		}
		in.Times = &times
		shown = FormatCheckTimes(times)
	case SettingNotifyInterval:
		n := DefaultNotifyIntervalDays
		if value != "" {
			var perr error
			if n, perr = strconv.Atoi(value); perr != nil {
				return "", NewExitErrorf(ExitArgument, "%s must be an integer between %d and %d", key, MinNotifyIntervalDays, MaxNotifyIntervalDays)
			}
		}
		in.IntervalDays = &n
	case SettingNotifyUserIDs:
		ids := []int64{}
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
	if key == settingSMTPPassword && value != "" {
		// The password belongs to the username (without one it is not kept).
		if saved, err := ResolveNotificationSettings(db, nil); err == nil && saved.SMTP.Username == "" {
			return "", NewExitErrorf(ExitArgument, "set %s first (it asks for the password)", SettingSMTPUsername)
		}
	}
	err := SaveNotificationSettings(db, masterKey, in)
	connectionKey := key == SettingSMTPHost || key == SettingSMTPPort || key == SettingSMTPSecurity || key == SettingSMTPUsername
	if connectionKey && askSMTPPassword != nil && (errors.Is(err, ErrSMTPPasswordRequired) || errors.Is(err, errSMTPPasswordMissing)) {
		password, aerr := askSMTPPassword()
		if aerr != nil {
			return "", aerr
		}
		if password == "" {
			return "", NewExitError(ExitArgument, "no password given")
		}
		if loadKey == nil {
			return "", errors.New("the master key is required to store the SMTP password")
		}
		if masterKey, err = loadKey(); err != nil {
			return "", err
		}
		in.SMTPPassword = &password
		err = SaveNotificationSettings(db, masterKey, in)
	}
	if err != nil {
		switch {
		case errors.Is(err, ErrSMTPPasswordRequired), errors.Is(err, errSMTPPasswordMissing) && connectionKey:
			return "", NewExitErrorf(ExitArgument, "%v (settings set %s on a terminal asks for the password)", err, key)
		case errors.Is(err, errSMTPPasswordMissing):
			return "", NewExitErrorf(ExitArgument, "%v (clear %s for a server without authentication; that removes the password too)",
				err, SettingSMTPUsername)
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
// commas and blanks is refused before that check, with a separate message
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
	// Checked against the saved values of the other two and saved under the
	// settings lock (saveDNSCheck); a refusal is an argument error.
	cfg, err := saveDNSCheck(db, enabled, resolvers, systemOnly)
	if err != nil {
		if _, refused := wording.As(err); refused {
			return "", NewExitError(ExitArgument, err.Error())
		}
		return "", err
	}
	if resolvers != nil {
		shown = displayList(cfg.Resolvers, DNSCheckResolversNone)
	}
	return shown, nil
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
	fmt.Printf("%-22s %s\n", SettingProbeSubjectTag+":", probe.SubjectTag)
	fmt.Printf("%-22s %s\n", SettingProbeSendTimes+":", displayList(probe.SendTimes, CheckTimesNone))
	fmt.Printf("%-22s %d\n", SettingProbeDelayMinutes+":", probe.DelayMinutes)
	fmt.Printf("%-22s %d\n", SettingProbeFailureMinutes+":", probe.FailureMinutes)
	fmt.Printf("%-22s %s\n", "probe_last_send_at:", formatNullTime(probe.LastSendAt))
	fmt.Printf("%-22s %s\n", "probe_next_check_at:", formatNullTime(probe.NextCheckAt))
	if probe.LastCheckError != "" {
		fmt.Printf("%-22s %s\n", "probe_last_check_error:", probe.LastCheckError)
	}
	if probe.LastDeleteError != "" {
		fmt.Printf("%-22s %s\n", "probe_last_delete_error:", probe.LastDeleteError)
	}
}
