// Package rqlitedrv provides a database/sql driver for rqlite, the distributed
// SQLite database.  Import this package for its side effect of registering the
// "rqlite" driver name with database/sql:
//
//	import _ "github.com/ojhapranjal26/featherdeploy/backend/internal/rqlitedrv"
//
// The DSN is an rqlite HTTP base URL, e.g. "http://127.0.0.1:4001".
package rqlitedrv

import (
	"bytes"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func init() {
	sql.Register("rqlite", &rqliteDriver{})
}

// ─── Driver ──────────────────────────────────────────────────────────────────

type rqliteDriver struct{}

func (d *rqliteDriver) Open(dsn string) (driver.Conn, error) {
	baseURL := strings.TrimRight(dsn, "/")
	return &conn{
		baseURL: baseURL,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}, nil
}

// ─── Connection ──────────────────────────────────────────────────────────────

type conn struct {
	baseURL string
	client  *http.Client
}

func (c *conn) Prepare(query string) (driver.Stmt, error) {
	return &stmt{conn: c, query: query}, nil
}

func (c *conn) Close() error { return nil }

func (c *conn) Begin() (driver.Tx, error) {
	return &tx{conn: c}, nil
}

// ─── Statement ───────────────────────────────────────────────────────────────

type stmt struct {
	conn  *conn
	query string
}

func (s *stmt) Close() error  { return nil }
func (s *stmt) NumInput() int { return -1 } // unknown, use positional ?

func (s *stmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.conn.execute(s.query, args)
}

func (s *stmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.conn.query(s.query, args)
}

// ─── Transaction (best-effort; rqlite atomicity is per-statement by default) ─

type tx struct{ conn *conn }

func (t *tx) Commit() error   { return nil }
func (t *tx) Rollback() error { return nil }

// ─── HTTP helpers ─────────────────────────────────────────────────────────────

// isRead returns true for SELECT, PRAGMA, EXPLAIN statements.
func isRead(q string) bool {
	upper := strings.ToUpper(strings.TrimSpace(q))
	return strings.HasPrefix(upper, "SELECT") ||
		strings.HasPrefix(upper, "PRAGMA") ||
		strings.HasPrefix(upper, "EXPLAIN")
}

// rqliteParam converts a driver.Value to a type that marshals cleanly to JSON
// for rqlite parameterized queries.
func rqliteParam(v driver.Value) interface{} {
	if v == nil {
		return nil
	}
	return v
}

// ─── Execute (INSERT / UPDATE / DELETE / CREATE) ─────────────────────────────

type executeRequest []interface{} // ["SQL", param1, param2, ...]

type executeResult struct {
	LastInsertID int64   `json:"last_insert_id"`
	RowsAffected int64   `json:"rows_affected"`
	Error        string  `json:"error"`
	Time         float64 `json:"time"`
}

type executeResponse struct {
	Results []executeResult `json:"results"`
	Error   string          `json:"error"`
}

type writeResult struct {
	lastID   int64
	affected int64
}

func (r writeResult) LastInsertId() (int64, error) { return r.lastID, nil }
func (r writeResult) RowsAffected() (int64, error) { return r.affected, nil }

func (c *conn) execute(query string, args []driver.Value) (driver.Result, error) {
	row := make([]interface{}, 0, 1+len(args))
	row = append(row, query)
	for _, a := range args {
		row = append(row, rqliteParam(a))
	}

	payload := [][]interface{}{row}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("rqlite: marshal execute: %w", err)
	}

	resp, err := c.client.Post(c.baseURL+"/db/execute?timings", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("rqlite: execute POST: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("rqlite: read execute response: %w", err)
	}

	// If the response is not JSON (e.g. "leader not found" plain-text from rqlite
	// before Raft leader election completes), return a descriptive error so the
	// caller can retry rather than seeing a confusing json-parse error.
	if len(data) > 0 && data[0] != '{' && data[0] != '[' {
		snippet := strings.TrimSpace(string(data))
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		return nil, fmt.Errorf("rqlite: unexpected non-JSON response (HTTP %d): %s", resp.StatusCode, snippet)
	}

	var result executeResponse
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("rqlite: unmarshal execute (HTTP %d, body=%q): %w", resp.StatusCode, string(data[:min(200, len(data))]), err)
	}
	if result.Error != "" {
		return nil, fmt.Errorf("rqlite: %s", result.Error)
	}
	if len(result.Results) == 0 {
		return writeResult{}, nil
	}
	r := result.Results[0]
	if r.Error != "" {
		return nil, fmt.Errorf("rqlite: %s", r.Error)
	}
	return writeResult{lastID: r.LastInsertID, affected: r.RowsAffected}, nil
}

