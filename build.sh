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
RQLITE_DATA_DIR="/var/lib/featherdeploy/rqlite-data"
RQLITE_VER="8.36.5"
SVC_USER="featherdeploy"

# -- 0. Must run as root
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

# -- Helper: detect system architecture
detect_arch() {
  local machine
  machine=$(uname -m)
  case "$machine" in
    x86_64)           echo "amd64" ;;
    aarch64|arm64)    echo "arm64" ;;
    armv7l|armv6l)    echo "arm"   ;;
    *)
      echo "ERROR: Unsupported architecture: $machine" >&2
      exit 1
      ;;
  esac
}

# -- Helper: configure crun as Podman OCI runtime
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

# -- Helper: install rqlite binary
# Pass --force to always remove existing binary first.
install_rqlite() {
  local force="${1:-}"
  local ARCH
  ARCH=$(detect_arch)

  if [ "$force" = "--force" ]; then
    echo "==> Removing any existing rqlite binaries..."
    systemctl stop rqlite 2>/dev/null || true
    rm -f /usr/local/bin/rqlited /usr/local/bin/rqlite
  else
    if [ -f /usr/local/bin/rqlited ]; then
      if /usr/local/bin/rqlited --version >/dev/null 2>&1 || \
         /usr/local/bin/rqlited --help   >/dev/null 2>&1; then
        echo "  rqlited already installed -- skipping"
        return
      fi
      echo "  WARNING: existing rqlited binary is corrupt -- forcing reinstall"
      rm -f /usr/local/bin/rqlited /usr/local/bin/rqlite
    fi
  fi

  echo "==> Installing rqlite ${RQLITE_VER} (${ARCH})..."
  local TAR="rqlite-v${RQLITE_VER}-linux-${ARCH}.tar.gz"
  local URL="https://github.com/rqlite/rqlite/releases/download/v${RQLITE_VER}/${TAR}"

  rm -f "/tmp/${TAR}"
  rm -rf /tmp/rqlite-v*

  local attempt=0
  local downloaded=false
  while [ $attempt -lt 3 ]; do
    attempt=$(( attempt + 1 ))
    echo "  Downloading rqlite (attempt ${attempt}/3)..."
    rm -f "/tmp/${TAR}"

    if ! curl --fail --show-error --location \
         --connect-timeout 30 --max-time 180 \
         "$URL" -o "/tmp/${TAR}" 2>&1; then
      echo "  Download attempt ${attempt} failed -- retrying..."
      sleep 3
      continue
    fi

    local filesize
    filesize=$(stat -c%s "/tmp/${TAR}" 2>/dev/null || stat -f%z "/tmp/${TAR}" 2>/dev/null || echo 0)
    if [ "$filesize" -lt 5242880 ]; then
      echo "  Downloaded file too small (${filesize} bytes) -- retrying..."
      sleep 3
      continue
    fi

    echo "  Verifying archive integrity..."
    if tar -tzf "/tmp/${TAR}" >/dev/null 2>&1; then
      downloaded=true
      break
    else
      echo "  Archive integrity check failed (attempt ${attempt}) -- retrying..."
      sleep 3
      continue
    fi
  done

  if [ "$downloaded" = false ]; then
    echo "  ERROR: rqlite download failed after 3 attempts."
    echo "  Install manually: https://github.com/rqlite/rqlite/releases/tag/v${RQLITE_VER}"
    rm -f "/tmp/${TAR}"
    exit 1
  fi

  local EXTRACTED_DIR
  EXTRACTED_DIR=$(tar -tzf "/tmp/${TAR}" | sed 's|/.*||' | grep -v '^$' | sort -u | head -1)
  if [ -z "$EXTRACTED_DIR" ]; then
    echo "  ERROR: could not determine rqlite directory name from archive."
    rm -f "/tmp/${TAR}"
    exit 1
  fi

  echo "  Extracting ${EXTRACTED_DIR}..."
  rm -rf "/tmp/${EXTRACTED_DIR}"
  if ! tar -xzf "/tmp/${TAR}" -C /tmp/; then
    echo "  ERROR: rqlite extraction failed."
    rm -f "/tmp/${TAR}"
    exit 1
  fi

  if [ ! -f "/tmp/${EXTRACTED_DIR}/rqlited" ] || [ ! -f "/tmp/${EXTRACTED_DIR}/rqlite" ]; then
    echo "  ERROR: rqlited/rqlite binaries not found in extracted archive."
    rm -rf "/tmp/${TAR}" "/tmp/${EXTRACTED_DIR}"
    exit 1
  fi

  install -m 755 "/tmp/${EXTRACTED_DIR}/rqlited" /usr/local/bin/rqlited
  install -m 755 "/tmp/${EXTRACTED_DIR}/rqlite"  /usr/local/bin/rqlite
  rm -rf "/tmp/${TAR}" "/tmp/${EXTRACTED_DIR}"

  if ! /usr/local/bin/rqlited --version >/dev/null 2>&1 && \
     ! /usr/local/bin/rqlited --help   >/dev/null 2>&1; then
    echo "  ERROR: installed rqlited binary does not work."
    rm -f /usr/local/bin/rqlited /usr/local/bin/rqlite
    exit 1
  fi
  echo "  rqlited installed successfully (arch: ${ARCH})"
}

