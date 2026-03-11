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

# -- Helper: detect system architecture --------------------------------------
detect_arch() {
  local machine
  machine=$(uname -m)
  case "$machine" in
    x86_64)           echo "amd64" ;;
    aarch64|arm64)    echo "arm64" ;;
    armv7l|armv6l)    echo "arm" ;;
    *)
      echo "ERROR: Unsupported architecture: $machine" >&2
      exit 1
      ;;
  esac
}

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
# Pass --force as first arg to always remove any existing binary before installing.
install_rqlite() {
  local force="${1:-}"

  # FIX 1: Detect architecture dynamically instead of hardcoding amd64.
  local ARCH
  ARCH=$(detect_arch)

  if [ "$force" = "--force" ]; then
    echo "==> Removing any existing rqlite binaries..."
    systemctl stop rqlite 2>/dev/null || true
    rm -f /usr/local/bin/rqlited /usr/local/bin/rqlite
  else
    # FIX 2: Use a more reliable binary health-check.
    # Some rqlite builds exit non-zero on --version; use --help as fallback.
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

  # FIX 3: Clean up any leftover /tmp artefacts from prior failed runs.
  rm -f "/tmp/${TAR}"
  # Remove any directory that starts with the expected prefix to avoid stale state.
  rm -rf /tmp/rqlite-v*

  # Retry download up to 3 times to handle transient timeouts/corruption.
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

    # FIX 4: Verify both archive integrity AND minimum file size.
    # A truncated download can produce a valid-looking gzip header that passes
    # `tar -tzf` but fails on extraction.
    local filesize
    filesize=$(stat -c%s "/tmp/${TAR}" 2>/dev/null || stat -f%z "/tmp/${TAR}" 2>/dev/null || echo 0)
    if [ "$filesize" -lt 5242880 ]; then   # expect at least 5 MB
      echo "  Downloaded file is suspiciously small (${filesize} bytes) -- retrying..."
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

  # FIX 5: Robust directory-name extraction that works across tar versions.
  # List archive, strip trailing slash, take first unique top-level entry.
  local EXTRACTED_DIR
  EXTRACTED_DIR=$(tar -tzf "/tmp/${TAR}" | sed 's|/.*||' | grep -v '^$' | sort -u | head -1)
  if [ -z "$EXTRACTED_DIR" ]; then
    echo "  ERROR: could not determine rqlite directory name from archive."
    rm -f "/tmp/${TAR}"
    exit 1
  fi

  echo "  Extracting ${EXTRACTED_DIR}..."
  # FIX 6: Remove any pre-existing extracted directory before extracting to
  # prevent mixing files from different versions.
  rm -rf "/tmp/${EXTRACTED_DIR}"
  if ! tar -xzf "/tmp/${TAR}" -C /tmp/; then
    echo "  ERROR: rqlite extraction failed -- archive may be corrupted."
    rm -f "/tmp/${TAR}"
    exit 1
  fi

  # Verify the extracted binaries exist.
  if [ ! -f "/tmp/${EXTRACTED_DIR}/rqlited" ] || [ ! -f "/tmp/${EXTRACTED_DIR}/rqlite" ]; then
    echo "  ERROR: rqlited/rqlite binaries not found in extracted archive."
    echo "  Expected: /tmp/${EXTRACTED_DIR}/rqlited and /tmp/${EXTRACTED_DIR}/rqlite"
    rm -rf "/tmp/${TAR}" "/tmp/${EXTRACTED_DIR}"
    exit 1
  fi

  install -m 755 "/tmp/${EXTRACTED_DIR}/rqlited" /usr/local/bin/rqlited
  install -m 755 "/tmp/${EXTRACTED_DIR}/rqlite"  /usr/local/bin/rqlite
  rm -rf "/tmp/${TAR}" "/tmp/${EXTRACTED_DIR}"

  # FIX 7: Validate installed binary with a two-stage health-check.
  if ! /usr/local/bin/rqlited --version >/dev/null 2>&1 && \
     ! /usr/local/bin/rqlited --help   >/dev/null 2>&1; then
    echo "  ERROR: installed rqlited binary does not work."
    rm -f /usr/local/bin/rqlited /usr/local/bin/rqlite
    exit 1
  fi
  echo "  rqlited installed successfully (arch: ${ARCH})"
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
  # NOTE: install_rqlite is called later in step 8, AFTER the service user is
  # created, so it is intentionally NOT called here.
  install_go_tarball ; configure_crun
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

# -- Helper: ensure rqlite service is running (with data-dir recovery) -------
ensure_rqlite_running() {
  systemctl daemon-reload
  systemctl enable rqlite 2>/dev/null || true

  if systemctl is-active --quiet rqlite; then
    echo "  rqlite already running"
    return
  fi

  echo "  Starting rqlite..."
  systemctl start rqlite 2>/dev/null || true

  # Wait up to 15s for rqlite to answer
  local deadline=$(( $(date +%s) + 15 ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    if curl -sf http://127.0.0.1:4001/status >/dev/null 2>&1; then
      echo "  rqlite is ready"
      return
    fi
    sleep 1
  done

  # Service started but rqlite not responding -- likely corrupt data directory
  echo "  WARNING: rqlite did not respond -- data directory may be corrupt"
  echo "  Wiping rqlite data directory and retrying..."
  systemctl stop rqlite 2>/dev/null || true
  rm -rf /var/lib/featherdeploy/rqlite-data
  mkdir -p /var/lib/featherdeploy/rqlite-data
  chown -R featherdeploy:featherdeploy /var/lib/featherdeploy/rqlite-data
  chmod 750 /var/lib/featherdeploy/rqlite-data
  systemctl start rqlite

  local deadline2=$(( $(date +%s) + 30 ))
  while [ "$(date +%s)" -lt "$deadline2" ]; do
    if curl -sf http://127.0.0.1:4001/status >/dev/null 2>&1; then
      echo "  rqlite is ready (fresh data directory)"
      return
    fi
    sleep 1
  done

  echo "  ERROR: rqlite failed to start even after wiping data. Check: journalctl -u rqlite" >&2
  exit 1
}

# -- 8. Create service user + data directory (must happen before rqlite starts)
echo "==> Ensuring featherdeploy system user exists..."
if ! id -u featherdeploy >/dev/null 2>&1; then
  useradd --system --no-create-home --shell /usr/sbin/nologin featherdeploy
  echo "  Created system user: featherdeploy"
else
  echo "  System user featherdeploy already exists -- skipping"
fi

echo "==> Setting up data directory..."
mkdir -p /var/lib/featherdeploy/rqlite-data
chown -R featherdeploy:featherdeploy /var/lib/featherdeploy
chmod 750 /var/lib/featherdeploy

# -- 8b. Install rqlite binary + write + start service -----------------------
# On first install: always force-reinstall for a clean start.
# On update/reinstall: skip download if binary is already healthy.
if [ "$MODE" = "install" ]; then
  echo "==> Cleaning up any previous rqlite installation..."
  systemctl stop rqlite 2>/dev/null || true
  rm -rf /var/lib/featherdeploy/rqlite-data
  mkdir -p /var/lib/featherdeploy/rqlite-data
  chown -R featherdeploy:featherdeploy /var/lib/featherdeploy/rqlite-data
  install_rqlite --force
else
  install_rqlite
fi

write_rqlite_service "featherdeploy"
ensure_rqlite_running

# -- 9. Reinstall: wipe DB + run wizard --------------------------------------
if [ "$MODE" = "reinstall" ]; then
  echo "" ; echo "==> Removing existing database..."
  systemctl stop rqlite 2>/dev/null || true
  rm -f "$DATA_DB" ; rm -rf /var/lib/featherdeploy/rqlite-data
  mkdir -p /var/lib/featherdeploy/rqlite-data
  chown -R featherdeploy:featherdeploy /var/lib/featherdeploy/rqlite-data
  chmod 750 /var/lib/featherdeploy/rqlite-data
  ensure_rqlite_running
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