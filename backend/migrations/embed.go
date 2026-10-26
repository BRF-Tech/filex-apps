// Package migrations embeds the schema's SQL files. They are applied in
// file-name order, each once, recorded in schema_migrations (the runner is
// the backend lane's, internal/db). A file is never edited after it has
// been applied anywhere; a change is a new file.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
