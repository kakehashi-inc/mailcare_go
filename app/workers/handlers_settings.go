package workers

import (
	"net/http"
	"strings"

	"mailcare/app/models"
	"mailcare/app/modules"
	"mailcare/app/modules/agent"
)

// settingsDTO builds the settings payload. The server internals (worker
// count, listen address and port, data directory) and what reveals the mail
// infrastructure (the own sending servers, the SMTP hosts of the mailboxes,
// the addresses without one and the DNS resolvers) are included for
// administrators only; the switches of the DNS check reveal nothing and are
// shown to everyone. It fails only when the mailboxes cannot be listed for
// an administrator (own_sending_mailbox_hosts).
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
		// agent_reasoning_effort: "" = the CLI's own setting.
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
		own, err := modules.ResolveOwnSending(c.db)
		if err != nil {
			return nil, err
		}
		dto["workers"] = c.jm.Workers()
		dto["web_listen"] = c.webListen
		dto["web_port"] = c.webPort
		dto["data_dir"] = c.dataDir
		// Own sending servers (arrays, [] when not set) and the resolvers of
		// the DNS lookup failure verification (kept but not used while
		// dns_check_system_resolver_only is on).
		// own_sending_mailbox_hosts is read-only: the SMTP hosts of the
		// mailboxes, set on each mailbox, which count as own sending host
		// names next to own_sending_hosts. own_sending_mailboxes_without_host
		// is read-only too: the addresses whose notices about mail we did
		// not send are not judged for want of a host name (empty while
		// own_sending_hosts is set).
		dto["own_sending_ipv4"] = own.IPv4
		dto["own_sending_ipv6"] = own.IPv6
		dto["own_sending_hosts"] = own.Hosts
		dto["own_sending_mailbox_hosts"] = own.MailboxHosts
		dto["own_sending_mailboxes_without_host"] = own.MailboxesWithoutHost
		dto["dns_check_resolvers"] = dnsCheck.Resolvers
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
// keep theirs), the own sending servers, the DNS lookup failure verification (switch, resolvers and the
// OS-resolver-only switch, validated together with the saved values of the ones absent) and the worker count
// (applied to the job manager once saved). Absent fields are left unchanged and unknown ones (the read-only
// own_sending_mailbox_hosts and own_sending_mailboxes_without_host among them) are ignored. Every field is
// validated before anything is written, and the changes are saved in one transaction
// (modules.SettingsUpdate): nothing is saved when any field is refused or a write fails.
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
		OwnSendingIPv4     *[]string      `json:"own_sending_ipv4"`
		OwnSendingIPv6     *[]string      `json:"own_sending_ipv6"`
		OwnSendingHosts    *[]string      `json:"own_sending_hosts"`
		DNSCheckEnabled    *bool          `json:"dns_check_enabled"`
		DNSCheckResolvers  *[]string      `json:"dns_check_resolvers"`
		DNSCheckSystemOnly *bool          `json:"dns_check_system_resolver_only"`
		Workers            *int           `json:"workers"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	var u modules.SettingsUpdate
	if body.Workers != nil {
		if *body.Workers < 1 || *body.Workers > modules.MaxWorkers {
			writeJSON(w, http.StatusBadRequest, apiMessage{Key: "validation.common.numberOutOfRange", Params: map[string]any{"min": 1, "max": modules.MaxWorkers}})
			return
		}
		u.Workers(*body.Workers)
	}
	if body.AgentKeepDays != nil {
		if err := u.AgentKeepDays(*body.AgentKeepDays); err != nil {
			writeErrorMessage(w, r, err)
			return
		}
	}
	if body.MailKeepDays != nil {
		if err := u.MailKeepDays(*body.MailKeepDays); err != nil {
			writeErrorMessage(w, r, err)
			return
		}
	}
	if body.CleanupTime != nil {
		if _, err := u.CleanupTime(*body.CleanupTime); err != nil {
			writeErrorMessage(w, r, err)
			return
		}
	}
	if err := modules.ValidateRecheckDays(body.RecheckDays); err != nil {
		writeErrorMessage(w, r, err)
		return
	}
	if len(body.RecheckDays) > 0 {
		if err := u.RecheckDays(c.db, body.RecheckDays); err != nil {
			writeErrorMessage(w, r, err)
			return
		}
	}
	if body.CheckTimes != nil {
		times, err := modules.ParseCheckTimes(*body.CheckTimes)
		if err != nil {
			writeErrorMessage(w, r, err)
			return
		}
		u.CheckTimes(times)
	}
	// The own sending lists are normalized once here and saved as such.
	ownSending := []struct {
		in  *[]string
		set func([]string) error
	}{
		{body.OwnSendingIPv4, u.OwnSendingIPv4},
		{body.OwnSendingIPv6, u.OwnSendingIPv6},
		{body.OwnSendingHosts, u.OwnSendingHosts},
	}
	for _, o := range ownSending {
		if o.in == nil {
			continue
		}
		if err := o.set(*o.in); err != nil {
			writeErrorMessage(w, r, err)
			return
		}
	}
	// The DNS check switches and resolvers are checked as the combination
	// they give with the saved values of the ones absent; a request without
	// any of them is not refused for the saved combination.
	if body.DNSCheckEnabled != nil || body.DNSCheckResolvers != nil || body.DNSCheckSystemOnly != nil {
		if err := u.DNSCheck(c.db, body.DNSCheckEnabled, body.DNSCheckResolvers, body.DNSCheckSystemOnly); err != nil {
			writeErrorMessage(w, r, err)
			return
		}
	}
	var provider *string
	if body.AgentProvider != nil {
		name := strings.TrimSpace(*body.AgentProvider)
		if !agent.IsValidProvider(name) {
			writeError(w, http.StatusBadRequest, "validation.agent.providerUnknown")
			return
		}
		provider = &name
	}
	if body.AgentProvider != nil || body.AgentModel != nil || body.AgentEffort != nil {
		if err := u.AgentSettings(c.db, provider, body.AgentModel, body.AgentEffort); err != nil {
			writeErrorMessage(w, r, err)
			return
		}
	}
	if body.AgentEnabled != nil {
		u.AgentEnabled(*body.AgentEnabled)
	}
	if err := u.Save(c.db); err != nil {
		writeInternalError(w, "failed to save the settings", err)
		return
	}
	if body.Workers != nil {
		c.jm.SetWorkers(*body.Workers)
	}
	c.writeSettings(w, r)
}
