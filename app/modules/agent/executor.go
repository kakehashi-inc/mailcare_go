package agent

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// runAgent runs the CLI of an analysis (runProvider); a variable so that
// the tests can see what it is given.
var runAgent = runProvider

// runProvider launches the provider CLI (with the model and the reasoning
// level when the provider supports them and they are set) with the
// workspace as its working directory and returns the combined
// stdout+stderr.
func runProvider(ctx context.Context, p Provider, model, reasoning, dir, promptText, promptFile string) (string, error) {
	return runArgv(ctx, p.Name(), providerCommand(p, model, reasoning), dir, promptText, promptFile)
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

	// The prompt and the output go through files rather than pipes: the
	// run then ends when the CLI process ends, even when a process it left
	// behind still holds them open (no wait for pipes to close is needed).
	// The files are made in the workspace of the run (dir), not in the
	// temporary directory of the OS, and removed when the run ends.
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	if useStdin {
		in, err := tempFileWith(dir, promptText)
		if err != nil {
			return "", fmt.Errorf("prepare the prompt for %s: %w", name, err)
		}
		defer removeTemp(in)
		cmd.Stdin = in
	}
	out, err := os.CreateTemp(dir, tempFilePattern+".out")
	if err != nil {
		return "", fmt.Errorf("prepare the output of %s: %w", name, err)
	}
	defer removeTemp(out)
	cmd.Stdout = out
	cmd.Stderr = out
	runErr := cmd.Run()
	text, err := os.ReadFile(out.Name())
	if err != nil && runErr == nil {
		runErr = err
	}
	return string(text), runErr
}

// tempFilePattern is the name of the temporary files of a run (the prompt
// fed on stdin, the output) in its workspace, without the extension.
const tempFilePattern = ".mailcare-agent-*"

// tempFileWith returns a temporary file in dir holding text, positioned at
// its start.
func tempFileWith(dir, text string) (*os.File, error) {
	f, err := os.CreateTemp(dir, tempFilePattern+".in")
	if err != nil {
		return nil, err
	}
	if _, err := f.WriteString(text); err != nil {
		removeTemp(f)
		return nil, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		removeTemp(f)
		return nil, err
	}
	return f, nil
}

// removeTemp closes and removes a temporary file.
func removeTemp(f *os.File) {
	f.Close()
	_ = os.Remove(f.Name())
}
