package agent

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// waitDelay bounds how long a run waits for the CLI's output pipes to close
// after the process itself has ended (or been killed on context cancel). A
// child process left behind by the CLI would otherwise keep Wait blocked.
const waitDelay = 5 * time.Second

// runProvider launches the provider CLI with the workspace as its working
// directory and returns the combined stdout+stderr.
func runProvider(ctx context.Context, p Provider, dir, promptText, promptFile string) (string, error) {
	return runArgv(ctx, p.Name(), p.Command(), dir, promptText, promptFile)
}

// runArgv launches command (after placeholder substitution) directly, without
// a shell on any OS. Prompt delivery: the prompt is fed on stdin unless the
// argv uses {prompt} (inlined as one argv element, so multi-line prompts are
// safe) or {prompt_file} (path of PROMPT.md). {cwd} expands to dir.
func runArgv(ctx context.Context, name string, command []string, dir, promptText, promptFile string) (string, error) {
	argv := append([]string(nil), command...)
	if len(argv) == 0 {
		return "", fmt.Errorf("empty command for provider %s", name)
	}
	useStdin := true
	for i, a := range argv {
		if strings.Contains(a, "{prompt_file}") {
			argv[i] = strings.ReplaceAll(argv[i], "{prompt_file}", promptFile)
			useStdin = false
		}
		if strings.Contains(argv[i], "{prompt}") {
			argv[i] = strings.ReplaceAll(argv[i], "{prompt}", promptText)
			useStdin = false
		}
		if strings.Contains(argv[i], "{cwd}") {
			argv[i] = strings.ReplaceAll(argv[i], "{cwd}", dir)
		}
	}

	if _, err := exec.LookPath(argv[0]); err != nil {
		return "", fmt.Errorf("%q not found on PATH (is %s installed?)", argv[0], name)
	}

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.WaitDelay = waitDelay
	if useStdin {
		cmd.Stdin = strings.NewReader(promptText)
	}
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}
