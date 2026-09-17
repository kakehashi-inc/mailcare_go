package main

import "embed"

//go:embed app/migrations/*.sql
var migrationsFS embed.FS

//go:embed all:frontend/dist
var frontendFS embed.FS

// templates/agent/<provider>/** are the agent workspace skeletons and
// templates/mail/*.txt the notification mail templates.
//
//go:embed all:templates
var templatesFS embed.FS
