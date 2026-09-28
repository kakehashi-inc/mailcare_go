package agent

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"mailcare/app/models"
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

// ModelOption / ModelArgs (ModelSelector): codex exec takes the model as
// --model <MODEL> (alias -m).
func (codexProvider) ModelOption() string { return "--model" }

func (codexProvider) ModelArgs(model string) []string {
	return []string{"--model", model}
}

// codexModel is one entry of the model cache codex keeps in $CODEX_HOME
// (default ~/.codex) for its own model picker.
type codexModel struct {
	Slug            string `json:"slug"`
	Visibility      string `json:"visibility"`
	Priority        int    `json:"priority"`
	ReasoningLevels []struct {
		Effort string `json:"effort"`
	} `json:"supported_reasoning_levels"`
}

// readCodexModels reads the model cache in priority order. The file is
// written by codex itself and its format is not a public contract, so any
// problem yields nil.
func readCodexModels() []codexModel {
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
		Models []codexModel `json:"models"`
	}
	if json.Unmarshal(data, &cache) != nil {
		return nil
	}
	sort.SliceStable(cache.Models, func(i, j int) bool { return cache.Models[i].Priority < cache.Models[j].Priority })
	return cache.Models
}

// Models lists the models codex offers to the signed-in account: the
// entries its model cache lists (visibility "list") in its priority order.
// Any problem with the cache yields nil and the settings just show no
// examples.
func (codexProvider) Models() []string {
	var out []string
	for _, m := range readCodexModels() {
		if m.Visibility == "list" && ValidateModel(m.Slug) == nil && m.Slug != "" {
			out = append(out, m.Slug)
		}
	}
	return out
}

// ReasoningOption / ReasoningArgs / ReasoningLevels (ReasoningSelector):
// codex exec has no dedicated option; the level is a configuration override,
// -c model_reasoning_effort="<level>" (the value is parsed as TOML, so it is
// passed as a quoted string). The levels every model accepts come from the
// same model cache as Models, hidden models included (the model setting is
// free text).
func (codexProvider) ReasoningOption() string { return "-c model_reasoning_effort" }

func (codexProvider) ReasoningArgs(level string) []string {
	return []string{"-c", `model_reasoning_effort="` + level + `"`}
}

func (codexProvider) ReasoningLevels() map[string][]string {
	models := readCodexModels()
	if len(models) == 0 {
		return nil
	}
	out := map[string][]string{}
	for _, m := range models {
		if m.Slug == "" || ValidateModel(m.Slug) != nil {
			continue
		}
		var levels []string
		for _, l := range m.ReasoningLevels {
			if ValidateReasoningEffort(l.Effort) == nil && l.Effort != "" {
				levels = append(levels, l.Effort)
			}
		}
		if len(levels) > 0 {
			out[m.Slug] = levels
		}
	}
	return out
}

// ParseUsage (UsageReporter): codex exec prints a header block between two
// "--------" lines before the echoed prompt ("model: gpt-...", "reasoning
// effort: xhigh", ...), a line "exec" before every command the agent runs,
// and "tokens used" followed by the total ("54,394") on the next line at the
// end (the number may also follow on the same line). The header is read
// from the raw transcript, the commands and the tokens from the answer (the
// transcript after the echoed prompt), so that nothing inside the prompt can
// count. Without the header the output is not a codex transcript and the
// command count stays NULL.
func (codexProvider) ParseUsage(raw, answer string) models.AgentRunUsage {
	var u models.AgentRunUsage
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	start := -1
	for i, l := range lines {
		if strings.TrimSpace(l) != "--------" {
			continue
		}
		if start < 0 {
			start = i
			continue
		}
		for _, h := range lines[start+1 : i] {
			key, value, ok := strings.Cut(h, ":")
			if !ok {
				continue
			}
			switch strings.ToLower(strings.TrimSpace(key)) {
			case "model":
				u.Model = strings.TrimSpace(value)
			case "reasoning effort":
				u.ReasoningEffort = strings.TrimSpace(value)
			}
		}
		break
	}
	answerLines := strings.Split(strings.ReplaceAll(answer, "\r\n", "\n"), "\n")
	if start >= 0 {
		commands := 0
		for _, l := range answerLines {
			if strings.TrimSpace(l) == "exec" {
				commands++
			}
		}
		u.CommandCount = sql.NullInt64{Int64: int64(commands), Valid: true}
	}
	for i, l := range answerLines {
		m := codexTokensRe.FindStringSubmatch(strings.TrimSpace(l))
		if m == nil {
			continue
		}
		number := m[1]
		if number == "" && i+1 < len(answerLines) {
			number = strings.TrimSpace(answerLines[i+1])
		}
		if n, err := strconv.ParseInt(strings.ReplaceAll(number, ",", ""), 10, 64); err == nil && n >= 0 {
			u.TokensUsed = sql.NullInt64{Int64: n, Valid: true}
		}
	}
	return u
}

// codexTokensRe matches the "tokens used" line, with the total on the same
// line or not.
var codexTokensRe = regexp.MustCompile(`(?i)^tokens used:?\s*([0-9][0-9,]*)?$`)

// DetectRateLimit (RateLimitDetector): codex exec prints its refusal as
// "ERROR: You've hit your usage limit. ... or try again at 7:22 PM." on
// stderr, which the generic markers recognize; the "ERROR:" prefix is not
// required because the same text also appears without it in some versions.
func (codexProvider) DetectRateLimit(out string) RateLimitOutcome {
	return DetectRateLimitMarkers(out)
}
