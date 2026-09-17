package modules

import (
	"fmt"

	"mailcare/app/models"
)

// --- mailbox commands ---

// MailboxCmd groups the mailbox subcommands.
type MailboxCmd struct {
	Add    MailboxAddCmd    `cmd:"" help:"Register a mail address (IMAP account)"`
	List   MailboxListCmd   `cmd:"" help:"List mail addresses"`
	Show   MailboxShowCmd   `cmd:"" help:"Show a mail address"`
	Update MailboxUpdateCmd `cmd:"" help:"Update a mail address"`
	Delete MailboxDeleteCmd `cmd:"" help:"Delete a mail address"`
	Test   MailboxTestCmd   `cmd:"" help:"Test the IMAP connection of a mail address"`
}

// mailboxRow renders a mailbox for JSON output.
func mailboxRow(mb *models.Mailbox) map[string]interface{} {
	return map[string]interface{}{
		"id": mb.ID, "address": mb.Address, "display_name": mb.DisplayName, "imap_host": mb.ImapHost,
		"imap_port": mb.ImapPort, "imap_security": mb.ImapSecurity, "imap_username": mb.ImapUsername,
		"folder": mb.Folder, "enabled": mb.Enabled, "initial_days": mb.InitialDays, "recent_days": mb.RecentDays,
		"last_fetched_at": rfc3339OrNull(mb.LastFetchedAt), "last_fetch_error": mb.LastFetchError,
	}
}

func printMailbox(mb *models.Mailbox) {
	fmt.Printf("id:            %d\n", mb.ID)
	fmt.Printf("address:       %s\n", mb.Address)
	fmt.Printf("display name:  %s\n", mb.DisplayName)
	fmt.Printf("imap:          %s:%d (%s)\n", mb.ImapHost, mb.ImapPort, mb.ImapSecurity)
	fmt.Printf("username:      %s\n", mb.ImapUsername)
	fmt.Printf("folder:        %s\n", mb.Folder)
	fmt.Printf("enabled:       %v\n", mb.Enabled)
	fmt.Printf("initial days:  %d\n", mb.InitialDays)
	fmt.Printf("recent days:   %d\n", mb.RecentDays)
	fmt.Printf("last fetch:    %s (%s)\n", formatNullTime(mb.LastFetchedAt), fetchStatus(mb))
	if mb.LastFetchError != "" {
		fmt.Printf("last error:    %s\n", mb.LastFetchError)
	}
}

// MailboxAddCmd registers a mailbox.
type MailboxAddCmd struct {
	Address     string `help:"Mail address" required:""`
	Host        string `help:"IMAP host" required:""`
	Port        int    `help:"IMAP port (default: ${default_imap_port})" default:"0"`
	Security    string `help:"Connection security" enum:"ssl,starttls,none" default:"ssl"`
	Username    string `help:"IMAP username" required:""`
	Password    string `help:"IMAP password (prompted when omitted)"`
	Folder      string `help:"IMAP folder" default:"INBOX"`
	InitialDays int    `help:"Days to look back on the first check (default: ${default_initial_days})" name:"initial-days" default:"0"`
	RecentDays  int    `help:"Days to look back on later checks (default: ${default_recent_days})" name:"recent-days" default:"0"`
	DisplayName string `help:"Display name" name:"display-name"`
	Disabled    bool   `help:"Register as disabled (not checked automatically)"`
}

