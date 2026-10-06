// Package tabletopai provides embedded assets (migrations, web templates and static files)
// for the avari-tabletop-backend server.
package tabletopai

import "embed"

// Migrations contains all SQL migration files.
//
//go:embed migrations
var Migrations embed.FS

// Web contains all HTML templates and static assets.
//
//go:embed web
var Web embed.FS
