// Package migrations embeds all *.sql migration files so they are included
// in the binary and can be applied without any filesystem dependency at runtime.
package migrations

import "embed"

// FS holds the embedded migration SQL files.
// Files are named NNN_description.sql and applied in lexicographic order.
//
//go:embed *.sql
var FS embed.FS
