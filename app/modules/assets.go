package modules

import "io/fs"

// Embedded filesystems are injected from package main at startup.
//
//   - MigrationsFS: root contains "app/migrations/*.sql" (goose migrations).
//   - FrontendFS:   root contains "frontend/dist/**" (the built SPA).
//
// They are package-level variables (mirroring the StartServer pattern) so that
// modules/workers can reach the binary-embedded assets without an import cycle
// back into package main.
var (
	MigrationsFS fs.FS
	FrontendFS   fs.FS
)

// AppVersion is the build version injected from package main at startup
// (set via -ldflags "-X main.version=..."; "dev" for unreleased builds).
var AppVersion = "dev"
