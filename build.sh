#!/usr/bin/env bash
# =============================================================================
#  FeatherDeploy — one-shot build + install script
#  Usage:
#    curl -fsSL https://raw.githubusercontent.com/ojhapranjal26/FeatherDeploy/main/build.sh | sudo bash
#  or clone the repo and run:
#    sudo bash build.sh
#
#  What this script does:
#   1. Detects the OS package manager and installs build dependencies
#      (git, curl, gcc, Node.js 20, Go 1.22)
#   2. Clones / updates the FeatherDeploy source code
#   3. Builds the frontend (npm ci + npm run build)
#   4. Builds the Go binary with CGO (single self-contained executable)
#   5. Runs the interactive installer:  sudo ./featherdeploy install
#      which will ask for domain, superadmin credentials, and the OS user
#      that FeatherDeploy will run as (non-root, default: featherdeploy)
# =============================================================================
set -euo pipefail

REPO_URL="https://github.com/ojhapranjal26/FeatherDeploy.git"
INSTALL_DIR="/opt/featherdeploy-src"
BINARY="/usr/local/bin/featherdeploy"

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

  # Node.js 20 via NodeSource
  if ! command -v node >/dev/null 2>&1 || [ "$(node --version | cut -d. -f1 | tr -d v)" -lt 18 ]; then
    echo "==> Installing Node.js 20..."
    curl -fsSL https://deb.nodesource.com/setup_20.x | bash -
    apt-get install -y nodejs
  fi

  # Go (latest stable via official tarball if not installed or too old)
  install_go_tarball
}

install_deps_dnf() {
  dnf install -y curl git gcc make ca-certificates sqlite-devel
  if ! command -v node >/dev/null 2>&1; then
    dnf module enable -y nodejs:20 2>/dev/null || true
    dnf install -y nodejs npm
  fi
  install_go_tarball
}

install_deps_yum() {
  yum install -y curl git gcc make ca-certificates sqlite-devel
  if ! command -v node >/dev/null 2>&1; then
    curl -fsSL https://rpm.nodesource.com/setup_20.x | bash -
    yum install -y nodejs
  fi
  install_go_tarball
}

install_deps_apk() {
  apk update
  apk add --no-cache curl git gcc musl-dev make nodejs npm sqlite-dev
  install_go_tarball
}

install_deps_pacman() {
  pacman -Sy --noconfirm curl git gcc make nodejs npm go sqlite
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
