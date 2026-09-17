package main

import "embed"

//go:embed app/migrations/*.sql
var migrationsFS embed.FS

//go:embed all:frontend/dist
var frontendFS embed.FS
