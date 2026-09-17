package modules

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"develop_app/app/models"
)

// --- Output helpers ---

func printJSON(v interface{}) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to marshal JSON: %v\n", err)
		return
	}
	fmt.Println(string(data))
}

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

func maskToken(t string) string {
	if len(t) <= 12 {
		return t
	}
	return t[:12] + "..."
}

func formatExpiry(ns sql.NullTime) string {
	if !ns.Valid {
		return "never"
	}
	return ns.Time.Local().Format("2006-01-02 15:04:05")
}

// --- token commands ---

// TokenCmd groups bearer-token management subcommands.
type TokenCmd struct {
	Create TokenCreateCmd `cmd:"" help:"Create a token"`
	List   TokenListCmd   `cmd:"" help:"List tokens"`
	Show   TokenShowCmd   `cmd:"" help:"Show a single token"`
	Delete TokenDeleteCmd `cmd:"" help:"Delete a token"`
}

// TokenCreateCmd creates a new token.
type TokenCreateCmd struct {
	Identifier string `name:"identifier" help:"Identifier (A-Za-z0-9_-), required" required:""`
	Name       string `help:"Display name (default: the identifier)"`
	Expires    string `help:"Expiry: a duration like 720h or an RFC3339 timestamp (omit = never)"`
	JSON       bool   `help:"Output as JSON"`
}

func (c *TokenCreateCmd) Run() error {
	db, err := openDBForCLI()
	if err != nil {
		return err
	}
	defer db.Close()

	expiresAt, err := ParseExpiry(c.Expires)
	if err != nil {
		return NewExitError(ExitArgument, err.Error())
	}
	name := c.Name
	if name == "" {
		name = c.Identifier
	}
	tok, err := CreateToken(db, name, c.Identifier, expiresAt, false)
	if err != nil {
		return NewExitError(ExitArgument, err.Error())
	}
	if c.JSON {
		printJSON(map[string]interface{}{
			"identifier": tok.Identifier, "name": tok.Name, "token": tok.Token,
			"is_default": tok.IsDefault, "expires_at": formatExpiry(tok.ExpiresAt),
		})
		return nil
	}
	fmt.Printf("Created token %q (%s)\n", tok.Name, tok.Identifier)
	fmt.Printf("  token:   %s\n", tok.Token)
	fmt.Printf("  default: %v\n", tok.IsDefault)
	fmt.Printf("  expires: %s\n", formatExpiry(tok.ExpiresAt))
	fmt.Println("\nStore this token now; the full value is shown in plain text only here and via `token show`.")
	return nil
}

// TokenListCmd lists all tokens.
type TokenListCmd struct {
	JSON bool `help:"Output as JSON"`
}

func (c *TokenListCmd) Run() error {
	db, err := openDBForCLI()
	if err != nil {
		return err
	}
	defer db.Close()

	tokens, err := models.ListTokens(db)
	if err != nil {
		return NewExitError(ExitGeneral, err.Error())
	}
	if c.JSON {
		out := make([]map[string]interface{}, 0, len(tokens))
		for _, t := range tokens {
			out = append(out, map[string]interface{}{
				"identifier": t.Identifier, "name": t.Name, "is_default": t.IsDefault,
				"token_masked": maskToken(t.Token), "expires_at": formatExpiry(t.ExpiresAt),
				"created_at": t.CreatedAt.Local().Format("2006-01-02 15:04:05"),
			})
		}
		printJSON(out)
		return nil
	}
	if len(tokens) == 0 {
		fmt.Printf("No tokens. Create one with: %s token create --identifier <identifier>\n", AppName)
		return nil
	}
	fmt.Printf("%-3s %-20s %-24s %-16s %s\n", "DEF", "IDENTIFIER", "NAME", "TOKEN", "EXPIRES")
	for _, t := range tokens {
		def := " "
		if t.IsDefault {
			def = "*"
		}
		fmt.Printf("%-3s %-20s %-24s %-16s %s\n", def, t.Identifier, t.Name, maskToken(t.Token), formatExpiry(t.ExpiresAt))
	}
	return nil
}

// TokenShowCmd shows a single token (full value).
type TokenShowCmd struct {
	Identifier string `arg:"" help:"Token identifier"`
	JSON       bool   `help:"Output as JSON"`
}

