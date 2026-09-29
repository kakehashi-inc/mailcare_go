package agent

import (
	"fmt"
	"mailcare/app/modules/wording"
	"regexp"
	"strings"
)

// Model selection.
//
// ModelSelector is an optional Provider extension: a provider whose CLI can
// be told which model to use implements it, and the configured model
// (setting agent_model, empty = the CLI's own default) is appended to
// Command() through ModelArgs. A provider without it always runs Command()
// and the setting is ignored for it.

// MaxModelLength bounds the length of a model name.
const MaxModelLength = 100

// modelRe is the accepted shape of a model name: it starts with a letter or
// digit (so it can never be taken for an option of the CLI) and contains no
// white space.
var modelRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+-]*$`)

// ModelSelector is implemented by providers whose CLI accepts a model.
type ModelSelector interface {
	// ModelOption is the option of the CLI that names the model ("--model"),
	// shown in the settings as a hint.
	ModelOption() string
	// ModelArgs are the arguments that select the model, appended to
	// Command(). model is never empty.
	ModelArgs(model string) []string
	// Models lists model names the CLI accepts, for reference in the
	// settings (nil when unknown). It must be cheap and must not fail.
	Models() []string
}

// ValidateModel checks a model name ("" = the CLI default, always valid).
func ValidateModel(model string) error {
	if model == "" {
		return nil
	}
	if len(model) > MaxModelLength || !modelRe.MatchString(model) {
		return wording.New("validation.agent.modelInvalid",
			fmt.Sprintf("agent model must be at most %d characters of letters, digits and . _ : / @ + - (starting with a letter or digit)", MaxModelLength)).
			With("max", MaxModelLength)
	}
	return nil
}

// providerCommand returns the launch argv of a provider: Command(), then
// the model arguments when the provider supports models and one is set,
// then the reasoning arguments when the provider supports a reasoning
// level and one is set.
func providerCommand(p Provider, model, reasoning string) []string {
	argv := append([]string(nil), p.Command()...)
	model = strings.TrimSpace(model)
	if s, ok := p.(ModelSelector); ok && model != "" {
		argv = append(argv, s.ModelArgs(model)...)
	}
	reasoning = strings.TrimSpace(reasoning)
	if s, ok := p.(ReasoningSelector); ok && reasoning != "" {
		argv = append(argv, s.ReasoningArgs(reasoning)...)
	}
	return argv
}

// modelInfo returns the model option and reference models of a provider
// ("" and nil when it does not support models).
func modelInfo(p Provider) (option string, models []string) {
	s, ok := p.(ModelSelector)
	if !ok {
		return "", nil
	}
	return s.ModelOption(), s.Models()
}

// ProviderSupportsModel reports whether the named provider accepts a model.
func ProviderSupportsModel(name string) bool {
	p, ok := lookupProvider(name)
	if !ok {
		return false
	}
	_, ok = p.(ModelSelector)
	return ok
}
