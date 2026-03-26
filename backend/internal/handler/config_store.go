package handler

import (
	"context"
	"database/sql"
	"strconv"

	"github.com/ojhapranjal26/featherdeploy/backend/internal/crypto"
	"github.com/ojhapranjal26/featherdeploy/backend/internal/mailer"
)

// ConfigStore loads and persists AES-256-GCM encrypted application settings
// (SMTP credentials, GitHub OAuth credentials) from the app_settings table.
//
// Env-var defaults are kept as a fallback so existing deployments continue
// working without migrating to DB-based config immediately.
type ConfigStore struct {
	db         *sql.DB
	passphrase string // AES-256-GCM passphrase derived from JWT secret

	// Fallback defaults loaded from environment variables at startup
	defaultSMTP       mailer.Config
	defaultGHClientID string
	defaultGHClientSecret string
}

func NewConfigStore(
	db *sql.DB,
	passphrase string,
	defaultSMTP mailer.Config,
	defaultGHClientID, defaultGHClientSecret string,
) *ConfigStore {
	return &ConfigStore{
		db:                    db,
		passphrase:            passphrase,
		defaultSMTP:           defaultSMTP,
		defaultGHClientID:     defaultGHClientID,
		defaultGHClientSecret: defaultGHClientSecret,
	}
}

// ─── Low-level primitives ────────────────────────────────────────────────────

// get decrypts a single key from app_settings.
// Returns ("", false, nil) when the key doesn't exist.
func (s *ConfigStore) get(ctx context.Context, key string) (string, bool, error) {
	var enc string
	err := s.db.QueryRowContext(ctx,
		`SELECT enc_value FROM app_settings WHERE key = ?`, key,
	).Scan(&enc)
	if err == sql.ErrNoRows || enc == "" {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	plain, err := crypto.Decrypt(enc, s.passphrase)
	if err != nil {
		return "", false, err
	}
	return plain, true, nil
}

// Set encrypts and upserts a value. Use Delete to remove a key.
func (s *ConfigStore) Set(ctx context.Context, key, value string) error {
	enc, err := crypto.Encrypt(value, s.passphrase)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO app_settings(key, enc_value, updated_at) VALUES(?, ?, datetime('now'))
		ON CONFLICT(key) DO UPDATE SET enc_value=excluded.enc_value, updated_at=excluded.updated_at`,
		key, enc,
	)
	return err
}

// Delete removes a key from app_settings (no-op if absent).
func (s *ConfigStore) Delete(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM app_settings WHERE key = ?`, key)
	return err
}

// IsSet returns true when key has a stored non-empty value.
func (s *ConfigStore) IsSet(ctx context.Context, key string) bool {
	_, found, _ := s.get(ctx, key)
	return found
}

// ─── SMTP ────────────────────────────────────────────────────────────────────

// SMTPConfig returns the active SMTP configuration.
// DB-stored values take precedence over env-var defaults.
func (s *ConfigStore) SMTPConfig(ctx context.Context) mailer.Config {
	cfg := s.defaultSMTP
	if v, ok, _ := s.get(ctx, "smtp_host"); ok {
		cfg.Host = v
	}
	if v, ok, _ := s.get(ctx, "smtp_port"); ok {
		if p, _ := strconv.Atoi(v); p > 0 {
			cfg.Port = p
		}
	}
	if v, ok, _ := s.get(ctx, "smtp_user"); ok {
		cfg.Username = v
	}
	if v, ok, _ := s.get(ctx, "smtp_pass"); ok {
		cfg.Password = v
	}
	if v, ok, _ := s.get(ctx, "smtp_from"); ok {
		cfg.From = v
	}
	if v, ok, _ := s.get(ctx, "smtp_tls"); ok {
		cfg.UseTLS = v == "true"
	}
	return cfg
}

// SetSMTP encrypts and stores all SMTP fields. Empty string means "clear that key".
func (s *ConfigStore) SetSMTP(ctx context.Context, host, port, user, pass, from, tls string) error {
	pairs := [][2]string{
		{"smtp_host", host},
		{"smtp_port", port},
		{"smtp_user", user},
		{"smtp_pass", pass},
		{"smtp_from", from},
		{"smtp_tls", tls},
	}
	for _, p := range pairs {
		if p[1] == "" {
			if err := s.Delete(ctx, p[0]); err != nil {
				return err
			}
		} else {
			if err := s.Set(ctx, p[0], p[1]); err != nil {
				return err
			}
		}
	}
	return nil
}