func (c *TokenShowCmd) Run() error {
	db, err := openDBForCLI()
	if err != nil {
		return err
	}
	defer db.Close()

	t, err := models.GetTokenByIdentifier(db, c.Identifier)
	if err == sql.ErrNoRows {
		return NewExitErrorf(ExitArgument, "token %q not found", c.Identifier)
	}
	if err != nil {
		return NewExitError(ExitGeneral, err.Error())
	}
	if c.JSON {
		printJSON(map[string]interface{}{
			"identifier": t.Identifier, "name": t.Name, "token": t.Token,
			"is_default": t.IsDefault, "expires_at": formatExpiry(t.ExpiresAt),
			"created_at": t.CreatedAt.Local().Format("2006-01-02 15:04:05"),
		})
		return nil
	}
	fmt.Printf("identifier: %s\n", t.Identifier)
	fmt.Printf("name:       %s\n", t.Name)
	fmt.Printf("token:      %s\n", t.Token)
	fmt.Printf("default:    %v\n", t.IsDefault)
	fmt.Printf("expires:    %s\n", formatExpiry(t.ExpiresAt))
	fmt.Printf("created:    %s\n", t.CreatedAt.Local().Format("2006-01-02 15:04:05"))
	return nil
}

// TokenDeleteCmd deletes a token.
type TokenDeleteCmd struct {
	Identifier string `arg:"" help:"Token identifier"`
	Yes        bool   `short:"y" help:"Skip confirmation"`
}

func (c *TokenDeleteCmd) Run() error {
	db, err := openDBForCLI()
	if err != nil {
		return err
	}
	defer db.Close()

	if _, err := models.GetTokenByIdentifier(db, c.Identifier); err == sql.ErrNoRows {
		return NewExitErrorf(ExitArgument, "token %q not found", c.Identifier)
	} else if err != nil {
		return NewExitError(ExitGeneral, err.Error())
	}

	if !c.Yes {
		fmt.Printf("Delete token %q? [y/N]: ", c.Identifier)
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Scan()
		if strings.ToLower(strings.TrimSpace(scanner.Text())) != "y" {
			fmt.Println("Cancelled")
			return nil
		}
	}

	if err := models.DeleteToken(db, c.Identifier); err != nil {
		return NewExitError(ExitGeneral, err.Error())
	}
	fmt.Printf("Deleted token %q\n", c.Identifier)
	return nil
}

// --- service commands ---

// ServiceCmd manages the HTTP server (API + Web).
type ServiceCmd struct {
	Start  ServiceStartCmd  `cmd:"" help:"Start the API + Web server"`
	Stop   ServiceStopCmd   `cmd:"" help:"Stop the server"`
	Status ServiceStatusCmd `cmd:"" help:"Show server status"`
}

// ServiceStartCmd starts the server. The API server (--api-port) and the Web
// server (--web-port) are separate listeners. Zero values mean "not specified"
// so the server can fall back to the saved setting and then the code default.
type ServiceStartCmd struct {
	ApiListen string `help:"API listen address (default: saved setting or ${default_api_listen})" name:"api-listen" default:""`
	WebListen string `help:"Web UI listen address (default: saved setting or ${default_web_listen})" name:"web-listen" default:""`
	ApiPort   int    `help:"API listen port (default: saved setting or ${default_api_port})" name:"api-port" default:"0"`
	WebPort   int    `help:"Web UI listen port (default: saved setting or ${default_web_port})" name:"web-port" default:"0"`
}

func (c *ServiceStartCmd) Run() error {
	if StartServer == nil {
		return NewExitError(ExitGeneral, "server not linked")
	}
	if err := StartServer(c.ApiListen, c.WebListen, c.ApiPort, c.WebPort); err != nil {
		return NewExitError(ExitExec, err.Error())
	}
	return nil
}

// ServiceStopCmd stops the server (connects to the API control port).
type ServiceStopCmd struct {
	ApiPort int `help:"Target API port" name:"api-port" default:"${default_api_port}"`
}

func (c *ServiceStopCmd) Run() error {
	if err := StopServer(c.ApiPort); err != nil {
		return NewExitError(ExitExec, err.Error())
	}
	return nil
}

// ServiceStatusCmd shows server status (connects to the API control port).
type ServiceStatusCmd struct {
	ApiPort int `help:"Target API port" name:"api-port" default:"${default_api_port}"`
}

func (c *ServiceStatusCmd) Run() error {
	if err := ShowServerStatus(c.ApiPort); err != nil {
		return NewExitError(ExitExec, err.Error())
	}
	return nil
}
