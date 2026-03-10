package handler

import "strings"

// isUnique returns true if err is a SQLite UNIQUE constraint violation.
func isUnique(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint") || strings.Contains(msg, "unique")
}
