package workers

import (
	"net/http"
	"strings"

	"mailcare/app/modules"
	"mailcare/app/modules/agent"
)

// settingsDTO builds the settings payload.
func (c *core) settingsDTO() map[string]any {
	providers := agent.Providers()
	if providers == nil {
		providers = []agent.ProviderStatus{}
	}
	return map[string]any{
		"check_times":    modules.ResolveCheckTimes(c.db),
		"agent_provider": modules.ResolveAgentProvider(c.db),
		"agent_enabled":  modules.ResolveAgentEnabled(c.db),
		"providers":      providers,
		"web_listen":     c.webListen,
		"web_port":       c.webPort,
		"data_dir":       c.dataDir,
	}
}

func (c *core) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, c.settingsDTO())
}

// handleUpdateSettings changes the check times, the agent provider and the
// agent switch. Absent fields are left unchanged.
func (c *core) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CheckTimes    *[]string `json:"check_times"`
		AgentProvider *string   `json:"agent_provider"`
		AgentEnabled  *bool     `json:"agent_enabled"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	var times []string
	if body.CheckTimes != nil {
		if len(*body.CheckTimes) > 48 {
			writeError(w, http.StatusBadRequest, "too many check times")
			return
		}
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
	writeJSON(w, http.StatusOK, c.settingsDTO())
}
