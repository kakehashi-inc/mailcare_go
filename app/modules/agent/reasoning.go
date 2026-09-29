package agent

import (
	"fmt"
	"mailcare/app/modules/wording"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Reasoning level selection.
//
// ReasoningSelector is an optional Provider extension: a provider whose CLI
// can be told how much to reason implements it, and the configured level
// (setting agent_reasoning_effort, empty = the CLI's own setting) is
// appended to the launch argv through ReasoningArgs. A provider without it
// never gets a level and the setting must stay empty for it.

// MaxReasoningEffortLength bounds the length of a reasoning level.
const MaxReasoningEffortLength = 20

// reasoningRe is the accepted shape of a reasoning level: lower-case letters
// only, so the value can neither be taken for an option of the CLI nor add
// anything to the configuration value it is passed in.
var reasoningRe = regexp.MustCompile(`^[a-z]+$`)

// ReasoningSelector is implemented by providers whose CLI accepts a
// reasoning level.
type ReasoningSelector interface {
	// ReasoningOption is the option of the CLI that carries the level,
	// shown in the settings as a hint.
	ReasoningOption() string
	// ReasoningArgs are the arguments that set the level, appended to the
	// launch argv after the model arguments. level is never empty.
	ReasoningArgs(level string) []string
	// ReasoningLevels maps each model the CLI knows to the levels it
	// accepts, in the order the CLI lists them (nil when unknown). It must
	// be cheap and must not fail.
	ReasoningLevels() map[string][]string
}

// ValidateReasoningEffort checks the shape of a reasoning level ("" = the
// CLI's own setting, always valid).
func ValidateReasoningEffort(level string) error {
	if level == "" {
		return nil
	}
	if len(level) > MaxReasoningEffortLength || !reasoningRe.MatchString(level) {
		return wording.New("validation.agent.reasoningInvalid", fmt.Sprintf("agent reasoning effort must be at most %d lower-case letters", MaxReasoningEffortLength))
	}
	return nil
}

// CheckReasoningEffort validates a reasoning level for a provider and model
// before it is saved: the shape (ValidateReasoningEffort), that the provider
// accepts a level at all, and that the level is one the model accepts. The
// accepted levels come from ReasoningLevels: for a named model its own list,
// for the CLI default model (model "") the levels of every known model.
// When the levels are unknown (the provider cannot tell, or the model is not
// listed) only the shape is checked.
func CheckReasoningEffort(provider, model, level string) error {
	level = strings.TrimSpace(level)
	if err := ValidateReasoningEffort(level); err != nil {
		return err
	}
	if level == "" {
		return nil
	}
	p, ok := lookupProvider(provider)
	if !ok {
		return wording.New("validation.agent.providerUnknown", fmt.Sprintf("unknown agent provider %q", provider))
	}
	s, ok := p.(ReasoningSelector)
	if !ok {
		return wording.New("validation.agent.reasoningUnsupported", fmt.Sprintf("agent provider %q does not accept a reasoning effort", p.Name()))
	}
	known := s.ReasoningLevels()
	var accepted []string
	model = strings.TrimSpace(model)
	if model != "" {
		levels, listed := known[model]
		if !listed {
			return nil
		}
		accepted = levels
	} else {
		accepted = unionLevels(known)
	}
	if len(accepted) == 0 || slices.Contains(accepted, level) {
		return nil
	}
	if model == "" {
		return wording.New("validation.agent.reasoningInvalid",
			fmt.Sprintf("agent reasoning effort %q is not accepted by any known model (accepted: %s)", level, strings.Join(accepted, ", ")))
	}
	return wording.New("validation.agent.reasoningInvalid",
		fmt.Sprintf("agent reasoning effort %q is not accepted by model %q (accepted: %s)", level, model, strings.Join(accepted, ", ")))
}

// unionLevels returns every level any model accepts, each once, in the
// order of first appearance over the models sorted by name.
func unionLevels(known map[string][]string) []string {
	names := make([]string, 0, len(known))
	for name := range known {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []string
	for _, name := range names {
		for _, l := range known[name] {
			if !slices.Contains(out, l) {
				out = append(out, l)
			}
		}
	}
	return out
}

// reasoningInfo returns the reasoning option and the known levels of a
// provider ("" and nil when it does not support a reasoning level).
func reasoningInfo(p Provider) (option string, levels map[string][]string) {
	s, ok := p.(ReasoningSelector)
	if !ok {
		return "", nil
	}
	return s.ReasoningOption(), s.ReasoningLevels()
}

// ProviderSupportsReasoning reports whether the named provider accepts a
// reasoning level.
func ProviderSupportsReasoning(name string) bool {
	p, ok := lookupProvider(name)
	if !ok {
		return false
	}
	_, ok = p.(ReasoningSelector)
	return ok
}
