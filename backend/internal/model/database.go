package model

import "time"

const (
	DatabaseTypePostgres = "postgres"
	DatabaseTypeMySQL    = "mysql"
	DatabaseTypeSQLite   = "sqlite"
)

// Database represents a managed database instance within a project.
// Postgres and MySQL run as dedicated containers. SQLite is exposed as a
// managed volume mounted into sibling service containers.
type Database struct {
	ID          int64     `json:"id"`
	ProjectID   int64     `json:"project_id"`
	Name        string    `json:"name"`
	DBType      string    `json:"db_type"`      // postgres|mysql|sqlite
	DBVersion   string    `json:"db_version"`   // image tag e.g. "16", "8.4", "latest"
	DBName      string    `json:"db_name"`      // database/schema name inside the engine
	DBUser      string    `json:"db_user"`      // database user
	HostPort    int       `json:"host_port,omitempty"`  // >0 only for public network databases
	Status      string    `json:"status"`               // stopped|starting|running|error
	ContainerID string    `json:"container_id,omitempty"`
	NetworkPublic bool    `json:"network_public"` // true = also bound to a host port
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`

	// Populated by GET — not persisted in the DB.
	// ConnectionURL is the internal URL or file path made available to sibling
	// service containers in the same project.
	ConnectionURL       string `json:"connection_url,omitempty"`
	// PublicConnectionURL uses the host IP + host_port; valid from outside the project network.
	PublicConnectionURL string `json:"public_connection_url,omitempty"`
	// EnvVarName is the environment variable automatically injected into sibling
	// service containers, e.g. "MY_DB_URL".
	EnvVarName string `json:"env_var_name,omitempty"`
}

// CreateDatabaseRequest is the body for POST /api/projects/{id}/databases.
type CreateDatabaseRequest struct {
	Name          string `json:"name"          validate:"required,min=2,max=63,slug"`
	DBType        string `json:"db_type"       validate:"required,oneof=postgres mysql sqlite"`
	DBVersion     string `json:"db_version"    validate:"omitempty,max=32"`
	DBName        string `json:"db_name"       validate:"omitempty,max=64"`
	DBUser        string `json:"db_user"       validate:"omitempty,max=64"`
	DBPassword    string `json:"db_password"   validate:"omitempty,max=256"`
	NetworkPublic bool   `json:"network_public"`
}
