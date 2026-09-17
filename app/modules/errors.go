package modules

import "fmt"

// Exit codes returned by the CLI.
const (
	ExitSuccess  = 0 // normal termination
	ExitGeneral  = 1 // unspecified error
	ExitArgument = 2 // invalid arguments or flags
	ExitConfig   = 3 // configuration or database error
	ExitAuth     = 4 // authentication or authorization failure
	ExitExec     = 5 // external command / server execution error
	ExitFileIO   = 6 // file or directory I/O error
	ExitTimeout  = 7 // operation timed out
)

// ExitError wraps an exit code for CLI commands.
type ExitError struct {
	Code    int
	Message string
}

func (e *ExitError) Error() string { return e.Message }

// NewExitError creates an ExitError with the given code and message.
func NewExitError(code int, msg string) *ExitError {
	return &ExitError{Code: code, Message: msg}
}

// NewExitErrorf creates an ExitError with a formatted message.
func NewExitErrorf(code int, format string, args ...interface{}) *ExitError {
	return &ExitError{Code: code, Message: fmt.Sprintf(format, args...)}
}
