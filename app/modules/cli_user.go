package modules

import (
	"fmt"

	"mailcare/app/models"
)

// --- user commands ---

// UserCmd groups the user management subcommands.
type UserCmd struct {
	Create UserCreateCmd `cmd:"" help:"Create a user"`
	List   UserListCmd   `cmd:"" help:"List users"`
	Delete UserDeleteCmd `cmd:"" help:"Delete a user"`
	Passwd UserPasswdCmd `cmd:"" help:"Change the password of a user"`
	Role   UserRoleCmd   `cmd:"" help:"Change the role of a user"`
	Update UserUpdateCmd `cmd:"" help:"Change the profile of a user (display name, email, language, timezone, theme)"`
}

// UserCreateCmd creates a user.
type UserCreateCmd struct {
	Username    string `help:"Username ([A-Za-z0-9_.-], 1-64 characters)" required:""`
	DisplayName string `help:"Display name (default: the username)" name:"display-name"`
	Email       string `help:"Notification mail address (optional)"`
	Language    string `help:"UI language: ja or en (default: ${default_language})"`
	Timezone    string `help:"Display timezone, IANA name (default: ${default_timezone})"`
	Theme       string `help:"UI theme: auto, light or dark (default: ${default_theme})"`
	Role        string `help:"Role" enum:"admin,user" default:"user"`
	Password    string `help:"Password (prompted when omitted)"`
}

func (c *UserCreateCmd) Run() error {
	db, err := openDBForCLI()
	if err != nil {
		return err
	}
	defer db.Close()
	password := c.Password
	if password == "" {
		if password, err = promptPassword("Password"); err != nil {
			return err
		}
	}
	u, err := CreateUserFrom(db, NewUser{Username: c.Username, DisplayName: c.DisplayName, Email: c.Email, Language: c.Language,
		Timezone: c.Timezone, Theme: c.Theme, Password: password, Role: c.Role})
	if err != nil {
		return NewExitError(ExitArgument, err.Error())
	}
	if u.Email != "" {
		fmt.Printf("Created user %q (%s, id %d, email %s)\n", u.Username, u.Role, u.ID, u.Email)
	} else {
		fmt.Printf("Created user %q (%s, id %d)\n", u.Username, u.Role, u.ID)
	}
	return nil
}

// UserListCmd lists users.
type UserListCmd struct {
	JSON bool `help:"Output as JSON"`
}

func (c *UserListCmd) Run() error {
	db, err := openDBForCLI()
	if err != nil {
		return err
	}
	defer db.Close()
	users, err := models.ListUsers(db)
	if err != nil {
		return NewExitError(ExitGeneral, err.Error())
	}
	if c.JSON {
		out := make([]map[string]interface{}, 0, len(users))
		for _, u := range users {
			out = append(out, map[string]interface{}{
				"id": u.ID, "username": u.Username, "display_name": u.DisplayName, "email": u.Email, "language": u.Language, "timezone": u.Timezone, "theme": u.Theme, "role": u.Role,
				"created_at": u.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"), "last_login_at": rfc3339OrNull(u.LastLoginAt),
			})
		}
		printJSON(out)
		return nil
	}
	if len(users) == 0 {
		fmt.Printf("No users. Create one with: %s user create --username <name> --role admin\n", AppName)
		return nil
	}
	fmt.Printf("%-5s %-24s %-24s %-32s %-18s %-6s %s\n", "ID", "USERNAME", "DISPLAY NAME", "EMAIL", "TIMEZONE", "ROLE", "LAST LOGIN")
	for _, u := range users {
		email := u.Email
		if email == "" {
			email = "-"
		}
		fmt.Printf("%-5d %-24s %-24s %-32s %-18s %-6s %s\n", u.ID, u.Username, clip(u.DisplayName, 24), clip(email, 32), clip(u.Timezone, 18), u.Role, formatNullTime(u.LastLoginAt))
	}
	return nil
}

// UserDeleteCmd deletes a user.
type UserDeleteCmd struct {
	Username string `arg:"" help:"Username"`
	Yes      bool   `short:"y" help:"Skip confirmation"`
}

