package agent

import (
	"mailcare/app/models"
)

// UsageReporter is an optional Provider extension: a provider whose CLI
// tells in its output which model and reasoning level it used, how many
// tokens the run consumed and which commands the agent ran implements it.
// raw is the full transcript, answer the transcript with the echoed prompt
// removed (StripPromptEcho). Whatever the output does not tell stays "" /
// NULL; a provider without it records nothing.
type UsageReporter interface {
	ParseUsage(raw, answer string) models.AgentRunUsage
}

// parseUsage runs the provider's UsageReporter when it has one.
func parseUsage(p Provider, raw, answer string) models.AgentRunUsage {
	if r, ok := p.(UsageReporter); ok {
		return r.ParseUsage(raw, answer)
	}
	return models.AgentRunUsage{}
}
