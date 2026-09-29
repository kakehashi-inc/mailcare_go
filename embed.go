package main

import "embed"

//go:embed all:frontend/dist
var frontendFS embed.FS

// templates/agent/<provider>/** are the agent workspace skeletons,
// templates/mail/*.txt the notification mail templates and
// frontend/src/i18n/*.json the language files of the Web UI (the mails take
// the category names from them).
//
//go:embed all:templates frontend/src/i18n/*.json
var templatesFS embed.FS
