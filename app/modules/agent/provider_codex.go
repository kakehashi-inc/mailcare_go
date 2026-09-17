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
