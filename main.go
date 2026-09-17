package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"

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
	Token      modules.TokenCmd      `cmd:"" help:"Manage login tokens"`
	Mailbox    modules.MailboxCmd    `cmd:"" help:"Manage monitored mail addresses"`
	Check      modules.CheckCmd      `cmd:"" help:"Fetch new mail and detect bounces"`
	Reindex    modules.ReindexCmd    `cmd:"" help:"Rebuild the mail index from the raw files"`
	Reclassify modules.ReclassifyCmd `cmd:"" help:"Re-run bounce detection and grouping"`
	Analyze    modules.AnalyzeCmd    `cmd:"" help:"Run the agent analysis over bounce groups"`
	Groups     modules.GroupsCmd     `cmd:"" help:"List the bounce groups of a mail address"`
	Group      modules.GroupCmd      `cmd:"" help:"Show a bounce group and its analysis report"`
	Schedule   modules.ScheduleCmd   `cmd:"" help:"Show or set the daily check times"`
	Settings   modules.SettingsCmd   `cmd:"" help:"Show or change settings"`
	Jobs       modules.JobsCmd       `cmd:"" help:"Show the job history"`
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
		kong.ConfigureHelp(kong.HelpOptions{Compact: true}),
		// Defaults are referenced from constants so help text never drifts.
		kong.Vars{
			"default_web_listen":   modules.DefaultWebListenAddr,
			"default_web_port":     strconv.Itoa(modules.DefaultWebPort),
			"default_imap_port":    strconv.Itoa(modules.DefaultIMAPPort),
			"default_initial_days": strconv.Itoa(modules.DefaultInitialDays),
			"default_recent_days":  strconv.Itoa(modules.DefaultRecentDays),
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
