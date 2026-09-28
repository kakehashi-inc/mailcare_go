package agent

import (
	"fmt"
	"regexp"
	"strings"
)

// Model selection.
//
// ModelSelector is an optional Provider extension: a provider whose CLI can
// be told which model to use implements it, and the configured model
// (setting agent_model, empty = the CLI's own default) is passed through
// CommandWithModel. A provider without it always runs Command() and the
// setting is ignored for it.

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
	// CommandWithModel is Command() with the model added where the CLI
	// expects it. model is never empty.
	CommandWithModel(model string) []string
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
		return fmt.Errorf("agent model must be at most %d characters of letters, digits and . _ : / @ + - (starting with a letter or digit)", MaxModelLength)
	}
	return nil
}

// providerCommand returns the launch argv of a provider for a model: the
// provider's CommandWithModel when it supports models and one is set, else
// Command().
func providerCommand(p Provider, model string) []string {
	model = strings.TrimSpace(model)
	if s, ok := p.(ModelSelector); ok && model != "" {
		return s.CommandWithModel(model)
	}
	return p.Command()
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
