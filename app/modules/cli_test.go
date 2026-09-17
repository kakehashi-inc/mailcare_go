package modules

import (
	"bufio"
	"errors"
	"os"
	"testing"
)

// pipeStdin replaces os.Stdin (so readSecret sees no terminal) and the shared
// line reader with the given input for the rest of the test.
func pipeStdin(t *testing.T, input string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString(input); err != nil {
		t.Fatal(err)
	}
	w.Close()
	origStdin, origReader := os.Stdin, stdinReader
	os.Stdin, stdinReader = r, bufio.NewReader(r)
	t.Cleanup(func() {
		os.Stdin, stdinReader = origStdin, origReader
		r.Close()
	})
}

// TestPromptPasswordPipedInput checks the password prompt on piped input:
// two matching lines give the password, and a missing first line, a missing
// confirmation line and a mismatch are separate argument errors.
func TestPromptPasswordPipedInput(t *testing.T) {
	for _, tc := range []struct{ input, want, message string }{
		{"secret\nsecret\n", "secret", ""},
		{"secret\nsecret", "secret", ""},
		{"", "", "no password given"},
		{"secret\n", "", "the password must be entered twice (the confirmation line is missing)"},
		{"secret\nother\n", "", "the passwords do not match"},
	} {
		pipeStdin(t, tc.input)
		var got string
		var err error
		captureStdout(t, func() { got, err = promptPassword("Password") })
		if tc.message == "" {
			if err != nil || got != tc.want {
				t.Errorf("input %q: %q, %v; want %q", tc.input, got, err, tc.want)
			}
			continue
		}
		var exitErr *ExitError
		if !errors.As(err, &exitErr) || exitErr.Code != ExitArgument || exitErr.Message != tc.message {
			t.Errorf("input %q: %v; want argument error %q", tc.input, err, tc.message)
		}
	}
	// The lines are read from the shared reader, so the prompt does not
	// swallow the input that follows the password.
	pipeStdin(t, "secret\nsecret\ny\n")
	captureStdout(t, func() {
		if got, err := promptPassword("Password"); err != nil || got != "secret" {
			t.Errorf("password before the confirmation: %q, %v", got, err)
		}
		if !confirm("Continue? ") {
			t.Error("the line after the password was lost")
		}
	})
}
