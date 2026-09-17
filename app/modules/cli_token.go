package modules

import (
	"database/sql"
	"fmt"

	"mailcare/app/models"
)

// --- token commands ---

// TokenCmd groups the login-token subcommands.
type TokenCmd struct {
	Create TokenCreateCmd `cmd:"" help:"Create a login token for a user"`
	List   TokenListCmd   `cmd:"" help:"List login tokens"`
	Show   TokenShowCmd   `cmd:"" help:"Show a login token"`
	Delete TokenDeleteCmd `cmd:"" help:"Delete a login token"`
}

// tokenRow renders a token for JSON output.
func tokenRow(t *models.Token, username string, withValue bool) map[string]interface{} {
	row := map[string]interface{}{
		"id": t.ID, "user_id": t.UserID, "username": username, "identifier": t.Identifier, "name": t.Name,
		"expires_at": rfc3339OrNull(t.ExpiresAt), "created_at": t.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		"last_used_at": rfc3339OrNull(t.LastUsedAt),
	}
	if withValue {
		row["token"] = t.Token
	}
	return row
}

// usernamesByID maps user ids to usernames.
func usernamesByID(db *sql.DB) (map[int64]string, error) {
	users, err := models.ListUsers(db)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]string, len(users))
	for _, u := range users {
		out[u.ID] = u.Username
	}
	return out, nil
}

// TokenCreateCmd creates a login token.
type TokenCreateCmd struct {
	User       string `help:"Username the token belongs to" required:""`
	Identifier string `help:"Identifier (A-Za-z0-9_-; derived from the name when omitted)"`
	Name       string `help:"Display name (default: the identifier or the username)"`
	Expires    string `help:"Expiry: a duration like 720h or an RFC3339 timestamp (omit = never)"`
	JSON       bool   `help:"Output as JSON"`
}

func (c *TokenCreateCmd) Run() error {
	db, err := openDBForCLI()
	if err != nil {
		return err
	}
	defer db.Close()
	u, err := findUser(db, c.User)
	if err != nil {
		return err
	}
	expiresAt, err := ParseExpiry(c.Expires)
	if err != nil {
		return NewExitError(ExitArgument, err.Error())
	}
	name := c.Name
	if name == "" {
		name = c.Identifier
	}
	if name == "" {
		name = u.Username
	}
	tok, err := CreateLoginToken(db, u.ID, name, c.Identifier, expiresAt)
	if err != nil {
		return NewExitError(ExitArgument, err.Error())
	}
	if c.JSON {
		printJSON(tokenRow(tok, u.Username, true))
		return nil
	}
	fmt.Printf("Created token %q (%s) for user %q\n", tok.Name, tok.Identifier, u.Username)
	fmt.Printf("  token:   %s\n", tok.Token)
	fmt.Printf("  expires: %s\n", formatExpiry(tok.ExpiresAt))
	fmt.Println("\nStore this token now; the full value is shown only here and via `token show`.")
	return nil
}

// TokenListCmd lists tokens.
type TokenListCmd struct {
	User string `help:"Only tokens of this user"`
	JSON bool   `help:"Output as JSON"`
}

func (c *TokenListCmd) Run() error {
	db, err := openDBForCLI()
	if err != nil {
		return err
	}
	defer db.Close()
	var tokens []*models.Token
	if c.User != "" {
		u, err := findUser(db, c.User)
		if err != nil {
			return err
		}
		tokens, err = models.ListTokensByUser(db, u.ID)
		if err != nil {
			return NewExitError(ExitGeneral, err.Error())
		}
	} else if tokens, err = models.ListTokens(db); err != nil {
		return NewExitError(ExitGeneral, err.Error())
	}
	names, err := usernamesByID(db)
	if err != nil {
		return NewExitError(ExitGeneral, err.Error())
	}
	if c.JSON {
		out := make([]map[string]interface{}, 0, len(tokens))
		for _, t := range tokens {
			out = append(out, tokenRow(t, names[t.UserID], false))
		}
		printJSON(out)
		return nil
	}
	if len(tokens) == 0 {
		fmt.Printf("No tokens. Create one with: %s token create --user <username>\n", AppName)
		return nil
	}
	fmt.Printf("%-20s %-16s %-24s %-20s %s\n", "IDENTIFIER", "USER", "NAME", "EXPIRES", "LAST USED")
	for _, t := range tokens {
		fmt.Printf("%-20s %-16s %-24s %-20s %s\n", t.Identifier, names[t.UserID], clip(t.Name, 24), formatExpiry(t.ExpiresAt), formatNullTime(t.LastUsedAt))
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
	names, err := usernamesByID(db)
	if err != nil {
		return NewExitError(ExitGeneral, err.Error())
	}
	if c.JSON {
		printJSON(tokenRow(t, names[t.UserID], true))
		return nil
	}
	fmt.Printf("identifier: %s\n", t.Identifier)
	fmt.Printf("user:       %s\n", names[t.UserID])
	fmt.Printf("name:       %s\n", t.Name)
	fmt.Printf("token:      %s\n", t.Token)
	fmt.Printf("expires:    %s\n", formatExpiry(t.ExpiresAt))
	fmt.Printf("created:    %s\n", t.CreatedAt.Local().Format(cliTimeFmt))
	fmt.Printf("last used:  %s\n", formatNullTime(t.LastUsedAt))
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
