package agent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeFakeCLI creates an executable script in dir that writes its stdin to
// received_prompt.txt in the working directory and then prints the canned
// output. It returns the argv to launch it.
func writeFakeCLI(t *testing.T, dir, canned string) []string {
	t.Helper()
	cannedPath := filepath.Join(dir, "canned.txt")
	if err := os.WriteFile(cannedPath, []byte(canned), 0o600); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		script := filepath.Join(dir, "fake.cmd")
		body := "@echo off\r\nmore > received_prompt.txt\r\ntype \"" + cannedPath + "\"\r\n"
		if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
		return []string{script}
	}
	script := filepath.Join(dir, "fake.sh")
	body := "#!/bin/sh\ncat > received_prompt.txt\ncat \"" + cannedPath + "\"\nexit 3\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return []string{script}
}

func TestRunArgvStdinAndCwd(t *testing.T) {
	bin := t.TempDir()
	work := t.TempDir()
	argv := writeFakeCLI(t, bin, "hello from fake\n")
	out, err := runArgv(context.Background(), "fake", argv, work, "the prompt", filepath.Join(work, PromptFileName))
	if runtime.GOOS != "windows" && err == nil {
		t.Fatal("expected the non-zero exit to be reported as an error")
	}
	if !strings.Contains(out, "hello from fake") {
		t.Fatalf("combined output missing: %q", out)
	}
	got, readErr := os.ReadFile(filepath.Join(work, "received_prompt.txt"))
	if readErr != nil {
		t.Fatalf("script did not run in the workspace: %v", readErr)
	}
	if strings.TrimSpace(string(got)) != "the prompt" {
		t.Fatalf("prompt was not fed on stdin: %q", got)
	}
}

func TestRunArgvPlaceholders(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /bin/sh")
	}
	work := t.TempDir()
	argv := []string{"/bin/sh", "-c", `printf '%s|%s|%s' "$1" "$2" "$3"`, "sh", "{prompt}", "{prompt_file}", "{cwd}"}
	out, err := runArgv(context.Background(), "sh", argv, work, "multi\nline", "/tmp/PROMPT.md")
	if err != nil {
		t.Fatal(err)
	}
	if out != "multi\nline|/tmp/PROMPT.md|"+work {
		t.Fatalf("placeholders not substituted: %q", out)
	}
}

func TestRunArgvMissingBinary(t *testing.T) {
	_, err := runArgv(context.Background(), "ghost", []string{"definitely-not-a-real-binary-mlc"}, t.TempDir(), "p", "")
	if err == nil || !strings.Contains(err.Error(), "not found on PATH") {
		t.Fatalf("expected a launch error, got %v", err)
	}
	if _, err := runArgv(context.Background(), "empty", nil, t.TempDir(), "p", ""); err == nil {
		t.Fatal("empty argv must fail")
	}
}
