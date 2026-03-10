#!/usr/bin/env bash
# =============================================================================
#  FeatherDeploy — build + install / update script
#  Usage (first install OR update):
#    curl -fsSL https://raw.githubusercontent.com/ojhapranjal26/FeatherDeploy/main/build.sh | sudo bash
#  or clone the repo and run:
#    sudo bash build.sh
#
#  Modes:
#   • First install  — full interactive wizard (domain, superadmin, systemd, Caddy)
#   • Update         — rebuild binary + frontend, run DB migrations, restart service
#                      (all existing data kept)
#   • Reinstall      — stop service, wipe the database, run the full wizard again
# =============================================================================
set -euo pipefail

REPO_URL="https://github.com/ojhapranjal26/FeatherDeploy.git"
INSTALL_DIR="/opt/featherdeploy-src"
BINARY="/usr/local/bin/featherdeploy"
ENV_FILE="/etc/featherdeploy/featherdeploy.env"
SYSTEMD_UNIT="/etc/systemd/system/featherdeploy.service"
DATA_DB="/var/lib/featherdeploy/deploy.db"

# ── 0. Must run as root ───────────────────────────────────────────────────────
if [ "$(id -u)" -ne 0 ]; then
  echo "ERROR: This script must be run as root (use sudo)." >&2
  exit 1
fi

# ── Detect prior installation ─────────────────────────────────────────────────
is_installed() {
  [ -f "$ENV_FILE" ] || [ -f "$SYSTEMD_UNIT" ]
}

print_header() {
  echo ""
  echo "  ╔══════════════════════════════════════════════════════╗"
  echo "  ║          FeatherDeploy  —  Setup & Updater           ║"
  echo "  ╚══════════════════════════════════════════════════════╝"
  echo ""
}

print_header

# ── Ask mode only when already installed ──────────────────────────────────────
MODE="install"

if is_installed; then
  echo "  An existing FeatherDeploy installation was detected."
  echo ""
  echo "  What would you like to do?"
  echo ""
  echo "    [U]  Update    — rebuild binary + frontend, apply DB migrations,"
  echo "                     restart service  (all your data is preserved)"
  echo "    [R]  Reinstall — wipe the database and run the full setup wizard"
  echo "                     again  (ALL DATA WILL BE DELETED)"
  echo ""
  printf "  Your choice [U/r]: "
  read -r user_choice </dev/tty
  user_choice=$(printf '%s' "$user_choice" | tr '[:upper:]' '[:lower:]' | tr -d '[:space:]')

  if [ "$user_choice" = "r" ]; then
    echo ""
    printf "  ⚠  This will permanently delete the database.  Type YES to confirm: "
    read -r confirm </dev/tty
    if [ "$confirm" != "YES" ]; then
      echo "  Aborted."
      exit 0
    fi
    MODE="reinstall"
  else
    MODE="update"
  fi
fi

echo ""
echo "  Mode: $MODE"
echo ""

# ── 1. Install build deps ─────────────────────────────────────────────────────
install_deps_apt() {
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -y
  apt-get install -y \
    curl git gcc make ca-certificates \
    libsqlite3-dev build-essential

  # Podman
  if ! command -v podman >/dev/null 2>&1; then
    echo "==> Installing Podman..."
    apt-get install -y podman 2>/dev/null || \
      apt-get install -y podman-docker 2>/dev/null || \
      echo "  WARNING: podman not found in apt — install manually if needed"
  else
    echo "  Podman already installed — skipping"
  fi

  # Caddy
  if ! command -v caddy >/dev/null 2>&1; then
    echo "==> Installing Caddy..."
    apt-get install -y debian-keyring debian-archive-keyring apt-transport-https curl 2>/dev/null || true
    curl -fsSL 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | \
      gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg 2>/dev/null || true
    curl -fsSL 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' | \
      tee /etc/apt/sources.list.d/caddy-stable.list >/dev/null 2>&1 || true
    apt-get update -y 2>/dev/null || true
    apt-get install -y caddy
  else
    echo "  Caddy already installed — skipping"
  fi

  # Node.js 20 via NodeSource
  if ! command -v node >/dev/null 2>&1 || [ "$(node --version | cut -d. -f1 | tr -d v)" -lt 18 ]; then
    echo "==> Installing Node.js 20..."
    curl -fsSL https://deb.nodesource.com/setup_20.x | bash -
    apt-get install -y nodejs
  fi

  install_go_tarball
}