# -- Helper: detect the server's primary non-loopback IPv4 address
detect_server_ip() {
  # Try ip route first (most reliable), then hostname, then ifconfig fallback
  local ip
  ip=$(ip route get 1.1.1.1 2>/dev/null | awk '/src/ { print $7; exit }')
  if [ -z "$ip" ] || [ "$ip" = "0.0.0.0" ]; then
    ip=$(hostname -I 2>/dev/null | awk '{print $1}')
  fi
  if [ -z "$ip" ] || [ "$ip" = "0.0.0.0" ]; then
    ip=$(ip addr show 2>/dev/null | awk '/inet / && !/127\.0\.0\.1/ { gsub(/\/.*/, "", $2); print $2; exit }')
  fi
  if [ -z "$ip" ] || [ "$ip" = "0.0.0.0" ]; then
    echo "ERROR: Could not detect server IP address. Set it manually via FEATHERDEPLOY_HOST env var." >&2
    exit 1
  fi
  echo "$ip"
}

# -- Helper: write rqlite systemd service
write_rqlite_service() {
  # rqlite 8.x requires an explicit routable advertise address when binding
  # raft on 0.0.0.0 -- detect the real IP and pass it via -raft-adv-addr.
  local SERVER_IP
  SERVER_IP=$(detect_server_ip)
  echo "  Detected server IP for rqlite Raft advertise: ${SERVER_IP}"

  cat > "$RQLITE_UNIT" << RQEOF
[Unit]
Description=rqlite Distributed SQLite
After=network.target
Before=featherdeploy.service

[Service]
Type=simple
User=${SVC_USER}
Group=${SVC_USER}
NoNewPrivileges=true
ProtectSystem=false
ReadWritePaths=${RQLITE_DATA_DIR}
ExecStart=/usr/local/bin/rqlited \
  -node-id=main \
  -http-addr=127.0.0.1:4001 \
  -raft-addr=0.0.0.0:4002 \
  -raft-adv-addr=${SERVER_IP}:4002 \
  -bootstrap-expect=1 \
  ${RQLITE_DATA_DIR}
Restart=on-failure
RestartSec=5s
TimeoutStartSec=30s
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
RQEOF
  echo "  rqlite systemd service written"
}

