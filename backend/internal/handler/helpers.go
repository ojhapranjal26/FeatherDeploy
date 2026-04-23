package handler

import (
	"fmt"
	"strings"
	"time"
)

// isUnique returns true if err is a SQLite UNIQUE constraint violation.
func isUnique(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint") || strings.Contains(msg, "unique")
}

// flexTime is a sql.Scanner that accepts time.Time, string (multiple datetime
// layouts), or nil.  It is needed because the rqlite HTTP driver may return
// DATETIME column values as time.Time when column-type metadata is present, or
// as a raw string when it is absent — both must be handled gracefully.
type flexTime struct {
	Time  time.Time
	Valid bool
}

var dtLayouts = []string{
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05Z",
	"2006-01-02T15:04:05-07:00",
	"2006-01-02T15:04:05.999999999Z",
	"2006-01-02T15:04:05.999999999-07:00",
	"2006-01-02",
}

func (ft *flexTime) Scan(src interface{}) error {
	switch v := src.(type) {
	case time.Time:
		ft.Time = v.UTC()
		ft.Valid = true
		return nil
	case string:
		for _, layout := range dtLayouts {
			if t, err := time.Parse(layout, v); err == nil {
				ft.Time = t.UTC()
				ft.Valid = true
				return nil
			}
		}
		ft.Valid = false
		return nil
	case nil:
		ft.Valid = false
		return nil
	default:
		return fmt.Errorf("flexTime: unsupported src type %T", src)
	}
}
