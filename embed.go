package main

import "embed"

//go:embed app/migrations/*.sql
var migrationsFS embed.FS

//go:embed all:frontend/dist
var frontendFS embed.FS

//go:embed all:agent-templates
var templatesFS embed.FS