# -- Helper: ensure rqlite is running; print real errors if it fails
ensure_rqlite_running() {
  systemctl daemon-reload
  systemctl enable rqlite 2>/dev/null || true

  if systemctl is-active --quiet rqlite; then
    echo "  rqlite already running"
    return
  fi

  # Always fix ownership right before starting
  chown -R "${SVC_USER}:${SVC_USER}" "$RQLITE_DATA_DIR"
  chmod 750 "$RQLITE_DATA_DIR"

  echo "  Starting rqlite..."
  if ! systemctl start rqlite 2>/dev/null; then
    echo ""
    echo "  ERROR: rqlite service failed to start immediately."
    echo "  Journal output:"
    echo "  -----------------------------------------------------------------------"
    journalctl -u rqlite -n 30 --no-pager 2>/dev/null || true
    echo "  -----------------------------------------------------------------------"
    exit 1
  fi

  echo "  Waiting for rqlite to elect leader and become ready..."
  # Use /readyz (not /status) -- rqlite v8 only responds 200 on /readyz once
  # Raft leader election is complete and the node can accept write requests.
  local deadline=$(( $(date +%s) + 35 ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    if curl -sf http://127.0.0.1:4001/readyz >/dev/null 2>&1; then
      sleep 1  # grace period after leader election
      echo "  ✓ rqlite ready"
      return
    fi
    sleep 1
  done

  # Not responding after 20s -- show the actual journal so you can diagnose it
  echo ""
  echo "  ERROR: rqlite is not ready at :4001/readyz after 35s"
  echo "  Full journal output:"
  echo "  -----------------------------------------------------------------------"
  journalctl -u rqlite -n 50 --no-pager 2>/dev/null || true
  echo "  -----------------------------------------------------------------------"
  echo ""
  echo "  Common causes:"
  echo "    1) Port conflict     -- run: ss -tlnp | grep -E '4001|4002'"
  echo "    2) Permission error  -- run: ls -la /var/lib/featherdeploy/"
  echo "    3) Corrupt data dir  -- run: rm -rf ${RQLITE_DATA_DIR} then re-run script"
  exit 1
}

# -- 1. Install build deps
# NOTE: install_rqlite is intentionally NOT called here.
# It is called in step 9, AFTER the service user is created.

install_deps_apt() {
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -y
  apt-get install -y curl git gcc make ca-certificates build-essential sudo uidmap
  if ! command -v podman >/dev/null 2>&1; then
    echo "==> Installing Podman..."
    apt-get install -y podman 2>/dev/null || apt-get install -y podman-docker 2>/dev/null || echo "  WARNING: podman not in apt"
  else
    echo "  Podman already installed -- skipping"
  fi
  if ! command -v crun >/dev/null 2>&1; then
    echo "==> Installing crun..."
    apt-get install -y crun 2>/dev/null || echo "  WARNING: crun not available in apt"
  else
    echo "  crun already installed -- skipping"
  fi
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
  if ! command -v node >/dev/null 2>&1 || [ "$(node --version | cut -d. -f1 | tr -d v)" -lt 18 ]; then
    echo "==> Installing Node.js 20..."
    curl -fsSL https://deb.nodesource.com/setup_20.x | bash -
    apt-get install -y nodejs
  fi
  install_go_tarball
  configure_crun
}

install_deps_dnf() {
  dnf install -y curl git gcc make ca-certificates
  command -v podman >/dev/null 2>&1 || dnf install -y podman
  command -v crun   >/dev/null 2>&1 || dnf install -y crun 2>/dev/null || echo '  WARNING: crun not available via dnf'
  command -v caddy  >/dev/null 2>&1 || dnf install -y caddy 2>/dev/null || \
    (dnf copr enable -y @caddy/caddy 2>/dev/null && dnf install -y caddy) || echo '  WARNING: caddy not via dnf'
  command -v node >/dev/null 2>&1 || { dnf module enable -y nodejs:20 2>/dev/null || true; dnf install -y nodejs npm; }
  install_go_tarball ; configure_crun
}

install_deps_yum() {
  yum install -y curl git gcc make ca-certificates
  command -v podman >/dev/null 2>&1 || yum install -y podman
  command -v crun   >/dev/null 2>&1 || yum install -y crun 2>/dev/null || echo '  WARNING: crun not available via yum'
  command -v caddy  >/dev/null 2>&1 || (yum install -y yum-plugin-copr && yum copr enable -y @caddy/caddy && yum install -y caddy) || echo '  WARNING: caddy not via yum'
  command -v node >/dev/null 2>&1 || { curl -fsSL https://rpm.nodesource.com/setup_20.x | bash -; yum install -y nodejs; }
  install_go_tarball ; configure_crun
}

install_deps_apk() {
  apk update
  apk add --no-cache curl git gcc musl-dev make nodejs npm podman caddy
  apk add --no-cache crun 2>/dev/null || echo '  WARNING: crun not available via apk'
  install_go_tarball ; configure_crun
}

install_deps_pacman() {
  pacman -Sy --noconfirm curl git gcc make nodejs npm go podman caddy
  command -v crun >/dev/null 2>&1 || pacman -S --noconfirm crun 2>/dev/null || echo '  WARNING: crun not available via pacman'
  configure_crun
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

# -- 2. Clone or update source
echo "" ; echo "==> Fetching FeatherDeploy source..."
if [ -d "$INSTALL_DIR/.git" ]; then
  git -C "$INSTALL_DIR" fetch origin
  git -C "$INSTALL_DIR" reset --hard origin/main
else
  git clone "$REPO_URL" "$INSTALL_DIR"
fi
REPO="$INSTALL_DIR"

# -- 3. Build frontend
echo "" ; echo "==> Building frontend..."
cd "$REPO/frontend"
npm ci --prefer-offline
npm run build

# -- 4. Embed frontend into Go
echo "" ; echo "==> Embedding frontend into backend..."
mkdir -p "$REPO/backend/web/dist"
rm -rf "$REPO/backend/web/dist/"*
cp -r "$REPO/frontend/dist/." "$REPO/backend/web/dist/"

# -- 5. Build Go binaries
echo "" ; echo "==> Building FeatherDeploy main binary (CGO_ENABLED=0)..."
cd "$REPO/backend"
mkdir -p "$REPO/dist"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -ldflags="-s -w" -o "$REPO/dist/featherdeploy" ./cmd/server/
echo "==> Building FeatherDeploy node agent..."
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -ldflags="-s -w" -o "$REPO/dist/featherdeploy-node" ./cmd/node/

# -- 6. Stop service (update / reinstall only)
if [ "$MODE" != "install" ]; then
  echo "" ; echo "==> Stopping featherdeploy service..."
  systemctl stop featherdeploy 2>/dev/null || true
fi

# -- 7. Install binaries
cp "$REPO/dist/featherdeploy" "$BINARY" ; chmod +x "$BINARY"
echo "  Binary installed: $BINARY"
cp "$REPO/dist/featherdeploy-node" "$NODE_BINARY" ; chmod +x "$NODE_BINARY"
echo "  Node agent installed: $NODE_BINARY"

# -- 8. Create system user (MUST happen before rqlite service is written/started)
echo "==> Ensuring featherdeploy system user exists..."
if ! id -u "$SVC_USER" >/dev/null 2>&1; then
  # --system        = low UID, no aging, no login
  # --no-create-home = no /home/featherdeploy
  # --shell          = no interactive shell
  useradd --system --no-create-home --shell /usr/sbin/nologin "$SVC_USER"
  echo "  Created system user: ${SVC_USER}  (no password -- service account only)"
else
  echo "  System user ${SVC_USER} already exists -- skipping"
fi

# -- 8b. Configure rootless Podman: add subuid/subgid ranges for the service user.
# Without /etc/subuid + /etc/subgid entries, Podman cannot set up user namespaces
# and every `podman build`/`podman run` fails with "no subuid ranges found".
echo "==> Configuring rootless Podman for ${SVC_USER}..."
for _subfile in /etc/subuid /etc/subgid; do
  if ! grep -q "^${SVC_USER}:" "$_subfile" 2>/dev/null; then
    echo "${SVC_USER}:100000:65536" >> "$_subfile"
    echo "  Added entry to ${_subfile}: ${SVC_USER}:100000:65536"
  else
    echo "  ${_subfile} already has an entry for ${SVC_USER} — skipping"
  fi
done
if command -v podman >/dev/null 2>&1; then
  su -s /bin/sh -c "podman system migrate" "${SVC_USER}" 2>/dev/null || true
  echo "  Podman storage migrated for ${SVC_USER}"
fi

# -- 8c. Enable unprivileged user namespaces so rootless tools can work
echo "==> Enabling unprivileged user namespaces..."
mkdir -p /etc/sysctl.d
if [ -f /proc/sys/kernel/unprivileged_userns_clone ]; then
  sysctl -w kernel.unprivileged_userns_clone=1 2>/dev/null || true
  echo 'kernel.unprivileged_userns_clone=1' > /etc/sysctl.d/99-featherdeploy.conf
  echo "  kernel.unprivileged_userns_clone=1"
fi
# Ensure at least 3000 user namespaces are available (default may be 0 on some kernels)
if [ -f /proc/sys/user/max_user_namespaces ]; then
  cur=$(cat /proc/sys/user/max_user_namespaces)
  if [ "$cur" -lt 3000 ] 2>/dev/null; then
    sysctl -w user.max_user_namespaces=3000 2>/dev/null || true
    echo 'user.max_user_namespaces=3000' >> /etc/sysctl.d/99-featherdeploy.conf
    echo "  user.max_user_namespaces=3000"
  fi
fi

# -- 8d. sudo rule: allow featherdeploy to run rootful podman without a password
# This gives the service account access to rootful podman, which avoids all
# rootless user-namespace requirements (newuidmap failures, kernel restrictions).
echo "==> Installing sudo rule for featherdeploy → podman + caddy + self-update..."
cat > /etc/sudoers.d/featherdeploy-podman << 'SUDOEOF'
Defaults!/usr/bin/podman !requiretty
featherdeploy ALL=(root) NOPASSWD: /usr/bin/podman
featherdeploy ALL=(root) NOPASSWD: /bin/systemctl reload caddy
featherdeploy ALL=(root) NOPASSWD: /usr/bin/systemctl reload caddy
featherdeploy ALL=(root) NOPASSWD: /usr/bin/tee /etc/caddy/featherdeploy-services.caddy
featherdeploy ALL=(root) NOPASSWD: /usr/local/bin/featherdeploy-update
SUDOEOF
chmod 440 /etc/sudoers.d/featherdeploy-podman
echo "  /etc/sudoers.d/featherdeploy-podman installed"

# -- 8f. Install the self-update helper script (used by one-click UI updates)
echo "==> Installing featherdeploy-update helper script..."
cat > /usr/local/bin/featherdeploy-update << 'UPDATEEOF'
#!/usr/bin/env bash
# featherdeploy-update <VERSION> <ARCH>
# Downloads the release binary for the given version, replaces the running
# binary, then delegates to "featherdeploy update" for DB migrations and
# service restart.  Must be run as root (via sudo -n from the panel service).
set -euo pipefail
VERSION="${1:?Usage: featherdeploy-update <version> <arch>}"
ARCH="${2:?Usage: featherdeploy-update <version> <arch>}"
URL="https://github.com/ojhapranjal26/FeatherDeploy/releases/download/v${VERSION}/featherdeploy-linux-${ARCH}"
TMP=$(mktemp /tmp/featherdeploy-update-XXXXXX)
trap 'rm -f "$TMP"' EXIT
echo "==> Downloading FeatherDeploy v${VERSION} (${ARCH})..."
curl -fsSL --retry 3 --retry-delay 5 --connect-timeout 30 --max-time 300 "$URL" -o "$TMP"
chmod +x "$TMP"
echo "==> Installing new binary to /usr/local/bin/featherdeploy..."
cp -f "$TMP" /usr/local/bin/featherdeploy
echo "==> Running migration + service restart..."
exec /usr/local/bin/featherdeploy update
UPDATEEOF
chmod 755 /usr/local/bin/featherdeploy-update
echo "  featherdeploy-update installed"

# -- 8e. Create the Caddy services include file with correct ownership so the
#        FeatherDeploy service account can write domain routing config to it.
echo "==> Preparing Caddy services include file..."
touch /etc/caddy/featherdeploy-services.caddy
chown "${SVC_USER}:${SVC_USER}" /etc/caddy/featherdeploy-services.caddy
chmod 644 /etc/caddy/featherdeploy-services.caddy
echo "  /etc/caddy/featherdeploy-services.caddy ready"

# -- 9. Set up data directory with correct ownership, then install + start rqlite
echo "==> Setting up data directory..."
mkdir -p "$RQLITE_DATA_DIR"
chown -R "${SVC_USER}:${SVC_USER}" /var/lib/featherdeploy
chmod 750 /var/lib/featherdeploy
chmod 750 "$RQLITE_DATA_DIR"
echo "  Data directory ready: ${RQLITE_DATA_DIR}"

if [ "$MODE" = "install" ]; then
  echo "==> Cleaning up any previous rqlite installation..."
  systemctl stop rqlite 2>/dev/null || true
  rm -rf "$RQLITE_DATA_DIR"
  mkdir -p "$RQLITE_DATA_DIR"
  chown -R "${SVC_USER}:${SVC_USER}" "$RQLITE_DATA_DIR"
  chmod 750 "$RQLITE_DATA_DIR"
  install_rqlite --force
else
  install_rqlite
fi

write_rqlite_service
ensure_rqlite_running

# -- 10. Reinstall: wipe DB + run wizard
if [ "$MODE" = "reinstall" ]; then
  echo "" ; echo "==> Removing existing database..."
  systemctl stop rqlite 2>/dev/null || true
  rm -f "$DATA_DB"
  rm -rf "$RQLITE_DATA_DIR"
  mkdir -p "$RQLITE_DATA_DIR"
  chown -R "${SVC_USER}:${SVC_USER}" "$RQLITE_DATA_DIR"
  chmod 750 "$RQLITE_DATA_DIR"
  ensure_rqlite_running
  echo "" ; echo "==> Launching FeatherDeploy setup wizard..." ; echo ""
  exec "$BINARY" install

# -- 11. Update: migrate + restart
elif [ "$MODE" = "update" ]; then
  echo "" ; echo "==> Updating FeatherDeploy..."
  exec "$BINARY" update

# -- 12. First install: run wizard
else
  echo "" ; echo "==> Launching FeatherDeploy setup wizard..." ; echo ""
  exec "$BINARY" install
fi