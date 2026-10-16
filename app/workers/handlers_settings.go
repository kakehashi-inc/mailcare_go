package workers

import (
	"net/http"
	"strings"
	"time"

	"mailcare/app/models"
	"mailcare/app/modules"
	"mailcare/app/modules/agent"
	"mailcare/app/modules/wording"
)

// settingsDTO builds the settings payload. The server internals (worker
// count, listen address and port, data directory) and what reveals the mail
// infrastructure (the sending sources, the SMTP hosts of the mailboxes,
// the addresses without one and the DNS resolvers) are included for
// administrators only; the switches of the DNS check reveal nothing and are
// shown to everyone. It fails only when the mailboxes cannot be listed for
// an administrator (sending_source_mailbox_hosts).
func (c *core) settingsDTO(u *models.User) (map[string]any, error) {
	providers := agent.Providers()
	if providers == nil {
		providers = []agent.ProviderStatus{}
	}
	dnsCheck := modules.ResolveDNSCheck(c.db)
	dto := map[string]any{
		"check_times":    modules.ResolveCheckTimes(c.db),
		"agent_provider": modules.ResolveAgentProvider(c.db),
		"agent_model":    modules.ResolveAgentModel(c.db),
		// agent_reasoning_effort: "" = the setting of the CLI.
		"agent_reasoning_effort": modules.ResolveAgentReasoningEffort(c.db),
		"agent_enabled":          modules.ResolveAgentEnabled(c.db),
		"agent_keep_days":        modules.ResolveAgentKeepDays(c.db),
		"mail_keep_days":         modules.ResolveMailKeepDays(c.db),
		"cleanup_time":           modules.ResolveCleanupTime(c.db),
		// recheck_days: the re-check days of every category.
		"recheck_days":                   modules.ResolveRecheckDays(c.db),
		"dns_check_enabled":              dnsCheck.Enabled,
		"dns_check_system_resolver_only": dnsCheck.SystemOnly,
		"providers":                      providers,
		// check_times, cleanup_time and notify_time are interpreted in this zone.
		"server_timezone": modules.ServerTimezone(),
	}
	if u != nil && u.Role == modules.RoleAdmin {
		sources, err := modules.ResolveSendingSources(c.db)
		if err != nil {
			return nil, err
		}
		dto["workers"] = c.jm.Workers()
		dto["web_listen"] = c.webListen
		dto["web_port"] = c.webPort
		dto["data_dir"] = c.dataDir
		// Sending sources (arrays, [] when not set) and the resolvers of
		// the DNS lookup failure verification (kept but not used while
		// dns_check_system_resolver_only is on).
		// sending_source_mailbox_hosts is read-only: the SMTP hosts of the
		// mailboxes, set on each mailbox, which count as source host names
		// next to sending_source_hosts. sending_source_mailboxes_without_host
		// is read-only too: the addresses whose notices about mail never sent
		// from the sending sources are not judged for want of a host name (empty while
		// sending_source_hosts is set).
		dto["sending_source_ipv4"] = sources.IPv4
		dto["sending_source_ipv6"] = sources.IPv6
		dto["sending_source_hosts"] = sources.Hosts
		dto["sending_source_mailbox_hosts"] = sources.MailboxHosts
		dto["sending_source_mailboxes_without_host"] = sources.MailboxesWithoutHost
		dto["dns_check_resolvers"] = dnsCheck.Resolvers
		// Why an analysis asked for now cannot run ("" when it can: the CLI
		// of the provider is there and no recorded usage limit lasts): the
		// analyze buttons are enabled by it and show the reason.
		dto["analyze_refusal"] = ""
		if err := modules.AnalyzeRefusal(c.db, time.Now()); err != nil {
			if m, ok := wording.As(err); ok {
				dto["analyze_refusal"] = m.Key
			}
		}
	}
	return dto, nil
}

