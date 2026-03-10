package mailer

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// Config holds SMTP configuration.
// All values can be loaded from environment variables (see .env.example).
type Config struct {
	Host     string // SMTP_HOST (default: localhost)
	Port     int    // SMTP_PORT (default: 1025 for MailHog dev)
	Username string // SMTP_USER
	Password string // SMTP_PASS
	From     string // SMTP_FROM (e.g. "DeployPaaS <noreply@example.com>")
	UseTLS   bool   // SMTP_TLS  ("true" to use STARTTLS)
}

// Mailer sends transactional emails.
// In dev mode (no SMTP_HOST set) it logs the email to stdout instead.
type Mailer struct {
	cfg    Config
	devLog bool // true when no SMTP_HOST configured
}

// New creates a Mailer from cfg.
// If cfg.Host is empty the mailer runs in "dev log" mode: emails are
// printed to stdout rather than sent over the network.
func New(cfg Config) *Mailer {
	devLog := cfg.Host == ""
	if devLog {
		slog.Warn("SMTP_HOST not configured — emails will be logged to stdout only")
	}
	if cfg.From == "" {
		cfg.From = "DeployPaaS <noreply@deploypaaas.local>"
	}
	if cfg.Port == 0 {
		cfg.Port = 1025 // MailHog default
	}
	return &Mailer{cfg: cfg, devLog: devLog}
}

// ─── Public methods ──────────────────────────────────────────────────────────

// SendInvitation emails an invite link to the given address.
// inviteURL should be the full frontend URL, e.g.  https://app.example.com/invite/<token>
func (m *Mailer) SendInvitation(to, inviterName, inviteURL string, expiry time.Duration) error {
	subject := "You've been invited to DeployPaaS"
	body, err := renderTemplate(inviteTmpl, map[string]any{
		"InviterName": inviterName,
		"InviteURL":   inviteURL,
		"ExpiryMins":  int(expiry.Minutes()),
	})
	if err != nil {
		return fmt.Errorf("render invite template: %w", err)
	}
	return m.send(to, subject, body)
}

// ─── Internal ────────────────────────────────────────────────────────────────

func (m *Mailer) send(to, subject, htmlBody string) error {
	msg := buildMessage(m.cfg.From, to, subject, htmlBody)

	if m.devLog {
		slog.Info("--- [MAILER DEV MODE] email would be sent ---",
			"to", to,
			"subject", subject,
			"body_preview", truncate(htmlBody, 200),
		)
		return nil
	}

	addr := net.JoinHostPort(m.cfg.Host, strconv.Itoa(m.cfg.Port))

	var auth smtp.Auth
	if m.cfg.Username != "" {
		auth = smtp.PlainAuth("", m.cfg.Username, m.cfg.Password, m.cfg.Host)
	}

	if m.cfg.UseTLS {
		return m.sendWithTLS(addr, auth, to, msg)
	}
	return smtp.SendMail(addr, auth, extractAddress(m.cfg.From), []string{to}, msg)
}

func (m *Mailer) sendWithTLS(addr string, auth smtp.Auth, to string, msg []byte) error {
	tlsCfg := &tls.Config{ServerName: m.cfg.Host} //nolint:gosec
	conn, err := tls.Dial("tcp", addr, tlsCfg)
	if err != nil {
		return fmt.Errorf("tls dial: %w", err)
	}
	defer conn.Close()

	c, err := smtp.NewClient(conn, m.cfg.Host)
	if err != nil {
		return fmt.Errorf("smtp new client: %w", err)
	}
	defer c.Quit()

	if auth != nil {
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := c.Mail(extractAddress(m.cfg.From)); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	_, err = w.Write(msg)
	if err != nil {
		return err
	}
	return w.Close()
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func buildMessage(from, to, subject, htmlBody string) []byte {
	var b strings.Builder
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/html; charset=\"UTF-8\"\r\n")
	b.WriteString(fmt.Sprintf("From: %s\r\n", from))
	b.WriteString(fmt.Sprintf("To: %s\r\n", to))
	b.WriteString(fmt.Sprintf("Subject: %s\r\n", subject))
	b.WriteString("\r\n")
	b.WriteString(htmlBody)
	return []byte(b.String())
}

func extractAddress(from string) string {
	// Handle "Display Name <addr@host>" format
	if i := strings.Index(from, "<"); i >= 0 {
		return strings.TrimRight(from[i+1:], ">")
	}
	return from
}

func renderTemplate(tmpl string, data map[string]any) (string, error) {
	t, err := template.New("").Parse(tmpl)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ─── Email templates ─────────────────────────────────────────────────────────

const inviteTmpl = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8"/>
<meta name="viewport" content="width=device-width,initial-scale=1"/>
<title>You're invited to DeployPaaS</title>
<style>
  body{margin:0;padding:0;background:#f4f4f5;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif;}
  .wrap{max-width:520px;margin:40px auto;background:#ffffff;border-radius:12px;overflow:hidden;box-shadow:0 2px 8px rgba(0,0,0,.08);}
  .header{background:#4f46e5;padding:36px 40px 28px;}
  .logo{color:#ffffff;font-size:20px;font-weight:700;letter-spacing:-.5px;}
  .body{padding:36px 40px;}
  h1{margin:0 0 12px;font-size:22px;color:#18181b;font-weight:600;}
  p{margin:0 0 16px;font-size:15px;color:#52525b;line-height:1.6;}
  .btn{display:inline-block;padding:13px 28px;background:#4f46e5;color:#ffffff;text-decoration:none;border-radius:8px;font-size:15px;font-weight:600;}
  .note{margin-top:20px;font-size:13px;color:#a1a1aa;}
  .footer{padding:20px 40px;border-top:1px solid #e4e4e7;font-size:12px;color:#a1a1aa;text-align:center;}
</style>
</head>
<body>
<div class="wrap">
  <div class="header">
    <div class="logo">⚡ DeployPaaS</div>
  </div>
  <div class="body">
    <h1>You've been invited!</h1>
    <p><strong>{{.InviterName}}</strong> has invited you to join <strong>DeployPaaS</strong> — a platform for deploying and managing your applications.</p>
    <p>Click the button below to set your password and activate your account:</p>
    <a href="{{.InviteURL}}" class="btn">Accept invitation</a>
    <p class="note">⚠️ This link expires in <strong>{{.ExpiryMins}} minutes</strong>. If you did not expect this invitation, you can safely ignore this email.</p>
  </div>
  <div class="footer">DeployPaaS · If you have trouble clicking the button, copy and paste this URL: {{.InviteURL}}</div>
</div>
</body>
</html>`
