package modules

import (
	"fmt"
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
	Set  SettingsSetCmd  `cmd:"" help:"Set a setting (web_listen, web_port, workers, check_times, agent_provider, agent_enabled, cookie_ttl_hours)"`
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
	values := map[string]interface{}{
		SettingWebListen:      ResolveWebListen(db, ""),
		SettingWebPort:        ResolveWebPort(db, 0),
		SettingWorkers:        ResolveWorkers(db),
		SettingCheckTimes:     ResolveCheckTimes(db),
		SettingAgentProvider:  provider,
		SettingAgentEnabled:   ResolveAgentEnabled(db),
		SettingCookieTTLHours: ResolveCookieTTLHours(db),
	}
	if c.JSON {
		values["data_dir"] = dataDir
		values["providers"] = agent.Providers()
		printJSON(values)
		return nil
	}
	fmt.Printf("%-18s %s\n", "data_dir:", dataDir)
	fmt.Printf("%-18s %v\n", SettingWebListen+":", values[SettingWebListen])
	fmt.Printf("%-18s %v\n", SettingWebPort+":", values[SettingWebPort])
	fmt.Printf("%-18s %v\n", SettingWorkers+":", values[SettingWorkers])
	fmt.Printf("%-18s %s\n", SettingCheckTimes+":", FormatCheckTimes(ResolveCheckTimes(db)))
	available := "not available"
	if agent.ProviderAvailable(provider) {
		available = "available"
	}
	fmt.Printf("%-18s %s (%s)\n", SettingAgentProvider+":", provider, available)
	fmt.Printf("%-18s %v\n", SettingAgentEnabled+":", values[SettingAgentEnabled])
	fmt.Printf("%-18s %v\n", SettingCookieTTLHours+":", values[SettingCookieTTLHours])
	return nil
}

// SettingsSetCmd changes one setting.
type SettingsSetCmd struct {
	Key   string `arg:"" help:"Setting key"`
	Value string `arg:"" help:"New value (an empty string restores the default)"`
}

func (c *SettingsSetCmd) Run() error {
	db, err := openDBForCLI()
	if err != nil {
		return err
	}
	defer db.Close()
	key := strings.ToLower(strings.TrimSpace(c.Key))
	value := strings.TrimSpace(c.Value)
	if value == "" {
		if err := models.DeleteSetting(db, key); err != nil {
			return NewExitError(ExitConfig, err.Error())
		}
		fmt.Printf("%s restored to its default\n", key)
		return nil
	}
	switch key {
	case SettingWebListen:
		err = PersistSetting(db, key, value, value == DefaultWebListenAddr)
	case SettingWebPort, SettingCookieTTLHours:
		n, perr := strconv.Atoi(value)
		if perr != nil || n < 1 || (key == SettingWebPort && n > 65535) {
			return NewExitErrorf(ExitArgument, "%s must be a positive integer", key)
		}
		def := DefaultWebPort
		if key == SettingCookieTTLHours {
			def = DefaultCookieTTLHours
		}
		err = PersistSetting(db, key, strconv.Itoa(n), n == def)
	case SettingWorkers:
		n, perr := ParseWorkers(value)
		if perr != nil {
			return NewExitError(ExitArgument, perr.Error())
		}
		err = SaveWorkers(db, n)
		value = strconv.Itoa(n)
		fmt.Printf("(a running server applies the new worker count at its next start; use the Web settings to change it live)\n")
	case SettingCheckTimes:
		times, perr := ParseCheckTimes([]string{value})
		if perr != nil {
			return NewExitError(ExitArgument, perr.Error())
		}
		err = SaveCheckTimes(db, times)
		value = FormatCheckTimes(times)
	case SettingAgentProvider:
		if !agent.IsValidProvider(value) {
			names := make([]string, 0)
			for _, p := range agent.Providers() {
				names = append(names, p.Name)
			}
			return NewExitErrorf(ExitArgument, "unknown agent provider %q (registered: %s)", value, strings.Join(names, ", "))
		}
		err = SetAgentProvider(db, value)
	case SettingAgentEnabled:
		enabled, perr := ParseBoolSetting(value)
		if perr != nil {
			return NewExitError(ExitArgument, perr.Error())
		}
		err = SetAgentEnabled(db, enabled)
		value = strconv.FormatBool(enabled)
	default:
		return NewExitErrorf(ExitArgument, "unknown setting %q", key)
	}
	if err != nil {
		return NewExitError(ExitConfig, err.Error())
	}
	fmt.Printf("%s = %s\n", key, value)
	return nil
}
