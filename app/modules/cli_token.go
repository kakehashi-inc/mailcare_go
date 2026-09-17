package modules

import (
	"database/sql"
	"fmt"

	"mailcare/app/models"
)

// --- token commands ---
//
// Tokens are reserved for the future API: they are independent of users and
// are not used for anything yet (the Web login is username + password). The
// server ensures a default token at start (EnsureDefaultToken); the first
// token created into an empty table becomes the default as well (CreateToken).

// TokenCmd groups the token subcommands.
type TokenCmd struct {
	Create TokenCreateCmd `cmd:"" help:"Create an API token"`
	List   TokenListCmd   `cmd:"" help:"List API tokens"`
	Show   TokenShowCmd   `cmd:"" help:"Show an API token including its value"`
	Delete TokenDeleteCmd `cmd:"" help:"Delete an API token"`
}

// tokenRow renders a token for JSON output.
func tokenRow(t *models.Token, withValue bool) map[string]interface{} {
	row := map[string]interface{}{
		"id": t.ID, "identifier": t.Identifier, "name": t.Name, "is_default": t.IsDefault,
		"expires_at": rfc3339OrNull(t.ExpiresAt), "created_at": t.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
	if withValue {
		row["token"] = t.Token
	}
	return row
}

// TokenCreateCmd creates a token.
type TokenCreateCmd struct {
	Identifier string `help:"Identifier (A-Za-z0-9_-)" required:""`
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
	// CreateToken fills only the id; re-read the row for created_at.
	if stored, err := models.GetTokenByIdentifier(db, tok.Identifier); err == nil {
		tok = stored
	}
	if c.JSON {
		printJSON(tokenRow(tok, true))
		return nil
	}
	fmt.Printf("Created token %q (%s)\n", tok.Name, tok.Identifier)
	fmt.Printf("  token:   %s\n", tok.Token)
	fmt.Printf("  default: %v\n", tok.IsDefault)
	fmt.Printf("  expires: %s\n", formatExpiry(tok.ExpiresAt))
	fmt.Println("\nStore this token now; the full value is shown in plain text only here and via `token show`.")
	return nil
}

// TokenListCmd lists tokens.
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
			out = append(out, tokenRow(t, false))
		}
		printJSON(out)
		return nil
	}
	if len(tokens) == 0 {
		fmt.Printf("No tokens. Create one with: %s token create --identifier <identifier>\n", AppName)
		return nil
	}
	fmt.Printf("%-20s %-24s %-8s %-20s %s\n", "IDENTIFIER", "NAME", "DEFAULT", "EXPIRES", "CREATED")
	for _, t := range tokens {
		def := ""
		if t.IsDefault {
			def = "yes"
		}
		fmt.Printf("%-20s %-24s %-8s %-20s %s\n", t.Identifier, clip(t.Name, 24), def, formatExpiry(t.ExpiresAt), t.CreatedAt.Local().Format(cliTimeFmt))
	}
	return nil
}

// TokenShowCmd shows a single token including its value.
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
		printJSON(tokenRow(t, true))
		return nil
	}
	fmt.Printf("identifier: %s\n", t.Identifier)
	fmt.Printf("name:       %s\n", t.Name)
	fmt.Printf("default:    %v\n", t.IsDefault)
	fmt.Printf("token:      %s\n", t.Token)
	fmt.Printf("expires:    %s\n", formatExpiry(t.ExpiresAt))
	fmt.Printf("created:    %s\n", t.CreatedAt.Local().Format(cliTimeFmt))
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
	if !c.Yes && !confirm(fmt.Sprintf("Delete token %q? [y/N]: ", c.Identifier)) {
		fmt.Println("Cancelled")
		return nil
	}
	if err := models.DeleteToken(db, c.Identifier); err != nil {
		return NewExitError(ExitGeneral, err.Error())
	}
	fmt.Printf("Deleted token %q\n", c.Identifier)
	return nil
}