// writeSettings answers the settings payload of the caller.
func (c *core) writeSettings(w http.ResponseWriter, r *http.Request) {
	dto, err := c.settingsDTO(userFrom(r))
	if err != nil {
		writeInternalError(w, "failed to read the settings", err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

func (c *core) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	c.writeSettings(w, r)
}

// handleUpdateSettings changes the check times, the agent provider, model
// and reasoning level (validated together; a provider change without a
// model or level clears them, and a level the model is known not to accept
// refuses the whole request), the agent switch, the retention of the agent run directories, the retention
// of fetched mails, the time of the daily cleanup, the re-check days of the categories named (the others
// keep theirs), the sending sources, the DNS lookup failure verification (switch, resolvers and the
// OS-resolver-only switch, validated together with the saved values of the ones absent) and the worker count
// (applied to the job manager once saved). Absent fields are left unchanged and unknown ones (the read-only
// sending_source_mailbox_hosts and sending_source_mailboxes_without_host among them) are ignored. Every field is
// validated before anything is written, and the changes are saved in one transaction
// (modules.SettingsUpdate) under the settings lock (modules.UpdateSettings): nothing is
// saved when any field is refused or a write fails.
func (c *core) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CheckTimes         *[]string      `json:"check_times"`
		AgentProvider      *string        `json:"agent_provider"`
		AgentModel         *string        `json:"agent_model"`
		AgentEffort        *string        `json:"agent_reasoning_effort"`
		AgentEnabled       *bool          `json:"agent_enabled"`
		AgentKeepDays      *int           `json:"agent_keep_days"`
		MailKeepDays       *int           `json:"mail_keep_days"`
		CleanupTime        *string        `json:"cleanup_time"`
		RecheckDays        map[string]int `json:"recheck_days"`
		SendingSourceIPv4  *[]string      `json:"sending_source_ipv4"`
		SendingSourceIPv6  *[]string      `json:"sending_source_ipv6"`
		SendingSourceHosts *[]string      `json:"sending_source_hosts"`
		DNSCheckEnabled    *bool          `json:"dns_check_enabled"`
		DNSCheckResolvers  *[]string      `json:"dns_check_resolvers"`
		DNSCheckSystemOnly *bool          `json:"dns_check_system_resolver_only"`
		Workers            *int           `json:"workers"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	// The fields are validated, against the saved values where the result
	// depends on them, and saved under the settings lock
	// (modules.UpdateSettings), so that a concurrent change cannot make the
	// combination of the two one the validation refuses.
	fill := func(u *modules.SettingsUpdate) error {
		if body.Workers != nil {
			if *body.Workers < 1 || *body.Workers > modules.MaxWorkers {
				return wording.New("validation.common.numberOutOfRange", "workers out of range").
					With("min", 1).With("max", modules.MaxWorkers)
			}
			u.Workers(*body.Workers)
		}
		if body.AgentKeepDays != nil {
			if err := u.AgentKeepDays(*body.AgentKeepDays); err != nil {
				return err
			}
		}
		if body.MailKeepDays != nil {
			if err := u.MailKeepDays(*body.MailKeepDays); err != nil {
				return err
			}
		}
		if body.CleanupTime != nil {
			if _, err := u.CleanupTime(*body.CleanupTime); err != nil {
				return err
			}
		}
		if err := modules.ValidateRecheckDays(body.RecheckDays); err != nil {
			return err
		}
		if len(body.RecheckDays) > 0 {
			if err := u.RecheckDays(c.db, body.RecheckDays); err != nil {
				return err
			}
		}
		if body.CheckTimes != nil {
			times, err := modules.ParseCheckTimes(*body.CheckTimes)
			if err != nil {
				return err
			}
			u.CheckTimes(times)
		}
		// The sending source lists are normalized once here and saved as such.
		sendingSource := []struct {
			in  *[]string
			set func([]string) error
		}{
			{body.SendingSourceIPv4, u.SendingSourceIPv4},
			{body.SendingSourceIPv6, u.SendingSourceIPv6},
			{body.SendingSourceHosts, u.SendingSourceHosts},
		}
		for _, o := range sendingSource {
			if o.in == nil {
				continue
			}
			if err := o.set(*o.in); err != nil {
				return err
			}
		}
		// The DNS check switches and resolvers are checked as the combination
		// they give with the saved values of the ones absent; a request
		// without any of them is not refused for the saved combination.
		if body.DNSCheckEnabled != nil || body.DNSCheckResolvers != nil || body.DNSCheckSystemOnly != nil {
			if err := u.DNSCheck(c.db, body.DNSCheckEnabled, body.DNSCheckResolvers, body.DNSCheckSystemOnly); err != nil {
				return err
			}
		}
		var provider *string
		if body.AgentProvider != nil {
			name := strings.TrimSpace(*body.AgentProvider)
			if !agent.IsValidProvider(name) {
				return wording.New("validation.agent.providerUnknown", "unknown agent provider")
			}
			provider = &name
		}
		if body.AgentProvider != nil || body.AgentModel != nil || body.AgentEffort != nil {
			if err := u.AgentSettings(c.db, provider, body.AgentModel, body.AgentEffort); err != nil {
				return err
			}
		}
		if body.AgentEnabled != nil {
			u.AgentEnabled(*body.AgentEnabled)
		}
		return nil
	}
	if err := modules.UpdateSettings(c.db, fill); err != nil {
		if _, refused := wording.As(err); refused {
			writeErrorMessage(w, r, err)
		} else {
			writeInternalError(w, "failed to save the settings", err)
		}
		return
	}
	if body.Workers != nil {
		c.jm.SetWorkers(*body.Workers)
	}
	c.writeSettings(w, r)
}
