package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"develop_app/app/modules"
	_ "develop_app/app/workers" // registers modules.StartServer via init()

	"github.com/alecthomas/kong"
)

// version is injected at release time via -ldflags "-X main.version=...".
var version = "dev"

// CLI defines the top-level command structure. Startup parameter parsing lives
// here; the commands themselves are implemented in app/modules/cli.go.
type CLI struct {
	DataDir string `help:"Data directory (default: <executable dir>/data)" name:"data-dir" type:"path" placeholder:"DIR"`

	Token   modules.TokenCmd   `cmd:"" help:"Manage access tokens"`
	Service modules.ServiceCmd `cmd:"" help:"Manage the API + Web server"`
	Version VersionCmd         `cmd:"" help:"Show version"`
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
	modules.AppVersion = version

	var cli CLI
	ctx := kong.Parse(&cli,
		kong.Name(modules.AppName),
		kong.Description("Develop App: Web server with an embedded SPA and a SQLite database."),
		kong.UsageOnError(),
		kong.ConfigureHelp(kong.HelpOptions{Compact: true}),
		// Defaults are referenced from constants so help text never drifts.
		kong.Vars{
			"default_api_listen": modules.DefaultApiListenAddr,
			"default_web_listen": modules.DefaultWebListenAddr,
			"default_api_port":   strconv.Itoa(modules.DefaultApiPort),
			"default_web_port":   strconv.Itoa(modules.DefaultWebPort),
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
