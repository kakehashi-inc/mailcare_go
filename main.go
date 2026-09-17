package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"mailcare/app/models"
	"mailcare/app/modules"
	_ "mailcare/app/workers" // registers modules.StartServer via init()

	"github.com/alecthomas/kong"
)

// version is injected at release time via -ldflags "-X main.version=...".
var version = "dev"

// CLI defines the top-level command structure. Startup parameter parsing lives
// here; the commands themselves are implemented in app/modules/cli_*.go.
type CLI struct {
	DataDir string `help:"Data directory (default: <executable dir>/data)" name:"data-dir" type:"path" placeholder:"DIR"`

	Service    modules.ServiceCmd    `cmd:"" help:"Start, stop or inspect the Web server"`
	User       modules.UserCmd       `cmd:"" help:"Manage users"`
	Token      modules.TokenCmd      `cmd:"" help:"Manage API tokens (reserved for the future API)"`
	Mailbox    modules.MailboxCmd    `cmd:"" help:"Manage monitored mail addresses"`
	Sync       modules.SyncCmd       `cmd:"" help:"Fetch new mail, group the bounces and queue the analysis"`
	Fetch      modules.FetchCmd      `cmd:"" help:"Fetch new mail only (no grouping)"`
	Group      modules.GroupCmd      `cmd:"" help:"Group the mail not grouped yet (no fetch)"`
	Reindex    modules.ReindexCmd    `cmd:"" help:"Rebuild the mail index from the raw files"`
	Reclassify modules.ReclassifyCmd `cmd:"" help:"Re-run bounce detection and grouping over every mail"`
	Analyze    modules.AnalyzeCmd    `cmd:"" help:"Run the agent analysis over bounce groups"`
	Notify     modules.NotifyCmd     `cmd:"" help:"Send the alert notification mail now, or an SMTP test mail (--test)"`
	Cleanup    modules.CleanupCmd    `cmd:"" help:"Remove the mails and agent workspaces older than their retention (runs daily on the server)"`
	Groups     modules.GroupsCmd     `cmd:"" help:"List the bounce groups of a mail address, show one group (ADDRESS KEY) or change its state (set-state)"`
	Schedule   modules.ScheduleCmd   `cmd:"" help:"Show or set the daily check times"`
	Settings   modules.SettingsCmd   `cmd:"" help:"Show or change settings"`
	Jobs       modules.JobsCmd       `cmd:"" help:"Show the job history, or cancel a queued job (cancel ID)"`
	Version    VersionCmd            `cmd:"" help:"Show version"`
}

// VersionCmd prints the version.
type VersionCmd struct{}

func (v *VersionCmd) Run() error {
	fmt.Println(version)
	return nil
}

func main() {
	// Wire binary-embedded assets into the modules package.
	modules.MigrationsFS = migrationsFS
	modules.FrontendFS = frontendFS
	modules.TemplatesFS = templatesFS
	modules.AppVersion = version

	var cli CLI
	ctx := kong.Parse(&cli,
		kong.Name(modules.AppName),
		kong.Description("MailCare: collects bounce mail from IMAP accounts, bundles similar bounces and explains what to do about them."),
		kong.UsageOnError(),
		// kong exits with 80 on a usage error; map it to the documented
		// argument error code (help output keeps exit 0).
		kong.Exit(func(code int) {
			if code != 0 {
				code = modules.ExitArgument
			}
			os.Exit(code)
		}),
		kong.ConfigureHelp(kong.HelpOptions{Compact: true}),
		// Defaults are referenced from constants so help text never drifts.
		kong.Vars{
			"default_web_listen":     modules.DefaultWebListenAddr,
			"default_web_port":       strconv.Itoa(modules.DefaultWebPort),
			"default_imap_port":      strconv.Itoa(modules.DefaultIMAPPort),
			"default_initial_days":   strconv.Itoa(modules.DefaultInitialDays),
			"default_recent_days":    strconv.Itoa(modules.DefaultRecentDays),
			"default_workers":        strconv.Itoa(modules.DefaultWorkers),
			"max_workers":            strconv.Itoa(modules.MaxWorkers),
			"default_mail_keep_days": strconv.Itoa(modules.DefaultMailKeepDays),
			"min_mail_keep_days":     strconv.Itoa(modules.MinMailKeepDays),
			"max_mail_keep_days":     strconv.Itoa(modules.MaxMailKeepDays),
			"default_timezone":       models.DefaultTimezone,
			"default_language":       models.DefaultLanguage,
			"default_theme":          models.DefaultTheme,
		},
	)
	modules.SetDataDir(cli.DataDir)

	err := ctx.Run()
	if err != nil {
		var exitErr *modules.ExitError
		if errors.As(err, &exitErr) {
			fmt.Fprintf(os.Stderr, "Error: %s\n", exitErr.Message)
			os.Exit(exitErr.Code)
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(modules.ExitGeneral)
	}
}
