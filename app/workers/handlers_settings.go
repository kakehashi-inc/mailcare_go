package workers

import (
	"fmt"
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
		"check_times":     modules.ResolveCheckTimes(c.db),
		"agent_provider":  modules.ResolveAgentProvider(c.db),
		"agent_enabled":   modules.ResolveAgentEnabled(c.db),
		"agent_keep_days": modules.ResolveAgentKeepDays(c.db),
		"mail_keep_days":  modules.ResolveMailKeepDays(c.db),
		"providers":       providers,
		// check_times and notify_time are interpreted in this zone.
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

// handleUpdateSettings changes the check times, the agent provider, the
// agent switch, the retention of the agent run directories, the retention
// of fetched mails and the worker count (applied to the job manager at
// once). Absent fields are left unchanged.
func (c *core) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CheckTimes    *[]string `json:"check_times"`
		AgentProvider *string   `json:"agent_provider"`
		AgentEnabled  *bool     `json:"agent_enabled"`
		AgentKeepDays *int      `json:"agent_keep_days"`
		MailKeepDays  *int      `json:"mail_keep_days"`
		Workers       *int      `json:"workers"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Workers != nil && (*body.Workers < 1 || *body.Workers > modules.MaxWorkers) {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("workers must be between 1 and %d", modules.MaxWorkers))
		return
	}
	if body.AgentKeepDays != nil {
		if err := modules.ValidateAgentKeepDays(*body.AgentKeepDays); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if body.MailKeepDays != nil {
		if err := modules.ValidateMailKeepDays(*body.MailKeepDays); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	var times []string
	if body.CheckTimes != nil {
		var err error
		if times, err = modules.ParseCheckTimes(*body.CheckTimes); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	provider := ""
	if body.AgentProvider != nil {
		provider = strings.TrimSpace(*body.AgentProvider)
		if !agent.IsValidProvider(provider) {
			writeError(w, http.StatusBadRequest, "unknown agent provider")
			return
		}
	}
	if body.CheckTimes != nil {
		if err := modules.SaveCheckTimes(c.db, times); err != nil {
			writeInternalError(w, "failed to save check times", err)
			return
		}
	}
	if body.AgentProvider != nil {
		if err := modules.SetAgentProvider(c.db, provider); err != nil {
			writeInternalError(w, "failed to save the agent provider", err)
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
	if body.Workers != nil {
		if err := modules.SaveWorkers(c.db, *body.Workers); err != nil {
			writeInternalError(w, "failed to save the worker count", err)
			return
		}
		c.jm.SetWorkers(*body.Workers)
	}
	writeJSON(w, http.StatusOK, c.settingsDTO(userFrom(r)))
}
