package agent

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

// DetectRateLimit (RateLimitDetector): codex exec prints its refusal as
// "ERROR: You've hit your usage limit. ... or try again at 7:22 PM." on
// stderr, which the generic markers recognize; the "ERROR:" prefix is not
// required because the same text also appears without it in some versions.
func (codexProvider) DetectRateLimit(out string) RateLimitOutcome {
	return DetectRateLimitMarkers(out)
}