func (c *MailboxAddCmd) Run() error {
	db, err := openDBForCLI()
	if err != nil {
		return err
	}
	defer db.Close()
	key, err := loadKeyForCLI()
	if err != nil {
		return err
	}
	password := c.Password
	if password == "" {
		if password, err = promptPassword("IMAP password"); err != nil {
			return err
		}
	}
	enabled := !c.Disabled
	in := &MailboxInput{
		Address: c.Address, DisplayName: c.DisplayName, ImapHost: c.Host, ImapPort: c.Port, ImapSecurity: c.Security,
		ImapUsername: c.Username, ImapPassword: password, Folder: c.Folder, Enabled: &enabled,
		InitialDays: c.InitialDays, RecentDays: c.RecentDays,
	}
	mb, err := CreateMailbox(db, key, in)
	if err != nil {
		return NewExitError(ExitArgument, err.Error())
	}
	fmt.Printf("Registered mailbox %q (id %d)\n", mb.Address, mb.ID)
	fmt.Print("Testing the IMAP connection... ")
	ctx, cancel := signalContext()
	defer cancel()
	if err := TestMailboxConnection(ctx, db, key, MailboxInputFrom(mb)); err != nil {
		fmt.Println("failed")
		fmt.Printf("Warning: %v\n", err)
		fmt.Printf("Fix the settings with: %s mailbox update %s ...\n", AppName, mb.Address)
		return nil
	}
	fmt.Println("ok")
	return nil
}

// MailboxListCmd lists mailboxes.
type MailboxListCmd struct {
	JSON bool `help:"Output as JSON"`
}

func (c *MailboxListCmd) Run() error {
	db, err := openDBForCLI()
	if err != nil {
		return err
	}
	defer db.Close()
	mailboxes, err := models.ListMailboxes(db)
	if err != nil {
		return NewExitError(ExitGeneral, err.Error())
	}
	if c.JSON {
		out := make([]map[string]interface{}, 0, len(mailboxes))
		for _, mb := range mailboxes {
			out = append(out, mailboxRow(mb))
		}
		printJSON(out)
		return nil
	}
	if len(mailboxes) == 0 {
		fmt.Printf("No mailboxes. Register one with: %s mailbox add --address <address> --host <imap host> --username <user>\n", AppName)
		return nil
	}
	fmt.Printf("%-5s %-32s %-28s %-8s %-20s %s\n", "ID", "ADDRESS", "IMAP", "ENABLED", "LAST FETCH", "STATUS")
	for _, mb := range mailboxes {
		imap := fmt.Sprintf("%s:%d", mb.ImapHost, mb.ImapPort)
		fmt.Printf("%-5d %-32s %-28s %-8v %-20s %s\n", mb.ID, mb.Address, clip(imap, 28), mb.Enabled, formatNullTime(mb.LastFetchedAt), fetchStatus(mb))
	}
	return nil
}

// fetchStatus derives never / ok / error from the last fetch of a mailbox.
func fetchStatus(mb *models.Mailbox) string {
	switch {
	case !mb.LastFetchedAt.Valid:
		return "never"
	case mb.LastFetchError != "":
		return "error"
	}
	return "ok"
}

// MailboxShowCmd shows one mailbox.
type MailboxShowCmd struct {
	Address string `arg:"" help:"Mail address"`
	JSON    bool   `help:"Output as JSON"`
}

func (c *MailboxShowCmd) Run() error {
	db, err := openDBForCLI()
	if err != nil {
		return err
	}
	defer db.Close()
	mb, err := findMailbox(db, c.Address)
	if err != nil {
		return err
	}
	if c.JSON {
		printJSON(mailboxRow(mb))
		return nil
	}
	printMailbox(mb)
	return nil
}

// MailboxUpdateCmd updates the given fields of a mailbox.
type MailboxUpdateCmd struct {
	Address     string `arg:"" help:"Mail address"`
	NewAddress  string `help:"New mail address" name:"new-address"`
	Host        string `help:"IMAP host"`
	Port        int    `help:"IMAP port" default:"0"`
	Security    string `help:"Connection security" enum:",ssl,starttls,none" default:""`
	Username    string `help:"IMAP username"`
	Password    string `help:"IMAP password"`
	AskPassword bool   `help:"Prompt for a new IMAP password" name:"ask-password"`
	Folder      string `help:"IMAP folder"`
	InitialDays int    `help:"Days to look back on the first check" name:"initial-days" default:"0"`
	RecentDays  int    `help:"Days to look back on later checks" name:"recent-days" default:"0"`
	DisplayName string `help:"Display name" name:"display-name"`
	Enabled     bool   `help:"Enable the mailbox"`
	Disabled    bool   `help:"Disable the mailbox"`
}

