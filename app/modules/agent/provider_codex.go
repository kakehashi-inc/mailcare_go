package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
)

// codex (OpenAI Codex CLI). Everything specific to this provider lives in
// this file; the shared pipeline (run.go / executor.go) never mentions it.

func init() {
	registerProvider(codexProvider{})
}

type codexProvider struct{}

func (codexProvider) Name() string  { return "codex" }
func (codexProvider) Label() string { return "Codex" }

// Command: codex exec runs non-interactively in a read-only sandbox, which is
// enough because the agent only reads the listed mail files (outside the
// workspace) and writes nothing. The workspace is a throwaway non-git
// directory, which codex exec rejects unless --skip-git-repo-check is set.
// There is no {prompt}/{prompt_file} placeholder, so the prompt is fed on
// stdin.
func (codexProvider) Command() []string {
	return []string{"codex", "exec", "--sandbox", "read-only", "--skip-git-repo-check"}
}

// ModelOption / CommandWithModel (ModelSelector): codex exec takes the model
// as --model <MODEL> (alias -m).
func (codexProvider) ModelOption() string { return "--model" }

func (p codexProvider) CommandWithModel(model string) []string {
	return append(p.Command(), "--model", model)
}

// Models lists the models codex offers to the signed-in account, read from
// the cache codex keeps in $CODEX_HOME (default ~/.codex) for its own model
// picker: the entries it lists (visibility "list") in its priority order.
// The file is written by codex itself and its format is not a public
// contract, so any problem yields nil and the settings just show no
// examples.
func (codexProvider) Models() []string {
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		dir, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		home = filepath.Join(dir, ".codex")
	}
	data, err := os.ReadFile(filepath.Join(home, "models_cache.json"))
	if err != nil {
		return nil
	}
	var cache struct {
		Models []struct {
			Slug       string `json:"slug"`
			Visibility string `json:"visibility"`
			Priority   int    `json:"priority"`
		} `json:"models"`
	}
	if json.Unmarshal(data, &cache) != nil {
		return nil
	}
	sort.SliceStable(cache.Models, func(i, j int) bool { return cache.Models[i].Priority < cache.Models[j].Priority })
	var out []string
	for _, m := range cache.Models {
		if m.Visibility == "list" && ValidateModel(m.Slug) == nil && m.Slug != "" {
			out = append(out, m.Slug)
		}
	}
	return out
}

// DetectRateLimit (RateLimitDetector): codex exec prints its refusal as
// "ERROR: You've hit your usage limit. ... or try again at 7:22 PM." on
// stderr, which the generic markers recognize; the "ERROR:" prefix is not
// required because the same text also appears without it in some versions.
func (codexProvider) DetectRateLimit(out string) RateLimitOutcome {
	return DetectRateLimitMarkers(out)
}
