#!/usr/bin/env bash
# =============================================================================
#  FeatherDeploy -- build + install / update script
#  Usage: curl -fsSL https://raw.githubusercontent.com/ojhapranjal26/FeatherDeploy/main/build.sh | sudo bash
#
#  Modes:
#   First install  -- full interactive wizard
#   Update         -- rebuild + migrate + restart (data preserved)
#   Reinstall      -- wipe database + run wizard again
# =============================================================================
set -euo pipefail

REPO_URL="https://github.com/ojhapranjal26/FeatherDeploy.git"
INSTALL_DIR="/opt/featherdeploy-src"
BINARY="/usr/local/bin/featherdeploy"
NODE_BINARY="/usr/local/bin/featherdeploy-node"
ENV_FILE="/etc/featherdeploy/featherdeploy.env"
SYSTEMD_UNIT="/etc/systemd/system/featherdeploy.service"
RQLITE_UNIT="/etc/systemd/system/rqlite.service"
DATA_DB="/var/lib/featherdeploy/deploy.db"
RQLITE_VER="8.36.5"

# -- 0. Must run as root ------------------------------------------------------
if [ "$(id -u)" -ne 0 ]; then
  echo "ERROR: This script must be run as root (use sudo)." >&2
  exit 1
fi

is_installed() { [ -f "$ENV_FILE" ] || [ -f "$SYSTEMD_UNIT" ]; }

print_header() {
  echo ""
  echo "  +=====================================================+"
  echo "  |         FeatherDeploy  --  Setup & Updater         |"
  echo "  +=====================================================+"
  echo ""
}
print_header

MODE="install"
if is_installed; then
  echo "  An existing FeatherDeploy installation was detected."
  echo ""
  echo "  What would you like to do?"
  echo "    [U]  Update    -- rebuild binary, apply DB migrations (data kept)"
  echo "    [R]  Reinstall -- wipe database and run the full setup wizard"
  echo ""
  printf "  Your choice [U/r]: "
  read -r user_choice </dev/tty
  user_choice=$(printf '%s' "$user_choice" | tr '[:upper:]' '[:lower:]' | tr -d '[:space:]')
  if [ "$user_choice" = "r" ]; then
    printf "  Type YES to confirm permanent data deletion: "
    read -r confirm </dev/tty
    if [ "$confirm" != "YES" ]; then echo "  Aborted."; exit 0; fi
    MODE="reinstall"
  else
    MODE="update"
  fi
fi
echo "  Mode: $MODE" ; echo ""

# -- Helper: configure crun as Podman OCI runtime ----------------------------
configure_crun() {
  if ! command -v crun >/dev/null 2>&1; then
    echo "  WARNING: crun not found -- skipping Podman runtime config"
    return
  fi
  echo "==> Configuring crun as Podman OCI runtime..."
  mkdir -p /etc/containers
  local conf="/etc/containers/containers.conf"
  if [ -f "$conf" ]; then
    if grep -qE '^\s*runtime\s*=' "$conf"; then
      sed -i 's|^\s*runtime\s*=.*|runtime = "crun"|' "$conf"
    elif grep -q '\[engine\]' "$conf"; then
      sed -i '/\[engine\]/a runtime = "crun"' "$conf"
    else
      printf '\n[engine]\nruntime = "crun"\n' >> "$conf"
    fi
  else
    printf '[engine]\nruntime = "crun"\n' > "$conf"
  fi
  echo "  crun configured as Podman OCI runtime"
}

# -- Helper: install rqlite binary -------------------------------------------
install_rqlite() {
  if command -v rqlited >/dev/null 2>&1; then
    echo "  rqlited already installed -- skipping"
    return
  fi
  echo "==> Installing rqlite ${RQLITE_VER}..."
  local ARCH="amd64"
  local TAR="rqlite-v${RQLITE_VER}-linux-${ARCH}.tar.gz"
  local URL="https://github.com/rqlite/rqlite/releases/download/v${RQLITE_VER}/${TAR}"
  curl -fsSL "$URL" -o "/tmp/${TAR}"
  local EXTRACTED_DIR
  EXTRACTED_DIR=$(tar -tzf "/tmp/${TAR}" | head -1 | cut -f1 -d"/")
  tar -xzf "/tmp/${TAR}" -C /tmp/
  install -m 755 "/tmp/${EXTRACTED_DIR}/rqlited" /usr/local/bin/rqlited
  install -m 755 "/tmp/${EXTRACTED_DIR}/rqlite"  /usr/local/bin/rqlite
  rm -rf "/tmp/${TAR}" "/tmp/${EXTRACTED_DIR}"
  echo "  rqlited installed"
}

