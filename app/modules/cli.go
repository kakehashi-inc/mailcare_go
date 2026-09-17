package modules

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"golang.org/x/term"

	"mailcare/app/models"
)

// CLI command implementations. The kong command tree is declared in main.go;
// each command type here has a Run method. The commands are split by area:
//
//	cli.go          shared helpers
//	cli_service.go  service start / stop / status
//	cli_user.go     user ...
//	cli_token.go    token ...
//	cli_mailbox.go  mailbox ...
//	cli_jobs.go     sync / fetch / group / reindex / reclassify / analyze / notify / jobs
//	cli_groups.go   groups ADDRESS [KEY]
//	cli_settings.go schedule / settings

// cliTimeFmt is the local timestamp layout used across CLI output.
const cliTimeFmt = "2006-01-02 15:04:05"

// --- Output helpers ---

func printJSON(v interface{}) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to marshal JSON: %v\n", err)
		return
	}
	fmt.Println(string(data))
}

// formatNullTime renders a nullable time in local time ("-" when NULL).
func formatNullTime(t sql.NullTime) string {
	if !t.Valid {
		return "-"
	}
	return t.Time.Local().Format(cliTimeFmt)
}

// formatExpiry renders a token expiry ("never" when NULL).
func formatExpiry(t sql.NullTime) string {
	if !t.Valid {
		return "never"
	}
	return t.Time.Local().Format(cliTimeFmt)
}

// rfc3339OrNull renders a nullable time for JSON output.
func rfc3339OrNull(t sql.NullTime) interface{} {
	if !t.Valid {
		return nil
	}
	return t.Time.UTC().Format("2006-01-02T15:04:05Z07:00")
}

// clip collapses whitespace and truncates s to at most n runes.
func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "..."
	}
	return s
}

// --- Input helpers ---

// stdinReader is shared so that buffered lines survive across prompts
// (a fresh reader per call would swallow the following lines of piped input).
var stdinReader = bufio.NewReader(os.Stdin)

func readLine() (string, error) {
	line, err := stdinReader.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// confirm prints prompt and returns true only if the user answers "y".
func confirm(prompt string) bool {
	fmt.Print(prompt)
	line, err := readLine()
	if err != nil {
		return false
	}
	return strings.ToLower(strings.TrimSpace(line)) == "y"
}

// readSecret reads one secret line from stdin. On a terminal the input is
// not echoed; on piped input a plain line is read.
func readSecret(label string) (string, error) {
	fmt.Printf("%s: ", label)
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		data, err := term.ReadPassword(fd)
		fmt.Println()
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
	return readLine()
}

// promptPassword asks for a password twice (hidden on a terminal; pass
// --password to avoid the prompt in scripts).
func promptPassword(label string) (string, error) {
	first, err := readSecret(label)
	if err != nil {
		return "", NewExitError(ExitArgument, "no password given")
	}
	second, err := readSecret(label + " (again)")
	if err != nil {
		return "", NewExitError(ExitArgument, "no password given")
	}
	if first != second {
		return "", NewExitError(ExitArgument, "the passwords do not match")
	}
	return first, nil
}

// --- Data access helpers ---

// openDBForCLI opens the database (creating the data dir as needed).
func openDBForCLI() (*sql.DB, error) {
	if _, err := EnsureDataDir(); err != nil {
		return nil, NewExitErrorf(ExitFileIO, "failed to create data directory: %s", err)
	}
	db, err := OpenDB("")
	if err != nil {
		return nil, NewExitErrorf(ExitConfig, "failed to open database: %s", err)
	}
	return db, nil
}

// loadKeyForCLI loads the master key.
func loadKeyForCLI() ([]byte, error) {
	key, err := LoadSecretKey()
	if err != nil {
		return nil, NewExitErrorf(ExitConfig, "%s", err)
	}
	return key, nil
}

// dataRoots returns the mails and agent directories.
func dataRoots() (mailsRoot, agentRoot string, err error) {
	if mailsRoot, err = MailsDir(); err != nil {
		return "", "", NewExitErrorf(ExitFileIO, "%s", err)
	}
	if agentRoot, err = AgentDir(); err != nil {
		return "", "", NewExitErrorf(ExitFileIO, "%s", err)
	}
	return mailsRoot, agentRoot, nil
}

// newCLIJobManager builds a (not started) job manager for in-process runs.
func newCLIJobManager(db *sql.DB) (*JobManager, error) {
	key, err := loadKeyForCLI()
	if err != nil {
		return nil, err
	}
	mailsRoot, agentRoot, err := dataRoots()
	if err != nil {
		return nil, err
	}
	return NewJobManager(db, key, mailsRoot, agentRoot, TemplatesFS), nil
}

// findMailbox resolves a mailbox by address (case-insensitive).
func findMailbox(db *sql.DB, address string) (*models.Mailbox, error) {
	mb, err := models.GetMailboxByAddress(db, strings.ToLower(strings.TrimSpace(address)))
	if err == sql.ErrNoRows {
		return nil, NewExitErrorf(ExitArgument, "mailbox %q not found", address)
	}
	if err != nil {
		return nil, NewExitError(ExitGeneral, err.Error())
	}
	return mb, nil
}

// findUser resolves a user by username.
func findUser(db *sql.DB, username string) (*models.User, error) {
	u, err := models.GetUserByUsername(db, strings.TrimSpace(username))
	if err == sql.ErrNoRows {
		return nil, NewExitErrorf(ExitArgument, "user %q not found", username)
	}
	if err != nil {
		return nil, NewExitError(ExitGeneral, err.Error())
	}
	return u, nil
}

// signalContext returns a context cancelled by SIGINT / SIGTERM.
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
}