install_deps_dnf() {
  dnf install -y curl git gcc make ca-certificates sqlite-devel
  command -v podman >/dev/null 2>&1 || dnf install -y podman
  command -v caddy  >/dev/null 2>&1 || dnf install -y caddy 2>/dev/null || \
    (dnf copr enable -y @caddy/caddy 2>/dev/null && dnf install -y caddy) || \
    echo "  WARNING: caddy not available via dnf — install manually"
  if ! command -v node >/dev/null 2>&1; then
    dnf module enable -y nodejs:20 2>/dev/null || true
    dnf install -y nodejs npm
  fi
  install_go_tarball
}

install_deps_yum() {
  yum install -y curl git gcc make ca-certificates sqlite-devel
  command -v podman >/dev/null 2>&1 || yum install -y podman
  command -v caddy  >/dev/null 2>&1 || (yum install -y yum-plugin-copr && yum copr enable -y @caddy/caddy && yum install -y caddy) || \
    echo "  WARNING: caddy not available via yum — install manually"
  if ! command -v node >/dev/null 2>&1; then
    curl -fsSL https://rpm.nodesource.com/setup_20.x | bash -
    yum install -y nodejs
  fi
  install_go_tarball
}

install_deps_apk() {
  apk update
  apk add --no-cache curl git gcc musl-dev make nodejs npm sqlite-dev podman caddy
  install_go_tarball
}

install_deps_pacman() {
  pacman -Sy --noconfirm curl git gcc make nodejs npm go sqlite podman caddy
}

install_go_tarball() {
  local need_go=false
  if ! command -v go >/dev/null 2>&1; then
    need_go=true
  else
    local ver
    ver=$(go version | awk '{print $3}' | tr -d 'go')
    local major minor
    major=$(echo "$ver" | cut -d. -f1)
    minor=$(echo "$ver" | cut -d. -f2)
    if [ "$major" -lt 1 ] || { [ "$major" -eq 1 ] && [ "$minor" -lt 21 ]; }; then
      need_go=true
    fi
  fi

  if $need_go; then
    echo "==> Installing Go 1.22..."
    local GO_VER="1.22.4"
    local GO_TAR="go${GO_VER}.linux-amd64.tar.gz"
    curl -fsSL "https://dl.google.com/go/${GO_TAR}" -o "/tmp/${GO_TAR}"
    rm -rf /usr/local/go
    tar -C /usr/local -xzf "/tmp/${GO_TAR}"
    rm "/tmp/${GO_TAR}"
    export PATH="/usr/local/go/bin:$PATH"
    echo 'export PATH="/usr/local/go/bin:$PATH"' >> /etc/profile.d/go.sh
    echo "    Go $(go version) installed"
  else
    echo "  Go $(go version) already installed — skipping"
  fi
}

echo "==> Checking build dependencies..."
if command -v apt-get >/dev/null 2>&1; then
  echo "==> Package manager: apt-get (Debian/Ubuntu)"
  install_deps_apt
elif command -v dnf >/dev/null 2>&1; then
  echo "==> Package manager: dnf (Fedora/RHEL)"
  install_deps_dnf
elif command -v yum >/dev/null 2>&1; then
  echo "==> Package manager: yum (CentOS/Amazon Linux)"
  install_deps_yum
elif command -v apk >/dev/null 2>&1; then
  echo "==> Package manager: apk (Alpine)"
  install_deps_apk
elif command -v pacman >/dev/null 2>&1; then
  echo "==> Package manager: pacman (Arch)"
  install_deps_pacman
else
  echo "WARNING: No supported package manager found."
  echo "Please install git, curl, gcc, Node.js 20, and Go 1.22+ manually, then re-run."
fi

export PATH="/usr/local/go/bin:$PATH"

# ── 2. Clone or update source ─────────────────────────────────────────────────
echo ""
echo "==> Fetching FeatherDeploy source..."
if [ -d "$INSTALL_DIR/.git" ]; then
  git -C "$INSTALL_DIR" fetch origin
  git -C "$INSTALL_DIR" reset --hard origin/main
else
  git clone "$REPO_URL" "$INSTALL_DIR"
fi
REPO="$INSTALL_DIR"

# ── 3. Build frontend ─────────────────────────────────────────────────────────
echo ""
echo "==> Building frontend..."
cd "$REPO/frontend"
npm ci --prefer-offline
npm run build

# ── 4. Copy frontend dist into Go embed directory ────────────────────────────
echo ""
echo "==> Embedding frontend into backend..."
mkdir -p "$REPO/backend/web/dist"
rm -rf "$REPO/backend/web/dist/"*
cp -r "$REPO/frontend/dist/." "$REPO/backend/web/dist/"