# -- Helper: write rqlite systemd service ------------------------------------
write_rqlite_service() {
  local svc_user="${1:-featherdeploy}"
  cat > "$RQLITE_UNIT" << RQEOF
[Unit]
Description=rqlite Distributed SQLite
After=network.target
Before=featherdeploy.service

[Service]
Type=simple
User=${svc_user}
Group=${svc_user}
ExecStart=/usr/local/bin/rqlited \
  -node-id=main \
  -http-addr=127.0.0.1:4001 \
  -raft-addr=0.0.0.0:4002 \
  /var/lib/featherdeploy/rqlite-data
Restart=always
RestartSec=5s
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
RQEOF
  echo "  rqlite systemd service written"
}

# -- 1. Install build deps ---------------------------------------------------
install_deps_apt() {
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -y
  apt-get install -y curl git gcc make ca-certificates build-essential
  # Podman
  if ! command -v podman >/dev/null 2>&1; then
    echo "==> Installing Podman..."
    apt-get install -y podman 2>/dev/null || apt-get install -y podman-docker 2>/dev/null || echo "  WARNING: podman not in apt"
  else
    echo "  Podman already installed -- skipping"
  fi
  # crun (lightweight OCI runtime for Podman)
  if ! command -v crun >/dev/null 2>&1; then
    echo "==> Installing crun..."
    apt-get install -y crun 2>/dev/null || echo "  WARNING: crun not available in apt"
  else
    echo "  crun already installed -- skipping"
  fi
  # Caddy
  if ! command -v caddy >/dev/null 2>&1; then
    echo "==> Installing Caddy..."
    apt-get install -y debian-keyring debian-archive-keyring apt-transport-https 2>/dev/null || true
    curl -fsSL 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg 2>/dev/null || true
    curl -fsSL 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' | tee /etc/apt/sources.list.d/caddy-stable.list >/dev/null 2>&1 || true
    apt-get update -y 2>/dev/null || true
    apt-get install -y caddy
  else
    echo "  Caddy already installed -- skipping"
  fi
  # Node.js 20
  if ! command -v node >/dev/null 2>&1 || [ "$(node --version | cut -d. -f1 | tr -d v)" -lt 18 ]; then
    echo "==> Installing Node.js 20..."
    curl -fsSL https://deb.nodesource.com/setup_20.x | bash -
    apt-get install -y nodejs
  fi
  install_go_tarball ; install_rqlite ; configure_crun
}

install_deps_dnf() {
  dnf install -y curl git gcc make ca-certificates
  command -v podman >/dev/null 2>&1 || dnf install -y podman
  command -v crun   >/dev/null 2>&1 || dnf install -y crun 2>/dev/null || echo '  WARNING: crun not available via dnf'
  command -v caddy  >/dev/null 2>&1 || dnf install -y caddy 2>/dev/null || \
    (dnf copr enable -y @caddy/caddy 2>/dev/null && dnf install -y caddy) || echo '  WARNING: caddy not via dnf'
  command -v node >/dev/null 2>&1 || { dnf module enable -y nodejs:20 2>/dev/null || true; dnf install -y nodejs npm; }
  install_go_tarball ; install_rqlite ; configure_crun
}

