// Command migrate applies pending SQL migrations to the Agent OS SQLite database.
// Usage:
//
//	go run ./cmd/migrate/ -path ./data/agentos.db
//	SQLITE_PATH=./data/agentos.db go run ./cmd/migrate/
package main

import (
	"flag"
	"log/slog"
	"os"

	"github.com/marcoantonios1/Agent-OS/internal/memory"
)

func main() {
	path := flag.String("path", os.Getenv("SQLITE_PATH"), "path to the SQLite database file")
	flag.Parse()

	if *path == "" {
		slog.Error("database path required: set -path or SQLITE_PATH")
		os.Exit(1)
	}

	db, err := memory.OpenDB(*path)
	if err != nil {
		slog.Error("migrate failed", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	slog.Info("all migrations applied", "path", *path)
}