// DeleteSMTP removes all SMTP keys from app_settings.
func (s *ConfigStore) DeleteSMTP(ctx context.Context) error {
	for _, k := range []string{"smtp_host", "smtp_port", "smtp_user", "smtp_pass", "smtp_from", "smtp_tls"} {
		if err := s.Delete(ctx, k); err != nil {
			return err
		}
	}
	return nil
}

// SMTPStatus returns display-safe fields (host/port/from/tls visible; user/pass as bool).
func (s *ConfigStore) SMTPStatus(ctx context.Context) map[string]any {
	host, _, _ := s.get(ctx, "smtp_host")
	port, _, _ := s.get(ctx, "smtp_port")
	from, _, _ := s.get(ctx, "smtp_from")
	tls, _, _ := s.get(ctx, "smtp_tls")
	userSet := s.IsSet(ctx, "smtp_user")
	passSet := s.IsSet(ctx, "smtp_pass")

	// Fall back to env-var defaults for display
	if host == "" {
		host = s.defaultSMTP.Host
	}
	if port == "" && s.defaultSMTP.Port != 0 {
		port = strconv.Itoa(s.defaultSMTP.Port)
	}
	if from == "" {
		from = s.defaultSMTP.From
	}
	if tls == "" {
		if s.defaultSMTP.UseTLS {
			tls = "true"
		} else {
			tls = "false"
		}
	}
	if !userSet && s.defaultSMTP.Username != "" {
		userSet = true
	}
	if !passSet && s.defaultSMTP.Password != "" {
		passSet = true
	}

	configured := host != "" || userSet
	return map[string]any{
		"configured":   configured,
		"host":         host,
		"port":         port,
		"from":         from,
		"tls":          tls,
		"username_set": userSet,
		"password_set": passSet,
	}
}

// ─── GitHub OAuth ────────────────────────────────────────────────────────────

// GitHubOAuth returns the active GitHub OAuth client ID and secret.
// DB-stored values take precedence over env-var defaults.
func (s *ConfigStore) GitHubOAuth(ctx context.Context) (clientID, clientSecret string) {
	clientID, clientSecret = s.defaultGHClientID, s.defaultGHClientSecret
	if v, ok, _ := s.get(ctx, "github_client_id"); ok {
		clientID = v
	}
	if v, ok, _ := s.get(ctx, "github_client_secret"); ok {
		clientSecret = v
	}
	return
}

// SetGitHubOAuth encrypts and stores the OAuth client credentials.
func (s *ConfigStore) SetGitHubOAuth(ctx context.Context, clientID, clientSecret string) error {
	for _, p := range [][2]string{{"github_client_id", clientID}, {"github_client_secret", clientSecret}} {
		if p[1] == "" {
			if err := s.Delete(ctx, p[0]); err != nil {
				return err
			}
		} else {
			if err := s.Set(ctx, p[0], p[1]); err != nil {
				return err
			}
		}
	}
	return nil
}

// DeleteGitHubOAuth removes both GitHub OAuth keys.
func (s *ConfigStore) DeleteGitHubOAuth(ctx context.Context) error {
	for _, k := range []string{"github_client_id", "github_client_secret"} {
		if err := s.Delete(ctx, k); err != nil {
			return err
		}
	}
	return nil
}

// GitHubOAuthStatus returns display-safe status (client_id shown; secret as bool).
func (s *ConfigStore) GitHubOAuthStatus(ctx context.Context) map[string]any {
	clientID, _, _ := s.get(ctx, "github_client_id")
	secretSet := s.IsSet(ctx, "github_client_secret")

	if clientID == "" {
		clientID = s.defaultGHClientID
	}
	if !secretSet && s.defaultGHClientSecret != "" {
		secretSet = true
	}
	return map[string]any{
		"configured":        clientID != "" && secretSet,
		"client_id":         clientID,
		"client_secret_set": secretSet,
	}
}
