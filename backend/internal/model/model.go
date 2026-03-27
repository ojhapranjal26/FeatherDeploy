package model

import "time"

// Role constants — used in JWT claims and DB column
const (
	RoleSuperAdmin = "superadmin"
	RoleAdmin      = "admin"
	RoleUser       = "user"
)

// User represents the users table
type User struct {
	ID                int64     `json:"id"`
	Email             string    `json:"email"`
	Name              string    `json:"name"`
	PasswordHash      string    `json:"-"`
	Role              string    `json:"role"` // superadmin | admin | user
	GitHubAccessToken string    `json:"-"`    // never exposed via JSON
	GitHubLogin       string    `json:"github_login,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// Project represents the projects table
type Project struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	OwnerID     int64     `json:"owner_id"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	// Populated on list
	ServiceCount int `json:"service_count,omitempty"`
}

// ProjectMember maps users↔projects with a per-project role
type ProjectMember struct {
	ProjectID int64  `json:"project_id"`
	UserID    int64  `json:"user_id"`
	Role      string `json:"role"` // owner | editor | viewer
}

// Service represents the services table
type Service struct {
	ID           int64     `json:"id"`
	ProjectID    int64     `json:"project_id"`
	Name         string    `json:"name"`
	Description  string    `json:"description"`
	DeployType   string    `json:"deploy_type"` // git | artifact | dockerfile
	RepoURL      string    `json:"repo_url"`
	RepoBranch   string    `json:"repo_branch"`
	RepoFolder   string    `json:"repo_folder"`   // optional subfolder within the repo to deploy from
	Framework    string    `json:"framework"`
	BuildCommand string    `json:"build_command"`
	StartCommand string    `json:"start_command"`
	AppPort      int       `json:"app_port"`
	HostPort     int       `json:"host_port,omitempty"`
	Status       string    `json:"status"` // inactive | deploying | running | error | stopped
	ContainerID  string    `json:"container_id,omitempty"`
	AutoDeploy   bool      `json:"auto_deploy"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Deployment represents the deployments table
type Deployment struct {
	ID           int64      `json:"id"`
	ServiceID    int64      `json:"service_id"`
	TriggeredBy  int64      `json:"triggered_by"`
	DeployType   string     `json:"deploy_type"`
	RepoURL      string     `json:"repo_url,omitempty"`
	CommitSHA    string     `json:"commit_sha,omitempty"`
	Branch       string     `json:"branch,omitempty"`
	ArtifactPath string     `json:"artifact_path,omitempty"`
	Status       string     `json:"status"` // pending | running | success | failed
	ErrorMessage string     `json:"error_message,omitempty"`
	StartedAt    time.Time  `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

// EnvVar represents the env_variables table
type EnvVar struct {
	ID        int64     `json:"id"`
	ServiceID int64     `json:"service_id"`
	Key       string    `json:"key"`
	Value     string    `json:"value,omitempty"` // omitted for secrets in list
	IsSecret  bool      `json:"is_secret"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Domain represents the domains table
type Domain struct {
	ID        int64     `json:"id"`
	ServiceID int64     `json:"service_id"`
	Domain    string    `json:"domain"`
	TLS       bool      `json:"tls"`
	Verified  bool      `json:"verified"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ───── Request / Response DTOs ─────────────────────────────────────────────

type RegisterRequest struct {
	Email    string `json:"email"    validate:"required,email,max=254"`
	Name     string `json:"name"     validate:"required,min=2,max=100"`
	Password string `json:"password" validate:"required,min=8,max=128"`
}

type LoginRequest struct {
	Email    string `json:"email"    validate:"required,email"`
	Password string `json:"password" validate:"required"`
}

type TokenResponse struct {
	Token string `json:"token"`
	User  User   `json:"user"`
}

type CreateProjectRequest struct {
	Name        string `json:"name"        validate:"required,min=2,max=63,slug"`
	Description string `json:"description" validate:"max=256"`
}

type UpdateProjectRequest struct {
	Name        string `json:"name"        validate:"omitempty,min=2,max=63,slug"`
	Description string `json:"description" validate:"max=256"`
}

type CreateServiceRequest struct {
	Name         string `json:"name"          validate:"required,min=2,max=63,slug"`
	Description  string `json:"description"   validate:"max=256"`
	DeployType   string `json:"deploy_type"   validate:"omitempty,oneof=git artifact dockerfile"`
	RepoURL      string `json:"repo_url"      validate:"omitempty,giturl,max=512"`
	RepoBranch   string `json:"repo_branch"   validate:"omitempty,max=255"`
	RepoFolder   string `json:"repo_folder"   validate:"omitempty,max=512"`
	Framework    string `json:"framework"     validate:"max=64"`
	BuildCommand string `json:"build_command" validate:"max=512"`
	StartCommand string `json:"start_command" validate:"max=512"`
	AppPort      int    `json:"app_port"      validate:"omitempty,min=1,max=65535"`
	HostPort     int    `json:"host_port"     validate:"omitempty,min=1,max=65535"`
	Domain       string `json:"domain"        validate:"omitempty,max=253"`
}

type UpdateServiceRequest struct {
	Name         string `json:"name"          validate:"omitempty,min=2,max=63,slug"`
	Description  string `json:"description"   validate:"max=256"`
	DeployType   string `json:"deploy_type"   validate:"omitempty,oneof=git artifact dockerfile"`
	RepoURL      string `json:"repo_url"      validate:"omitempty,giturl,max=512"`
	RepoBranch   string `json:"repo_branch"   validate:"omitempty,max=255"`
	RepoFolder   string `json:"repo_folder"   validate:"omitempty,max=512"`
	Framework    string `json:"framework"     validate:"max=64"`
	BuildCommand string `json:"build_command" validate:"max=512"`
	StartCommand string `json:"start_command" validate:"max=512"`
	AppPort      int    `json:"app_port"      validate:"omitempty,min=1,max=65535"`
	HostPort     int    `json:"host_port"     validate:"omitempty,min=1,max=65535"`
	// AutoDeploy: nil = don't change, true/false = enable/disable auto-deploy
	AutoDeploy *bool `json:"auto_deploy"`
	// ClearRepo: true = disconnect from Git (clears repo_url, repo_branch, repo_folder)
	ClearRepo bool `json:"clear_repo"`
}

type TriggerDeployRequest struct {
	DeployType   string `json:"deploy_type"   validate:"required,oneof=git artifact dockerfile"`
	RepoURL      string `json:"repo_url"      validate:"omitempty,giturl,max=512"`
	RepoBranch   string `json:"repo_branch"   validate:"omitempty,max=255"`
	CommitSHA    string `json:"commit_sha"    validate:"omitempty,hexadecimal,max=64"`
	ArtifactPath string `json:"artifact_path" validate:"omitempty,max=512"`
	Branch       string `json:"branch"        validate:"omitempty,max=255"`
}

type UpsertEnvVarRequest struct {
	Key      string `json:"key"       validate:"required,envkey"`
	Value    string `json:"value"     validate:"required,max=65535"`
	IsSecret bool   `json:"is_secret"`
}

type AddDomainRequest struct {
	Domain string `json:"domain" validate:"required,fqdn,max=255"`
	TLS    bool   `json:"tls"`
}

type AssignProjectMemberRequest struct {
	UserID int64  `json:"user_id" validate:"required,min=1"`
	Role   string `json:"role"    validate:"required,oneof=owner editor viewer"`
}

type UpdateUserRoleRequest struct {
	Role string `json:"role" validate:"required,oneof=superadmin admin user"`
}

// ───── Invitation types ────────────────────────────────────────────────────

// Invitation represents the invitations table
type Invitation struct {
	ID         int64      `json:"id"`
	Email      string     `json:"email"`
	Token      string     `json:"token,omitempty"` // only returned to admin at creation
	Role       string     `json:"role"`
	InvitedBy  int64      `json:"invited_by"`
	ExpiresAt  time.Time  `json:"expires_at"`
	AcceptedAt *time.Time `json:"accepted_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

type CreateInvitationRequest struct {
	Email string `json:"email" validate:"required,email,max=254"`
	Role  string `json:"role"  validate:"required,oneof=superadmin admin user"`
}

type AcceptInvitationRequest struct {
	Name     string `json:"name"     validate:"required,min=2,max=100"`
	Password string `json:"password" validate:"required,min=8,max=128"`
}

// ───── SSH key types ───────────────────────────────────────────────────────

// SSHKey represents the ssh_keys table.
type SSHKey struct {
	ID          int64     `json:"id"`
	UserID      int64     `json:"user_id"`
	Name        string    `json:"name"`
	PublicKey   string    `json:"public_key"`
	Fingerprint string    `json:"fingerprint"`
	HasPrivate  bool      `json:"has_private"` // true when the server generated the key pair
	CreatedAt   time.Time `json:"created_at"`
}

type GenerateSSHKeyRequest struct {
	Name string `json:"name" validate:"required,min=1,max=64"`
}

type ImportSSHKeyRequest struct {
	Name      string `json:"name"       validate:"required,min=1,max=64"`
	PublicKey string `json:"public_key" validate:"required"`
}

// ───── GitHub App types ────────────────────────────────────────────────────

// GitHubAppConfig represents the github_app_config singleton table.
type GitHubAppConfig struct {
	ID             int64     `json:"id"`
	AppID          string    `json:"app_id"`
	AppName        string    `json:"app_name"`
	PrivateKeyPEM  string    `json:"-"` // never serialised
	InstallationID string    `json:"installation_id"`
	WebhookSecret  string    `json:"-"` // never serialised
	ClientID       string    `json:"client_id,omitempty"`
	ClientSecret   string    `json:"-"` // never serialised
	UpdatedAt      time.Time `json:"updated_at"`
}

type SetGitHubAppConfigRequest struct {
	AppID          string `json:"app_id"          validate:"required"`
	AppName        string `json:"app_name"        validate:"required,min=1,max=128"`
	PrivateKeyPEM  string `json:"private_key_pem" validate:"required"` // RSA PEM key from GitHub App settings
	InstallationID string `json:"installation_id" validate:"required"`
	WebhookSecret  string `json:"webhook_secret"`
	ClientID       string `json:"client_id"`
	ClientSecret   string `json:"client_secret"`
}

// ───── GitHub OAuth types ──────────────────────────────────────────────────

type GitHubRepo struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	Description   string `json:"description"`
	Private       bool   `json:"private"`
	HTMLURL       string `json:"html_url"`
	CloneURL      string `json:"clone_url"`
	SSHURL        string `json:"ssh_url"`
	DefaultBranch string `json:"default_branch"`
	Language      string `json:"language"`
	UpdatedAt     string `json:"updated_at"`
}
