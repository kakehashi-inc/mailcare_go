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
}

// UserCreateCmd creates a user.
type UserCreateCmd struct {
	Username    string `help:"Username ([A-Za-z0-9_.-], 1-64 characters)" required:""`
	DisplayName string `help:"Display name (default: the username)" name:"display-name"`
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
	u, err := CreateUser(db, c.Username, c.DisplayName, password, c.Role)
	if err != nil {
		return NewExitError(ExitArgument, err.Error())
	}
	fmt.Printf("Created user %q (%s, id %d)\n", u.Username, u.Role, u.ID)
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
				"id": u.ID, "username": u.Username, "display_name": u.DisplayName, "role": u.Role,
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
	fmt.Printf("%-5s %-24s %-24s %-6s %s\n", "ID", "USERNAME", "DISPLAY NAME", "ROLE", "LAST LOGIN")
	for _, u := range users {
		fmt.Printf("%-5d %-24s %-24s %-6s %s\n", u.ID, u.Username, clip(u.DisplayName, 24), u.Role, formatNullTime(u.LastLoginAt))
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
	if !c.Yes && !confirm(fmt.Sprintf("Delete user %q and its tokens? [y/N]: ", u.Username)) {
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
	if err := models.UpdateUser(db, u.ID, u.DisplayName, c.Role); err != nil {
		return NewExitError(ExitGeneral, err.Error())
	}
	fmt.Printf("Role of %q is now %s\n", u.Username, c.Role)
	return nil
}
