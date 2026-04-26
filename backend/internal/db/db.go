package db

import (
	"database/sql"
	"embed"
	"fmt"
	"strings"

	_ "github.com/ojhapranjal26/featherdeploy/backend/internal/rqlitedrv"
)

//go:embed schema.sql
var schemaFS embed.FS

// OpenRqlite connects to a running rqlite server at the given URL
// (e.g. "http://127.0.0.1:4001") and applies all schema migrations.
func OpenRqlite(url string) (*sql.DB, error) {
	db, err := sql.Open("rqlite", url)
	if err != nil {
		return nil, fmt.Errorf("open rqlite: %w", err)
	}

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping rqlite: %w", err)
	}

	if err := applySchema(db); err != nil {
		return nil, err
	}
	return db, nil
}

// applySchema runs each schema statement from schema.sql.
// Comments are stripped before splitting on ";" so semicolons inside comments
// cannot turn into bogus SQL fragments during startup.
func applySchema(db *sql.DB) error {
	schema, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		return fmt.Errorf("read schema: %w", err)
	}

	var sqlLines []string
	for _, line := range strings.Split(string(schema), "\n") {
		if idx := strings.Index(line, "--"); idx >= 0 {
			line = line[:idx]
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		sqlLines = append(sqlLines, line)
	}

	stmts := strings.Split(strings.Join(sqlLines, "\n"), ";")
	for _, s := range stmts {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, err := db.Exec(s); err != nil {
			msg := strings.ToLower(err.Error())
			// Ignore benign schema errors:
			// - "already exists"  — IF NOT EXISTS not supported for all statement types
			// - "duplicate column" — ALTER TABLE ADD COLUMN on already-migrated DB
			if strings.Contains(msg, "already exists") ||
				strings.Contains(msg, "duplicate column") {
				continue
			}
			return fmt.Errorf("schema exec (%q): %w", s[:min(40, len(s))], err)
		}
	}
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
