package modules

import "time"

// --- Application ---

const (
	// AppName is the executable name, also used as the CLI name and in status
	// output. Replace "develop_app" when starting a project from this template.
	AppName = "develop_app"
	// DBFileName is the SQLite database file created inside the data directory.
	DBFileName = "develop_app.db"
)

// --- Token ---

const (
	// TokenPrefix is prepended to the random part of every bearer token.
	TokenPrefix = "dap_"
	// TokenRandomBytes is the number of random bytes (hex-encoded) in a token.
	TokenRandomBytes = 20 // -> 40 hex chars, full token "dap_" + 40 = 44 chars
)

// --- Server defaults ---

const (
	// The API and Web servers have independent listen addresses.
	DefaultApiListenAddr = "0.0.0.0"
	DefaultWebListenAddr = "0.0.0.0"
	DefaultApiPort       = 8081 // API port (JSON API + local control endpoints)
	DefaultWebPort       = 8080 // Web UI port (SPA)
	// ReadHeaderTimeout bounds how long a client may take to send its request
	// headers (protects the servers from slowloris-style connections).
	ReadHeaderTimeout = 10 * time.Second
	// ShutdownGraceTimeout bounds how long a graceful shutdown waits for the HTTP
	// servers to drain in-flight requests before giving up.
	ShutdownGraceTimeout = 5 * time.Second
)

// --- Settings keys ---
//
// Keys of the settings table. A key is stored only when its value differs from
// the code default, so a later change to a default takes effect on its own.

const (
	SettingApiListen = "api_listen"
	SettingWebListen = "web_listen"
	SettingApiPort   = "api_port"
	SettingWebPort   = "web_port"
)
