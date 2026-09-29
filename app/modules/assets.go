package modules

import "io/fs"

// The files built into the binary (embed.go), passed in by package main at
// startup (the migrations go to package dbschema as dbschema.FS).
//
//   - FrontendFS: root contains "frontend/dist/**" (the built SPA) and
//     "frontend/src/i18n/<language>.json" (the language files of the Web UI;
//     the server words the category names of the notification mail and the
//     PDF reports with them, LanguageFile).
//   - EmbeddedFS: the embedded/ directory: "templates/agent/<provider>/**"
//     (workspace skeletons copied into the agent workspace of a group; the
//     agent package names that directory), "templates/mail/*.txt" (the
//     notification mail templates, see notify.go),
//     "templates/export/group_README.md" (the README.md of a group export),
//     "fonts/*" (the fonts of the PDF reports, package pdfreport) and
//     "migrations/**".
//
// They are package-level variables (mirroring the StartServer pattern) so that
// modules/workers can reach the binary-embedded assets without an import cycle
// back into package main.
var (
	FrontendFS fs.FS
	EmbeddedFS fs.FS
)

// AppVersion is the build version injected from package main at startup
// (set via -ldflags "-X main.version=..."; "dev" for unreleased builds).
var AppVersion = "dev"