# ── 5. Build Go binary ────────────────────────────────────────────────────────
echo ""
echo "==> Building FeatherDeploy binary..."
cd "$REPO/backend"
mkdir -p "$REPO/dist"
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 \
  go build \
    -ldflags="-s -w" \
    -o "$REPO/dist/featherdeploy" \
    ./cmd/server/

# ── 6. Stop service before replacing binary (update / reinstall only) ─────────
if [ "$MODE" != "install" ]; then
  echo ""
  echo "==> Stopping featherdeploy service..."
  systemctl stop featherdeploy 2>/dev/null || true
fi

# ── 7. Install new binary ─────────────────────────────────────────────────────
cp "$REPO/dist/featherdeploy" "$BINARY"
chmod +x "$BINARY"
echo "  Binary installed to: $BINARY"

# ── 8. Reinstall: wipe DB then run the full wizard ────────────────────────────
if [ "$MODE" = "reinstall" ]; then
  echo ""
  echo "==> Removing existing database..."
  rm -f "$DATA_DB"
  echo "  Database removed."
  echo ""
  echo "==> Launching FeatherDeploy setup wizard..."
  echo ""
  exec "$BINARY" install

# ── 9. Update: apply migrations and restart ───────────────────────────────────
elif [ "$MODE" = "update" ]; then
  echo ""
  echo "==> Updating FeatherDeploy..."
  exec "$BINARY" update

# ── 10. First install: run the full wizard ────────────────────────────────────
else
  echo ""
  echo "==> Launching FeatherDeploy setup wizard..."
  echo ""
  exec "$BINARY" install
fi


# ── 0. Must run as root ───────────────────────────────────────────────────────
if [ "$(id -u)" -ne 0 ]; then
  echo "ERROR: This script must be run as root (use sudo)." >&2
  exit 1
fi

echo ""
echo "  FeatherDeploy installer — setting up dependencies..."
echo ""

# ── 1. Detect package manager and install build deps ─────────────────────────
install_deps_apt() {
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -y
  apt-get install -y \
    curl git gcc make ca-certificates \
    libsqlite3-dev build-essential

  # Podman
  if ! command -v podman >/dev/null 2>&1; then
    echo "==> Installing Podman..."
    apt-get install -y podman 2>/dev/null || \
      apt-get install -y podman-docker 2>/dev/null || \
      echo "  WARNING: podman not found in apt — install manually if needed"
  else
    echo "  Podman already installed — skipping"
  fi

  # Caddy
  if ! command -v caddy >/dev/null 2>&1; then
    echo "==> Installing Caddy..."
    apt-get install -y debian-keyring debian-archive-keyring apt-transport-https curl 2>/dev/null || true
    curl -fsSL 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | \
      gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg 2>/dev/null || true
    curl -fsSL 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' | \
      tee /etc/apt/sources.list.d/caddy-stable.list >/dev/null 2>&1 || true
    apt-get update -y 2>/dev/null || true
    apt-get install -y caddy
  else
    echo "  Caddy already installed — skipping"
  fi

  # Node.js 20 via NodeSource
  if ! command -v node >/dev/null 2>&1 || [ "$(node --version | cut -d. -f1 | tr -d v)" -lt 18 ]; then
    echo "==> Installing Node.js 20..."
    curl -fsSL https://deb.nodesource.com/setup_20.x | bash -
    apt-get install -y nodejs
  fi

  install_go_tarball
}

install_deps_dnf() {
  dnf install -y curl git gcc make ca-certificates sqlite-devel
  command -v podman >/dev/null 2>&1 || dnf install -y podman
  command -v caddy  >/dev/null 2>&1 || dnf install -y caddy 2>/dev/null || \
    (dnf copr enable -y @caddy/caddy 2>/dev/null && dnf install -y caddy) || \
    echo "  WARNING: caddy not available via dnf — install manually"
  if ! command -v node >/dev/null 2>&1; then
    dnf module enable -y nodejs:20 2>/dev/null || true
    dnf install -y nodejs npm
  fi
  install_go_tarball
}

install_deps_yum() {
  yum install -y curl git gcc make ca-certificates sqlite-devel
  command -v podman >/dev/null 2>&1 || yum install -y podman
  command -v caddy  >/dev/null 2>&1 || (yum install -y yum-plugin-copr && yum copr enable -y @caddy/caddy && yum install -y caddy) || \
    echo "  WARNING: caddy not available via yum — install manually"
  if ! command -v node >/dev/null 2>&1; then
    curl -fsSL https://rpm.nodesource.com/setup_20.x | bash -
    yum install -y nodejs
  fi
  install_go_tarball
}