// ─── Query (SELECT) ───────────────────────────────────────────────────────────

type queryRequest []interface{} // ["SQL", param1, ...]

type queryResult struct {
	Columns []string        `json:"columns"`
	Types   []string        `json:"types"`
	Values  [][]interface{} `json:"values"`
	Error   string          `json:"error"`
	Time    float64         `json:"time"`
}

type queryResponse struct {
	Results []queryResult `json:"results"`
	Error   string        `json:"error"`
}

type rows struct {
	columns []string
	types   []string
	values  [][]interface{}
	pos     int
}

func (r *rows) Columns() []string { return r.columns }
func (r *rows) Close() error      { return nil }

// datetimeLayouts are the text formats SQLite/rqlite uses for DATETIME columns.
var datetimeLayouts = []string{
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05Z",
	"2006-01-02T15:04:05-07:00",
	"2006-01-02T15:04:05.999999999Z",
	"2006-01-02T15:04:05.999999999-07:00",
	"2006-01-02",
}

func (r *rows) Next(dest []driver.Value) error {
	if r.pos >= len(r.values) {
		return io.EOF
	}
	row := r.values[r.pos]
	r.pos++
	for i, v := range row {
		if i >= len(dest) {
			break
		}
		colType := ""
		if i < len(r.types) {
			colType = strings.ToLower(r.types[i])
		}
		dest[i] = convertValue(v, colType)
	}
	return nil
}

// convertValue maps JSON-decoded values (float64, string, bool, nil) to
// driver.Value types compatible with database/sql's Scan machinery.
// When colType is "datetime", "date", or "timestamp" the string is parsed into
// a time.Time so that Scan into time.Time fields works without errors.
func convertValue(v interface{}, colType string) driver.Value {
	if v == nil {
		return nil
	}
	switch t := v.(type) {
	case float64:
		// rqlite returns all numbers as float64; convert integers to int64 when
		// possible so Scan into int, int64 fields works without conversion.
		if t == float64(int64(t)) {
			return int64(t)
		}
		return t
	case string:
		// Parse datetime strings into time.Time when the column type hints it.
		// Without this, Scan into time.Time fails because database/sql cannot
		// automatically convert a plain string driver value to time.Time.
		if colType == "datetime" || colType == "date" || colType == "timestamp" {
			for _, layout := range datetimeLayouts {
				if ts, err := time.Parse(layout, t); err == nil {
					// Explicitly set UTC location — time.Parse with a timezone-free
					// layout already returns UTC, but .UTC() makes the intent clear
					// and guards against any future layout changes.
					return ts.UTC()
				}
			}
		}
		return t
	case bool:
		if t {
			return int64(1)
		}
		return int64(0)
	default:
		return fmt.Sprintf("%v", v)
	}
}

func (c *conn) query(query string, args []driver.Value) (driver.Rows, error) {
	row := make([]interface{}, 0, 1+len(args))
	row = append(row, query)
	for _, a := range args {
		row = append(row, rqliteParam(a))
	}

	payload := [][]interface{}{row}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("rqlite: marshal query: %w", err)
	}

	resp, err := c.client.Post(c.baseURL+"/db/query?level=strong&timings", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("rqlite: query POST: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("rqlite: read query response: %w", err)
	}

	var result queryResponse
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("rqlite: unmarshal query: %w", err)
	}
	if result.Error != "" {
		return nil, fmt.Errorf("rqlite: %s", result.Error)
	}
	if len(result.Results) == 0 {
		return &rows{}, nil
	}
	r := result.Results[0]
	if r.Error != "" {
		// Map rqlite "no such table" to something database/sql callers recognize.
		if strings.Contains(r.Error, "no such table") || strings.Contains(r.Error, "no rows") {
			return &rows{columns: r.Columns}, nil
		}
		return nil, fmt.Errorf("rqlite: %s", r.Error)
	}
	return &rows{
		columns: r.Columns,
		types:   r.Types,
		values:  r.Values,
	}, nil
}
