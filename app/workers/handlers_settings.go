package workers

import (
	"net/http"
	"strings"

	"mailcare/app/models"
	"mailcare/app/modules"
	"mailcare/app/modules/agent"
)

// settingsDTO builds the settings payload. The server internals (worker
// count, listen address and port, data directory) are included for
// administrators only.
func (c *core) settingsDTO(u *models.User) map[string]any {
	providers := agent.Providers()
	if providers == nil {
		providers = []agent.ProviderStatus{}
	}
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
		"providers":              providers,
		// check_times, cleanup_time and notify_time are interpreted in this zone.
		"server_timezone": modules.ServerTimezone(),
	}
	if u != nil && u.Role == modules.RoleAdmin {
		dto["workers"] = c.jm.Workers()
		dto["web_listen"] = c.webListen
		dto["web_port"] = c.webPort
		dto["data_dir"] = c.dataDir
	}
	return dto
}

func (c *core) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, c.settingsDTO(userFrom(r)))
}

// handleUpdateSettings changes the check times, the agent provider, model
// and reasoning level (validated together; a provider change without a
// model or level clears them, and a level the model is known not to accept
// refuses the whole request), the agent switch, the retention of the agent run directories, the retention
// of fetched mails, the time of the daily cleanup and the worker count (applied to the job manager at
// once). Absent fields are left unchanged.
func (c *core) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CheckTimes    *[]string `json:"check_times"`
		AgentProvider *string   `json:"agent_provider"`
		AgentModel    *string   `json:"agent_model"`
		AgentEffort   *string   `json:"agent_reasoning_effort"`
		AgentEnabled  *bool     `json:"agent_enabled"`
		AgentKeepDays *int      `json:"agent_keep_days"`
		MailKeepDays  *int      `json:"mail_keep_days"`
		CleanupTime   *string   `json:"cleanup_time"`
		Workers       *int      `json:"workers"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Workers != nil && (*body.Workers < 1 || *body.Workers > modules.MaxWorkers) {
		writeJSON(w, http.StatusBadRequest, apiMessage{Key: "validation.common.numberOutOfRange", Params: map[string]any{"min": 1, "max": modules.MaxWorkers}})
		return
	}
	if body.AgentKeepDays != nil {
		if err := modules.ValidateAgentKeepDays(*body.AgentKeepDays); err != nil {
			writeErrorMessage(w, r, err)
			return
		}
	}
	if body.MailKeepDays != nil {
		if err := modules.ValidateMailKeepDays(*body.MailKeepDays); err != nil {
			writeErrorMessage(w, r, err)
			return
		}
	}
	if body.CleanupTime != nil {
		if _, err := modules.ParseCleanupTime(*body.CleanupTime); err != nil {
			writeErrorMessage(w, r, err)
			return
		}
	}
	var times []string
	if body.CheckTimes != nil {
		var err error
		if times, err = modules.ParseCheckTimes(*body.CheckTimes); err != nil {
			writeErrorMessage(w, r, err)
			return
		}
	}
	provider := modules.ResolveAgentProvider(c.db)
	if body.AgentProvider != nil {
		provider = strings.TrimSpace(*body.AgentProvider)
		if !agent.IsValidProvider(provider) {
			writeError(w, http.StatusBadRequest, "validation.agent.providerUnknown")
			return
		}
	}
	agentChanged := body.AgentProvider != nil || body.AgentModel != nil || body.AgentEffort != nil
	if agentChanged {
		model := modules.ResolveAgentModel(c.db)
		level := modules.ResolveAgentReasoningEffort(c.db)
		if body.AgentProvider != nil && provider != modules.ResolveAgentProvider(c.db) {
			model, level = "", ""
		}
		if body.AgentModel != nil {
			model = *body.AgentModel
		}
		if body.AgentEffort != nil {
			level = *body.AgentEffort
		}
		if err := modules.CheckAgentSettings(provider, model, level); err != nil {
			writeErrorMessage(w, r, err)
			return
		}
	}
	if body.CheckTimes != nil {
		if err := modules.SaveCheckTimes(c.db, times); err != nil {
			writeInternalError(w, "failed to save check times", err)
			return
		}
	}
	if agentChanged {
		var providerArg *string
		if body.AgentProvider != nil {
			providerArg = &provider
		}
		if err := modules.SaveAgentSettings(c.db, providerArg, body.AgentModel, body.AgentEffort); err != nil {
			writeInternalError(w, "failed to save the agent settings", err)
			return
		}
	}
	if body.AgentEnabled != nil {
		if err := modules.SetAgentEnabled(c.db, *body.AgentEnabled); err != nil {
			writeInternalError(w, "failed to save the agent switch", err)
			return
		}
	}
	if body.AgentKeepDays != nil {
		if err := modules.SaveAgentKeepDays(c.db, *body.AgentKeepDays); err != nil {
			writeInternalError(w, "failed to save the agent retention", err)
			return
		}
	}
	if body.MailKeepDays != nil {
		if err := modules.SaveMailKeepDays(c.db, *body.MailKeepDays); err != nil {
			writeInternalError(w, "failed to save the mail retention", err)
			return
		}
	}
	if body.CleanupTime != nil {
		if _, err := modules.SaveCleanupTime(c.db, *body.CleanupTime); err != nil {
			writeInternalError(w, "failed to save the cleanup time", err)
			return
		}
	}
	if body.Workers != nil {
		if err := modules.SaveWorkers(c.db, *body.Workers); err != nil {
			writeInternalError(w, "failed to save the worker count", err)
			return
		}
		c.jm.SetWorkers(*body.Workers)
	}
	writeJSON(w, http.StatusOK, c.settingsDTO(userFrom(r)))
}