func (c *MailboxUpdateCmd) Run() error {
	if c.Enabled && c.Disabled {
		return NewExitError(ExitArgument, "--enabled and --disabled are mutually exclusive")
	}
	db, err := openDBForCLI()
	if err != nil {
		return err
	}
	defer db.Close()
	key, err := loadKeyForCLI()
	if err != nil {
		return err
	}
	mailsRoot, _, err := dataRoots()
	if err != nil {
		return err
	}
	mb, err := findMailbox(db, c.Address)
	if err != nil {
		return err
	}
	in := MailboxInputFrom(mb)
	if c.NewAddress != "" {
		in.Address = c.NewAddress
	}
	if c.Host != "" {
		in.ImapHost = c.Host
	}
	if c.Port != 0 {
		in.ImapPort = c.Port
	}
	if c.Security != "" {
		in.ImapSecurity = c.Security
	}
	if c.Username != "" {
		in.ImapUsername = c.Username
	}
	if c.Password != "" {
		in.ImapPassword = c.Password
	} else if c.AskPassword {
		if in.ImapPassword, err = promptPassword("IMAP password"); err != nil {
			return err
		}
	}
	if c.Folder != "" {
		in.Folder = c.Folder
	}
	if c.InitialDays != 0 {
		in.InitialDays = c.InitialDays
	}
	if c.RecentDays != 0 {
		in.RecentDays = c.RecentDays
	}
	if c.DisplayName != "" {
		in.DisplayName = c.DisplayName
	}
	if c.Enabled || c.Disabled {
		enabled := c.Enabled
		in.Enabled = &enabled
	}
	if err := UpdateMailbox(db, key, mailsRoot, mb, in); err != nil {
		return NewExitError(ExitArgument, err.Error())
	}
	fmt.Printf("Updated mailbox %q\n", mb.Address)
	return nil
}

// MailboxDeleteCmd deletes a mailbox.
type MailboxDeleteCmd struct {
	Address  string `arg:"" help:"Mail address"`
	Yes      bool   `short:"y" help:"Skip confirmation"`
	KeepData bool   `help:"Keep the raw mail files and the index" name:"keep-data"`
}

func (c *MailboxDeleteCmd) Run() error {
	db, err := openDBForCLI()
	if err != nil {
		return err
	}
	defer db.Close()
	mailsRoot, _, err := dataRoots()
	if err != nil {
		return err
	}
	mb, err := findMailbox(db, c.Address)
	if err != nil {
		return err
	}
	what := "and its mail data"
	if c.KeepData {
		what = "(keeping its mail data)"
	}
	if !c.Yes && !confirm(fmt.Sprintf("Delete mailbox %q %s? [y/N]: ", mb.Address, what)) {
		fmt.Println("Cancelled")
		return nil
	}
	if err := DeleteMailbox(db, mailsRoot, mb, c.KeepData); err != nil {
		return NewExitError(ExitGeneral, err.Error())
	}
	fmt.Printf("Deleted mailbox %q\n", mb.Address)
	return nil
}

// MailboxTestCmd tests the IMAP connection of a stored mailbox.
type MailboxTestCmd struct {
	Address string `arg:"" help:"Mail address"`
}

func (c *MailboxTestCmd) Run() error {
	db, err := openDBForCLI()
	if err != nil {
		return err
	}
	defer db.Close()
	key, err := loadKeyForCLI()
	if err != nil {
		return err
	}
	mb, err := findMailbox(db, c.Address)
	if err != nil {
		return err
	}
	fmt.Printf("Testing the IMAP connection of %s (%s:%d, %s)... ", mb.Address, mb.ImapHost, mb.ImapPort, mb.ImapSecurity)
	ctx, cancel := signalContext()
	defer cancel()
	if err := TestMailboxConnection(ctx, db, key, MailboxInputFrom(mb)); err != nil {
		fmt.Println("failed")
		return NewExitError(ExitExec, err.Error())
	}
	fmt.Println("ok")
	return nil
}
