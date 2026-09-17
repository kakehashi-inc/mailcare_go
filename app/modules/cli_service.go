package modules

// --- service commands ---

// ServiceCmd manages the HTTP server.
type ServiceCmd struct {
	Start  ServiceStartCmd  `cmd:"" help:"Start the Web server (foreground)"`
	Stop   ServiceStopCmd   `cmd:"" help:"Stop the running server"`
	Status ServiceStatusCmd `cmd:"" help:"Show the status of the running server"`
}

// ServiceStartCmd starts the server. The numeric flags are pointers so that
// an omitted flag (nil) is told apart from an explicit value: only an omitted
// flag falls back to the saved setting and then the code default, while any
// given value, 0 included, is validated as it is.
type ServiceStartCmd struct {
	WebListen    string   `help:"Listen address (default: saved setting or ${default_web_listen})" name:"web-listen" default:""`
	WebPort      *int     `help:"Listen port (default: saved setting or ${default_web_port})" name:"web-port" placeholder:"N"`
	Workers      *int     `help:"Jobs run at once, 1-${max_workers} (default: saved setting or ${default_workers}; saved)" name:"workers" placeholder:"N"`
	CheckTime    []string `help:"Daily check time HH:MM (repeatable; saved as the new schedule)" name:"check-time" placeholder:"HH:MM"`
	MailKeepDays *int     `help:"Days a fetched mail is kept, ${min_mail_keep_days}-${max_mail_keep_days} (default: saved setting or ${default_mail_keep_days}; saved)" name:"mail-keep-days" placeholder:"N"`
}

func (c *ServiceStartCmd) Run() error {
	if StartServer == nil {
		return NewExitError(ExitGeneral, "server not linked")
	}
	// StartServer takes 0 for "not specified"; every given value has been
	// checked to be in range (never 0) by then.
	webPort, err := validateWebPortFlag(c.WebPort)
	if err != nil {
		return err
	}
	workers := 0
	if c.Workers != nil {
		if *c.Workers < 1 || *c.Workers > MaxWorkers {
			return NewExitErrorf(ExitArgument, "workers must be between 1 and %d", MaxWorkers)
		}
		workers = *c.Workers
	}
	mailKeepDays := 0
	if c.MailKeepDays != nil {
		if err := ValidateMailKeepDays(*c.MailKeepDays); err != nil {
			return NewExitError(ExitArgument, err.Error())
		}
		mailKeepDays = *c.MailKeepDays
	}
	var times []string
	if len(c.CheckTime) > 0 {
		times, err = ParseCheckTimes(c.CheckTime)
		if err != nil {
			return NewExitError(ExitArgument, err.Error())
		}
	}
	if err := StartServer(c.WebListen, webPort, workers, times, mailKeepDays); err != nil {
		return NewExitError(ExitExec, err.Error())
	}
	return nil
}

// validateWebPortFlag checks an optional --web-port flag: nil (omitted)
// yields 0 for "not specified", a given value must be 1..65535.
func validateWebPortFlag(port *int) (int, error) {
	if port == nil {
		return 0, nil
	}
	if *port < 1 || *port > 65535 {
		return 0, NewExitError(ExitArgument, "web-port must be between 1 and 65535")
	}
	return *port, nil
}

// resolveControlPort returns the port to reach the local server on: the
// given port, else the saved setting, else the default.
func resolveControlPort(port *int) (int, error) {
	if port != nil {
		return validateWebPortFlag(port)
	}
	db, err := openDBForCLI()
	if err != nil {
		return 0, err
	}
	defer db.Close()
	return ResolveWebPort(db, 0), nil
}

// ServiceStopCmd stops the server through the control endpoint. Only the
// server of this process's data directory is stopped: the port may be in use
// by a MailCare server started for another data directory.
type ServiceStopCmd struct {
	WebPort *int `help:"Port of the running server (default: saved setting or ${default_web_port})" name:"web-port" placeholder:"N"`
}

func (c *ServiceStopCmd) Run() error {
	port, err := resolveControlPort(c.WebPort)
	if err != nil {
		return err
	}
	dataDir, err := DataDir()
	if err != nil {
		return NewExitErrorf(ExitFileIO, "%s", err)
	}
	st, err := GetServerStatus(port)
	if err != nil {
		return NewExitError(ExitExec, err.Error())
	}
	if !st.ServesDataDir(dataDir) {
		reported := st.DataDir
		if reported == "" {
			reported = "(not reported)"
		}
		return NewExitErrorf(ExitExec, "the server on port %d serves another data directory: %s", port, reported)
	}
	if err := StopServer(port); err != nil {
		return NewExitError(ExitExec, err.Error())
	}
	return nil
}

// ServiceStatusCmd shows the server status through the control endpoint.
type ServiceStatusCmd struct {
	WebPort *int `help:"Port of the running server (default: saved setting or ${default_web_port})" name:"web-port" placeholder:"N"`
}

func (c *ServiceStatusCmd) Run() error {
	port, err := resolveControlPort(c.WebPort)
	if err != nil {
		return err
	}
	if err := ShowServerStatus(port); err != nil {
		return NewExitError(ExitExec, err.Error())
	}
	return nil
}
