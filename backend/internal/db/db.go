package db

import (
	"database/sql"
	"embed"
	"fmt"
	"strings"

	_ "github.com/deploy-paas/backend/internal/rqlitedrv"
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

// applySchema runs each CREATE TABLE / CREATE INDEX statement from schema.sql.
// Statements are split on ";" so they can be executed individually via rqlite.
func applySchema(db *sql.DB) error {
	schema, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		return fmt.Errorf("read schema: %w", err)
	}

	stmts := strings.Split(string(schema), ";")
	for _, s := range stmts {
		s = strings.TrimSpace(s)
		if s == "" || strings.HasPrefix(s, "--") {
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
