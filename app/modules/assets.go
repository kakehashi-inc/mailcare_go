package modules

import "io/fs"

// Embedded filesystems are injected from package main at startup (the goose
// migrations are embedded by package app/migrations itself).
//
//   - FrontendFS:   root contains "frontend/dist/**" (the built SPA).
//   - TemplatesFS:  root contains "templates/agent/<provider>/**" (workspace
//     skeletons copied into the agent workspace of a group; the agent
//     package names that directory), "templates/mail/*.txt" (the
//     notification mail templates, see notify.go) and
//     "frontend/src/i18n/<language>.json" (the language files of the Web
//     UI; the mails take the category names from them).
//
// They are package-level variables (mirroring the StartServer pattern) so that
// modules/workers can reach the binary-embedded assets without an import cycle
// back into package main.
var (
	FrontendFS  fs.FS
	TemplatesFS fs.FS
)

// AppVersion is the build version injected from package main at startup
// (set via -ldflags "-X main.version=..."; "dev" for unreleased builds).
var AppVersion = "dev"