install_deps_apk() {
  apk update
  apk add --no-cache curl git gcc musl-dev make nodejs npm sqlite-dev podman caddy
  install_go_tarball
}

install_deps_pacman() {
  pacman -Sy --noconfirm curl git gcc make nodejs npm go sqlite podman caddy
}

install_go_tarball() {
  # Install Go 1.22 if not present or version < 1.21
  local need_go=false
  if ! command -v go >/dev/null 2>&1; then
    need_go=true
  else
    local ver
    ver=$(go version | awk '{print $3}' | tr -d 'go')
    local major minor
    major=$(echo "$ver" | cut -d. -f1)
    minor=$(echo "$ver" | cut -d. -f2)
    if [ "$major" -lt 1 ] || { [ "$major" -eq 1 ] && [ "$minor" -lt 21 ]; }; then
      need_go=true
    fi
  fi

  if $need_go; then
    echo "==> Installing Go 1.22..."
    local GO_VER="1.22.4"
    local GO_TAR="go${GO_VER}.linux-amd64.tar.gz"
    curl -fsSL "https://dl.google.com/go/${GO_TAR}" -o "/tmp/${GO_TAR}"
    rm -rf /usr/local/go
    tar -C /usr/local -xzf "/tmp/${GO_TAR}"
    rm "/tmp/${GO_TAR}"
    # Make go available in PATH for this session
    export PATH="/usr/local/go/bin:$PATH"
    echo 'export PATH="/usr/local/go/bin:$PATH"' >> /etc/profile.d/go.sh
    echo "    Go $(go version) installed"
  else
    echo "  Go $(go version) already installed — skipping"
  fi
}

if command -v apt-get >/dev/null 2>&1; then
  echo "==> Package manager: apt-get (Debian/Ubuntu)"
  install_deps_apt
elif command -v dnf >/dev/null 2>&1; then
  echo "==> Package manager: dnf (Fedora/RHEL)"
  install_deps_dnf
elif command -v yum >/dev/null 2>&1; then
  echo "==> Package manager: yum (CentOS/Amazon Linux)"
  install_deps_yum
elif command -v apk >/dev/null 2>&1; then
  echo "==> Package manager: apk (Alpine)"
  install_deps_apk
elif command -v pacman >/dev/null 2>&1; then
  echo "==> Package manager: pacman (Arch)"
  install_deps_pacman
else
  echo "WARNING: No supported package manager found."
  echo "Please install git, curl, gcc, Node.js 20, and Go 1.22+ manually, then re-run."
fi

# Ensure go is on PATH (may have been installed above)
export PATH="/usr/local/go/bin:$PATH"

# ── 2. Clone or update source ─────────────────────────────────────────────────
echo ""
echo "==> Fetching FeatherDeploy source..."
if [ -d "$INSTALL_DIR/.git" ]; then
  git -C "$INSTALL_DIR" pull --ff-only
else
  git clone "$REPO_URL" "$INSTALL_DIR"
fi
REPO="$INSTALL_DIR"

# ── 3. Build frontend ─────────────────────────────────────────────────────────
echo ""
echo "==> Building frontend..."
cd "$REPO/frontend"
npm ci --prefer-offline
npm run build

# ── 4. Copy frontend dist into Go embed directory ────────────────────────────
echo ""
echo "==> Embedding frontend into backend..."
mkdir -p "$REPO/backend/web/dist"
rm -rf "$REPO/backend/web/dist/"*
cp -r "$REPO/frontend/dist/." "$REPO/backend/web/dist/"

# ── 5. Build Go binary ────────────────────────────────────────────────────────
echo ""
echo "==> Building FeatherDeploy binary..."
cd "$REPO/backend"
mkdir -p "$REPO/dist"
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 \
  go build \
    -ldflags="-s -w" \
    -o "$REPO/dist/featherdeploy" \
    ./cmd/server/

# Copy to system path
cp "$REPO/dist/featherdeploy" "$BINARY"
chmod +x "$BINARY"
echo "  Binary installed to: $BINARY"

# ── 6. Run the interactive installer ─────────────────────────────────────────
echo ""
echo "==> Launching FeatherDeploy setup wizard..."
echo ""
exec "$BINARY" install
