package main

import "embed"

// Everything built into the binary is embedded here, from two places only:
//
//   - frontend: the built SPA (frontend/dist) and the language files of the
//     Web UI (frontend/src/i18n/*.json; the server words the notification
//     mail's category names and the PDF reports with them).
//   - embedded/: every other file the binary carries (templates/, fonts/,
//     migrations/; see modules.EmbeddedFS).
//
// main passes them to the packages that read them (modules.FrontendFS,
// modules.EmbeddedFS, dbschema.FS); no other package embeds files.

//go:embed all:frontend/dist frontend/src/i18n/*.json
var frontendFS embed.FS

//go:embed all:embedded
var embeddedFS embed.FS
