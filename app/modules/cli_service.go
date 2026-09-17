package modules

// --- service commands ---

// ServiceCmd manages the HTTP server.
type ServiceCmd struct {
	Start  ServiceStartCmd  `cmd:"" help:"Start the Web server (foreground)"`
	Stop   ServiceStopCmd   `cmd:"" help:"Stop the running server"`
	Status ServiceStatusCmd `cmd:"" help:"Show the status of the running server"`
}

// ServiceStartCmd starts the server. Zero values mean "not specified" so the
// server can fall back to the saved setting and then the code default.
type ServiceStartCmd struct {
	WebListen string   `help:"Listen address (default: saved setting or ${default_web_listen})" name:"web-listen" default:""`
	WebPort   int      `help:"Listen port (default: saved setting or ${default_web_port})" name:"web-port" default:"0"`
	CheckTime []string `help:"Daily check time HH:MM (repeatable; saved as the new schedule)" name:"check-time" placeholder:"HH:MM"`
}

func (c *ServiceStartCmd) Run() error {
	if StartServer == nil {
		return NewExitError(ExitGeneral, "server not linked")
	}
	if c.WebPort < 0 || c.WebPort > 65535 {
		return NewExitError(ExitArgument, "web-port must be between 1 and 65535")
	}
	var times []string
	if len(c.CheckTime) > 0 {
		var err error
		times, err = ParseCheckTimes(c.CheckTime)
		if err != nil {
			return NewExitError(ExitArgument, err.Error())
		}
	}
	if err := StartServer(c.WebListen, c.WebPort, times); err != nil {
		return NewExitError(ExitExec, err.Error())
	}
	return nil
}

// resolveControlPort returns the port to reach the local server on: the
// given port, else the saved setting, else the default.
func resolveControlPort(port int) (int, error) {
	if port != 0 {
		if port < 1 || port > 65535 {
			return 0, NewExitError(ExitArgument, "web-port must be between 1 and 65535")
		}
		return port, nil
	}
	db, err := openDBForCLI()
	if err != nil {
		return 0, err
	}
	defer db.Close()
	return ResolveWebPort(db, 0), nil
}

// ServiceStopCmd stops the server through the control endpoint.
type ServiceStopCmd struct {
	WebPort int `help:"Port of the running server (default: saved setting or ${default_web_port})" name:"web-port" default:"0"`
}

func (c *ServiceStopCmd) Run() error {
	port, err := resolveControlPort(c.WebPort)
	if err != nil {
		return err
	}
	if err := StopServer(port); err != nil {
		return NewExitError(ExitExec, err.Error())
	}
	return nil
}

// ServiceStatusCmd shows the server status through the control endpoint.
type ServiceStatusCmd struct {
	WebPort int `help:"Port of the running server (default: saved setting or ${default_web_port})" name:"web-port" default:"0"`
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