install_deps_yum() {
  yum install -y curl git gcc make ca-certificates
  command -v podman >/dev/null 2>&1 || yum install -y podman
  command -v crun   >/dev/null 2>&1 || yum install -y crun 2>/dev/null || echo '  WARNING: crun not available via yum'
  command -v caddy  >/dev/null 2>&1 || (yum install -y yum-plugin-copr && yum copr enable -y @caddy/caddy && yum install -y caddy) || echo '  WARNING: caddy not via yum'
  command -v node >/dev/null 2>&1 || { curl -fsSL https://rpm.nodesource.com/setup_20.x | bash -; yum install -y nodejs; }
  install_go_tarball ; install_rqlite ; configure_crun
}

install_deps_apk() {
  apk update
  apk add --no-cache curl git gcc musl-dev make nodejs npm podman caddy
  apk add --no-cache crun 2>/dev/null || echo '  WARNING: crun not available via apk'
  install_go_tarball ; install_rqlite ; configure_crun
}

install_deps_pacman() {
  pacman -Sy --noconfirm curl git gcc make nodejs npm go podman caddy
  command -v crun >/dev/null 2>&1 || pacman -S --noconfirm crun 2>/dev/null || echo '  WARNING: crun not available via pacman'
  install_rqlite ; configure_crun
}

install_go_tarball() {
  local need_go=false
  command -v go >/dev/null 2>&1 || need_go=true
  if ! $need_go; then
    local ver major minor
    ver=$(go version | awk '{print $3}' | tr -d 'go')
    major=$(echo "$ver" | cut -d. -f1) ; minor=$(echo "$ver" | cut -d. -f2)
    { [ "$major" -lt 1 ] || { [ "$major" -eq 1 ] && [ "$minor" -lt 21 ]; }; } && need_go=true || true
  fi
  if $need_go; then
    local GO_VER="1.22.4" GO_TAR
    GO_TAR="go${GO_VER}.linux-amd64.tar.gz"
    echo "==> Installing Go ${GO_VER}..."
    curl -fsSL "https://dl.google.com/go/${GO_TAR}" -o "/tmp/${GO_TAR}"
    rm -rf /usr/local/go
    tar -C /usr/local -xzf "/tmp/${GO_TAR}" ; rm "/tmp/${GO_TAR}"
    export PATH="/usr/local/go/bin:$PATH"
    echo 'export PATH="/usr/local/go/bin:$PATH"' >> /etc/profile.d/go.sh
    echo "    Go $(go version) installed"
  else
    echo "  Go $(go version) already installed -- skipping"
  fi
}

echo "==> Checking build dependencies..."
if   command -v apt-get >/dev/null 2>&1; then install_deps_apt
elif command -v dnf     >/dev/null 2>&1; then install_deps_dnf
elif command -v yum     >/dev/null 2>&1; then install_deps_yum
elif command -v apk     >/dev/null 2>&1; then install_deps_apk
elif command -v pacman  >/dev/null 2>&1; then install_deps_pacman
else echo 'WARNING: no supported package manager. Install git/curl/gcc/crun/Node.js 20/Go 1.22+ manually.'; fi

export PATH="/usr/local/go/bin:$PATH"

# -- 2. Clone or update source -----------------------------------------------
echo "" ; echo "==> Fetching FeatherDeploy source..."
if [ -d "$INSTALL_DIR/.git" ]; then
  git -C "$INSTALL_DIR" fetch origin
  git -C "$INSTALL_DIR" reset --hard origin/main
else
  git clone "$REPO_URL" "$INSTALL_DIR"
fi
REPO="$INSTALL_DIR"

# -- 3. Build frontend -------------------------------------------------------
echo "" ; echo "==> Building frontend..."
cd "$REPO/frontend"
npm ci --prefer-offline
npm run build

# -- 4. Embed frontend into Go -----------------------------------------------
echo "" ; echo "==> Embedding frontend into backend..."
mkdir -p "$REPO/backend/web/dist"
rm -rf "$REPO/backend/web/dist/"*
cp -r "$REPO/frontend/dist/." "$REPO/backend/web/dist/"

# -- 5. Build Go binaries (main server + node agent) -------------------------
echo "" ; echo "==> Building FeatherDeploy main binary (CGO_ENABLED=0)..."
cd "$REPO/backend"
mkdir -p "$REPO/dist"

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -ldflags="-s -w" -o "$REPO/dist/featherdeploy" ./cmd/server/

echo "==> Building FeatherDeploy node agent..."
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -ldflags="-s -w" -o "$REPO/dist/featherdeploy-node" ./cmd/node/

# -- 6. Stop service (update / reinstall only) --------------------------------
if [ "$MODE" != "install" ]; then
  echo "" ; echo "==> Stopping featherdeploy service..."
  systemctl stop featherdeploy 2>/dev/null || true
fi

# -- 7. Install binaries ------------------------------------------------------
cp "$REPO/dist/featherdeploy" "$BINARY" ; chmod +x "$BINARY"
echo "  Binary installed: $BINARY"
cp "$REPO/dist/featherdeploy-node" "$NODE_BINARY" ; chmod +x "$NODE_BINARY"
echo "  Node agent installed: $NODE_BINARY"

# -- 8. Ensure rqlite data dir + service are in place ------------------------
mkdir -p /var/lib/featherdeploy/rqlite-data
if [ ! -f "$RQLITE_UNIT" ]; then
  write_rqlite_service "featherdeploy"
  systemctl daemon-reload
  systemctl enable rqlite
fi

# -- 9. Reinstall: wipe DB + run wizard --------------------------------------
if [ "$MODE" = "reinstall" ]; then
  echo "" ; echo "==> Removing existing database..."
  rm -f "$DATA_DB" ; rm -rf /var/lib/featherdeploy/rqlite-data
  mkdir -p /var/lib/featherdeploy/rqlite-data
  echo "" ; echo "==> Launching FeatherDeploy setup wizard..." ; echo ""
  exec "$BINARY" install

# -- 10. Update: migrate + restart -------------------------------------------
elif [ "$MODE" = "update" ]; then
  echo "" ; echo "==> Updating FeatherDeploy..."
  exec "$BINARY" update

# -- 11. First install: run wizard -------------------------------------------
else
  echo "" ; echo "==> Launching FeatherDeploy setup wizard..." ; echo ""
  exec "$BINARY" install
fi

