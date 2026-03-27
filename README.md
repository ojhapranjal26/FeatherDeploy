<div align="center">

# 🪶 FeatherDeploy

**A self-hosted, lightweight PaaS panel for deploying containerised applications via Podman.**

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.22+-00ADD8?logo=go)](https://go.dev)
[![React](https://img.shields.io/badge/React-18-61DAFB?logo=react)](https://react.dev)

</div>

---

## ✨ Features

- **Single binary** — frontend (React + Vite) is embedded directly into the Go binary via `go:embed`. No separate web server needed.
- **Invite-only registration** — no public sign-up. Superadmins send email invitations.
- **Full RBAC** — global roles (`superadmin`, `admin`, `user`) and per-project roles (`owner`, `editor`, `viewer`).
- **GitHub integrations**
  - OAuth App — connect a personal GitHub account
  - GitHub App — organisation-wide repo access via RS256 JWT installation tokens
  - SSH Keys — generate or import ED25519 deploy keys (private keys stored AES-256-GCM encrypted)
  - 📖 [Full GitHub setup guide →](docs/github-setup.md)
- **Podman containers** — deploy services as rootful OCI containers (via `sudo -n podman`) for maximum kernel compatibility on low-cost VPS hosts.
- **Automatic TLS** — Caddy reverse proxy obtains Let's Encrypt certificates for your domain.
- **Non-root service user** — the panel process runs under a dedicated Linux user (not root) for security isolation.
- **Systemd managed** — installs as a persistent systemd service with automatic restarts.

---

## 🖥️ Tech Stack

| Layer     | Technology                                                                |
|-----------|---------------------------------------------------------------------------|
| Frontend  | React 19, TypeScript, Vite 7, Tailwind CSS v4, shadcn/ui, TanStack Query v5, recharts, react-hook-form, zod, lucide-react |
| Backend   | Go 1.26, chi v5 router, JWT (HS256 + RS256), bcrypt                       |
| Database  | rqlite v8 (distributed SQLite — no CGO, no external DB binary required)   |
| Container | Podman (rootful OCI — builds and runs containers via `sudo -n podman`)    |
| Proxy     | Caddy 2 (automatic HTTPS via Let's Encrypt)                               |
| Infra     | systemd, rqlite                                                           |

---

## 🚀 One-Command Installation (Linux)

> **Requirements:** A fresh Linux server (Ubuntu 22.04+ recommended) with a public IP and a domain name pointed at it. Run as root or with `sudo`.

```bash
curl -fsSL https://raw.githubusercontent.com/ojhapranjal26/FeatherDeploy/main/build.sh | sudo bash
```

That single command will:

1. **Install all dependencies automatically:**
   - `git`, `curl`, `ca-certificates`
   - **Node.js 20** (via NodeSource)
   - **Go 1.26** (via official tarball if not already installed)
   - **Podman** (container runtime, used in rootful mode via `sudo`)
   - **rqlite v8** (embedded distributed SQLite — no CGO or libsqlite3 needed)
   - **Caddy** (reverse proxy + automatic TLS)
2. **Clone** the FeatherDeploy source from GitHub into `/opt/featherdeploy-src`
3. **Build** the React frontend (`npm ci && npm run build`)
4. **Compile** the Go backend with the frontend embedded inside (single binary)
5. **Install** the binary to `/usr/local/bin/featherdeploy`
6. **Launch the interactive setup wizard** which asks:

```
Service OS username [featherdeploy]: myuser         ← Linux user that runs the service (not root)
Password for OS user 'myuser' (min 8 chars):        ← OS user password
Confirm OS user password:

Panel domain (e.g. panel.example.com): panel.featherdeploy.in

Superadmin email: admin@featherdeploy.in
Superadmin full name: Pranjal Ojha
Superadmin password (min 8 chars):
Confirm superadmin password:
```

After the wizard completes, FeatherDeploy will be:
- Running at `https://panel.featherdeploy.in` with automatic TLS
- Managed by systemd (`featherdeploy.service`)
- Running as your chosen non-root OS user

---

## 🔧 Requirements

| Requirement | Notes |
|---|---|
| Linux (x86_64) | Ubuntu 22.04+, Debian 12+, Fedora 38+, CentOS Stream 9, Arch, Alpine |
| Root / sudo | Required only for installation |
| Domain name | Must resolve to the server's public IP before install |
| Ports 80 + 443 | Used by Caddy for HTTP→HTTPS redirect and TLS |
| Internet access | To download Node.js, Go, Podman, Caddy, and Let's Encrypt certificates |

---

## 📋 Post-Installation

### Check service status
```bash
sudo systemctl status featherdeploy
sudo systemctl status caddy
```

### View live logs
```bash
sudo journalctl -u featherdeploy -f
```

### Switch to the service user
```bash
sudo -u featherdeploy /bin/bash    # the account has no login shell — use sudo -u
```

### Restart the panel
```bash
sudo systemctl restart featherdeploy
```

### Environment configuration

The installer writes `/etc/featherdeploy/featherdeploy.env`. This file is readable only by root and the service group. Use `sudo` to view or edit it:

```bash
sudo nano /etc/featherdeploy/featherdeploy.env
sudo systemctl restart featherdeploy   # apply changes
```

Optional settings you can add:

```env
# /etc/featherdeploy/featherdeploy.env

DB_PATH=/var/lib/featherdeploy/deploy.db
JWT_SECRET=<auto-generated — do not change>
ADDR=127.0.0.1:8080
ORIGIN=https://panel.featherdeploy.in

# SMTP (optional — leave blank to log invite links to the console instead)
SMTP_HOST=smtp.example.com
SMTP_PORT=587
SMTP_USER=user@example.com
SMTP_PASS=yourpassword
SMTP_FROM=noreply@example.com
SMTP_TLS=true

# GitHub OAuth (optional)
GITHUB_CLIENT_ID=
GITHUB_CLIENT_SECRET=
```

After editing, restart the service: `sudo systemctl restart featherdeploy`

---

## 🔒 Security Model

The panel process **never runs as root**. The installer creates a dedicated Linux user (default: `featherdeploy`) with no direct sudo access.
- The only elevated permission granted is a locked-down sudoers rule: `NOPASSWD: /usr/bin/podman` (rootful containers) and `NOPASSWD: /usr/local/bin/featherdeploy-update` (one-click updates). Nothing else.
- The binary is owned by `root` and executable, but all data (`/var/lib/featherdeploy/`) is owned by the service user only.
- The env file (`/etc/featherdeploy/featherdeploy.env`) containing the JWT secret has mode `640` (readable only by root and the service group).
- The systemd unit sets `PrivateTmp=yes`. `NoNewPrivileges` is intentionally **not** set because the service must invoke `sudo -n podman` for container builds and runs.
- Passwords are hashed with **bcrypt** (cost 14). SSH private keys are encrypted with **AES-256-GCM** before storage.
- HTTPS is enforced by Caddy with automatic certificate renewal. Security headers (`HSTS`, `X-Frame-Options`, `X-Content-Type-Options`) are set on all responses.

---

## 🏗️ Development Setup

### Clone the repository
```bash
git clone https://github.com/ojhapranjal26/FeatherDeploy.git
cd FeatherDeploy
```

### Run the backend (Go)
```bash
cd backend
cp .env.example .env          # edit as needed
go run ./cmd/server/
```

### Run the frontend (Vite dev server)
```bash
cd frontend
npm install
npm run dev
```

The frontend dev server starts at `http://localhost:5173` and proxies API calls to the backend at `:8080`.

### Build the production binary (requires WSL2 or Linux)
```bash
# From WSL2 on Windows:
powershell ./build.ps1

# From Linux / macOS:
sudo bash build.sh
```

---

## 📁 Project Structure

```
FeatherDeploy/
├── build.sh                    # One-shot Linux install / update script
├── build.ps1                   # Windows wrapper (runs build.sh via WSL2)
├── VERSION.json                # Version manifest — checked by the dashboard update banner
├── frontend/                   # React 19 + Vite + TypeScript frontend
│   └── src/
│       ├── pages/              # Page components
│       ├── components/         # UI + layout components (shadcn/ui)
│       ├── context/            # Auth + Theme contexts
│       ├── hooks/              # SSE hooks (stats, deployment logs)
│       └── api/                # Typed API client helpers (axios)
└── backend/
    ├── cmd/
    │   ├── server/main.go      # Entry point (serve / install / update subcommands)
    │   └── node/main.go        # Worker-node binary entry point
    ├── web/                    # go:embed target for built frontend assets
    ├── internal/
    │   ├── auth/               # bcrypt + JWT helpers
    │   ├── crypto/             # AES-256-GCM encrypt/decrypt
    │   ├── db/                 # rqlite open + schema migrations
    │   ├── deploy/             # Deployment queue, runner, Dockerfile generator
    │   ├── detect/             # Framework + language auto-detection
    │   ├── handler/            # HTTP handlers (auth, projects, github, ssh, system…)
    │   ├── heartbeat/          # Brain/node heartbeat + cluster state
    │   ├── installer/          # Interactive Linux setup wizard + update logic
    │   ├── mailer/             # SMTP email sender
    │   ├── middleware/         # JWT auth + RBAC middleware
    │   ├── model/              # Shared data types + DTOs
    │   ├── pki/                # Internal CA, TLS cert generation for nodes
    │   ├── rqlitedrv/          # rqlite database/sql driver
    │   └── validator/          # Input validation
    └── migrations/
        └── schema.sql          # Full database schema
```

---

## 🤝 Contributing

Pull requests are welcome. For major changes, please open an issue first.

---

## 📄 License

[MIT](LICENSE)