func (c *UserDeleteCmd) Run() error {
	db, err := openDBForCLI()
	if err != nil {
		return err
	}
	defer db.Close()
	u, err := findUser(db, c.Username)
	if err != nil {
		return err
	}
	if u.Role == RoleAdmin {
		n, err := models.CountAdmins(db)
		if err != nil {
			return NewExitError(ExitGeneral, err.Error())
		}
		if n <= 1 {
			return NewExitError(ExitArgument, "cannot delete the last administrator")
		}
	}
	if !c.Yes && !confirm(fmt.Sprintf("Delete user %q? [y/N]: ", u.Username)) {
		fmt.Println("Cancelled")
		return nil
	}
	if err := models.DeleteUser(db, u.ID); err != nil {
		return NewExitError(ExitGeneral, err.Error())
	}
	fmt.Printf("Deleted user %q\n", u.Username)
	return nil
}

// UserPasswdCmd changes a password.
type UserPasswdCmd struct {
	Username string `arg:"" help:"Username"`
	Password string `help:"New password (prompted when omitted)"`
}

func (c *UserPasswdCmd) Run() error {
	db, err := openDBForCLI()
	if err != nil {
		return err
	}
	defer db.Close()
	u, err := findUser(db, c.Username)
	if err != nil {
		return err
	}
	password := c.Password
	if password == "" {
		if password, err = promptPassword("New password"); err != nil {
			return err
		}
	}
	if err := ChangePassword(db, u.ID, password); err != nil {
		return NewExitError(ExitArgument, err.Error())
	}
	fmt.Printf("Password of %q changed (existing Web sessions are signed out)\n", u.Username)
	return nil
}

// UserRoleCmd changes a role.
type UserRoleCmd struct {
	Username string `arg:"" help:"Username"`
	Role     string `help:"New role" enum:"admin,user" required:""`
}

func (c *UserRoleCmd) Run() error {
	db, err := openDBForCLI()
	if err != nil {
		return err
	}
	defer db.Close()
	u, err := findUser(db, c.Username)
	if err != nil {
		return err
	}
	if u.Role == RoleAdmin && c.Role != RoleAdmin {
		n, err := models.CountAdmins(db)
		if err != nil {
			return NewExitError(ExitGeneral, err.Error())
		}
		if n <= 1 {
			return NewExitError(ExitArgument, "cannot demote the last administrator")
		}
	}
	if err := models.UpdateUser(db, u.ID, u.DisplayName, u.Email, u.Language, u.Timezone, u.Theme, c.Role); err != nil {
		return NewExitError(ExitGeneral, err.Error())
	}
	fmt.Printf("Role of %q is now %s\n", u.Username, c.Role)
	return nil
}

// UserUpdateCmd changes the profile fields of a user. Flags that are not
// given leave the field unchanged; --email "" clears the address and an
// empty preference restores its default.
type UserUpdateCmd struct {
	Username    string  `arg:"" help:"Username"`
	DisplayName *string `help:"Display name" name:"display-name"`
	Email       *string `help:"Notification mail address (\"\" clears it)"`
	Language    *string `help:"UI language: ja or en"`
	Timezone    *string `help:"Display timezone, IANA name (e.g. Asia/Tokyo, UTC)"`
	Theme       *string `help:"UI theme: auto, light or dark"`
}

func (c *UserUpdateCmd) Run() error {
	if c.DisplayName == nil && c.Email == nil && c.Language == nil && c.Timezone == nil && c.Theme == nil {
		return NewExitError(ExitArgument, "nothing to change (give at least one of --display-name, --email, --language, --timezone, --theme)")
	}
	db, err := openDBForCLI()
	if err != nil {
		return err
	}
	defer db.Close()
	u, err := findUser(db, c.Username)
	if err != nil {
		return err
	}
	in := ProfileInput{DisplayName: c.DisplayName, Email: c.Email, Language: c.Language, Timezone: c.Timezone, Theme: c.Theme}
	if err := UpdateProfile(db, u, in); err != nil {
		return NewExitError(ExitArgument, err.Error())
	}
	fresh, err := models.GetUserByID(db, u.ID)
	if err != nil {
		return NewExitError(ExitGeneral, err.Error())
	}
	email := fresh.Email
	if email == "" {
		email = "-"
	}
	fmt.Printf("Updated user %q: display name %q, email %s, language %s, timezone %s, theme %s\n",
		fresh.Username, fresh.DisplayName, email, fresh.Language, fresh.Timezone, fresh.Theme)
	return nil
}
