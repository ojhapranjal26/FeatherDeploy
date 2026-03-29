#!/bin/bash
# FeatherDeploy — complete uninstall script
# Run as root: sudo bash uninstall.sh
# Or piped:    curl -fsSL https://.../uninstall.sh | sudo bash
#
# Removes ALL FeatherDeploy components including podman and crun packages.
# Does NOT touch Caddy (only removes the FeatherDeploy-managed config files).
#
# DATA WARNING: Irreversible. All services, databases, and history are deleted.

set -e

if [ "$(id -u)" -ne 0 ]; then
  echo "ERROR: run as root: sudo bash uninstall.sh"
  exit 1
fi

echo ""
echo "════════════════════════════════════════════════════════════════"
echo "  FeatherDeploy — Full Uninstall"
echo "════════════════════════════════════════════════════════════════"
echo ""
echo "  This will permanently delete:"
echo "    - featherdeploy and rqlite services and binaries"
echo "    - OS user 'featherdeploy' and ALL its data"
echo "    - podman, crun, netavark, aardvark-dns packages"
echo "    - /etc/featherdeploy/ config directory"
echo "    - FeatherDeploy Caddy config (Caddy itself stays installed)"
echo ""

# Read confirmation from /dev/tty so this works when piped from curl
# (stdin is the pipe, not the terminal)
if [ -t 0 ]; then
  # Running interactively
  read -r -p "Type 'yes' to confirm full uninstall: " confirm
else
  # Piped — must read from the real terminal
  read -r -p "Type 'yes' to confirm full uninstall: " confirm </dev/tty
fi

if [ "$confirm" != "yes" ]; then
  echo "Aborted."
  exit 0
fi

SVC_USER="featherdeploy"

# ── 1. Stop and disable all services ─────────────────────────────────────────
echo ""
echo "── Stopping services ───────────────────────────────────────────"
systemctl stop featherdeploy 2>/dev/null || true
systemctl stop rqlite        2>/dev/null || true
systemctl disable featherdeploy 2>/dev/null || true
systemctl disable rqlite        2>/dev/null || true
# Kill any remaining processes owned by the service user
pkill -9 -u "$SVC_USER" 2>/dev/null || true
sleep 1
echo "  ✓ services stopped"

# ── 2. Run podman system reset while user still exists ───────────────────────
echo ""
echo "── Resetting podman (removing images, containers, networks) ────"
if id -u "$SVC_USER" &>/dev/null; then
  mkdir -p /run/featherdeploy-runtime
  chown "$SVC_USER:$SVC_USER" /run/featherdeploy-runtime 2>/dev/null || true
  su -s /bin/sh "$SVC_USER" -c \
    "cd / && HOME=/var/lib/featherdeploy XDG_RUNTIME_DIR=/run/featherdeploy-runtime podman system reset --force 2>&1" \
    || true
  echo "  ✓ podman state cleared"
fi

# ── 3. Remove systemd units ───────────────────────────────────────────────────
echo ""
echo "── Removing systemd units ──────────────────────────────────────"
rm -f /etc/systemd/system/featherdeploy.service
rm -f /etc/systemd/system/rqlite.service
systemctl daemon-reload
echo "  ✓ systemd units removed"

# ── 4. Remove binaries ────────────────────────────────────────────────────────
echo ""
echo "── Removing FeatherDeploy/rqlite binaries ──────────────────────"
rm -f /usr/local/bin/featherdeploy
rm -f /usr/local/bin/rqlite
rm -f /usr/local/bin/rqlited
echo "  ✓ binaries removed"

# ── 5. Remove podman, crun, netavark packages ─────────────────────────────────
echo ""
echo "── Removing podman, crun, netavark packages ────────────────────"
if command -v dnf &>/dev/null; then
  dnf remove -y podman crun netavark aardvark-dns 2>/dev/null || true
  echo "  ✓ dnf: podman crun netavark aardvark-dns removed"
elif command -v apt-get &>/dev/null; then
  apt-get remove -y --purge podman crun netavark 2>/dev/null || true
  apt-get autoremove -y 2>/dev/null || true
  echo "  ✓ apt-get: podman crun netavark removed"
elif command -v yum &>/dev/null; then
  yum remove -y podman crun netavark aardvark-dns 2>/dev/null || true
  echo "  ✓ yum: podman crun netavark removed"
elif command -v pacman &>/dev/null; then
  pacman -Rns --noconfirm podman crun netavark 2>/dev/null || true
  echo "  ✓ pacman: podman crun netavark removed"
else
  echo "  WARNING: no supported package manager found — remove podman/crun manually"
fi
# Remove any remaining podman config/cache globally
rm -rf /etc/containers /var/lib/containers /var/cache/libpod

# ── 6. Remove the service user and data ──────────────────────────────────────
echo ""
echo "── Removing service user and data ─────────────────────────────"
if id -u "$SVC_USER" &>/dev/null; then
  userdel -r "$SVC_USER" 2>/dev/null || userdel "$SVC_USER" 2>/dev/null || true
  echo "  ✓ user '$SVC_USER' removed"
fi
# Remove data dirs in case userdel didn't (home may be at non-default path)
rm -rf /var/lib/featherdeploy
rm -rf /home/featherdeploy
rm -rf /run/featherdeploy-runtime
echo "  ✓ data directories removed"

# ── 7. Remove config ──────────────────────────────────────────────────────────
echo ""
echo "── Removing FeatherDeploy config ───────────────────────────────"
rm -rf /etc/featherdeploy
echo "  ✓ /etc/featherdeploy removed"

# ── 8. Clean FeatherDeploy Caddy config (Caddy stays installed) ──────────────
echo ""
echo "── Cleaning FeatherDeploy Caddy config (keeping Caddy) ─────────"
rm -f /etc/caddy/featherdeploy-services.caddy
if [ -f /etc/caddy/Caddyfile ]; then
  sed -i '/# Service domain routing.*FeatherDeploy/d' /etc/caddy/Caddyfile
  sed -i '/import \/etc\/caddy\/featherdeploy-services\.caddy/d' /etc/caddy/Caddyfile
  systemctl reload caddy 2>/dev/null || true
fi
echo "  ✓ Caddy FeatherDeploy config removed (Caddy still running)"

# ── 9. Remove subuid/subgid entries ──────────────────────────────────────────
echo ""
echo "── Removing subuid/subgid entries ─────────────────────────────"
sed -i "/^${SVC_USER}:/d" /etc/subuid 2>/dev/null || true
sed -i "/^${SVC_USER}:/d" /etc/subgid 2>/dev/null || true
echo "  ✓ subuid/subgid entries removed"

echo ""
echo "════════════════════════════════════════════════════════════════"
echo "  ✓ FeatherDeploy fully uninstalled."
echo ""
echo "  To reinstall, get the latest build.sh and run:"
echo "    sudo bash build.sh && sudo featherdeploy install"
echo "════════════════════════════════════════════════════════════════"
echo ""


set -e

if [ "$(id -u)" -ne 0 ]; then
  echo "ERROR: run as root: sudo bash uninstall.sh"
  exit 1
fi

echo ""
echo "════════════════════════════════════════════════════════════════"
echo "  FeatherDeploy — Full Uninstall"
echo "════════════════════════════════════════════════════════════════"
echo ""
echo "WARNING: This will permanently delete all FeatherDeploy data,"
echo "including services, databases, and deployment history."
echo ""
read -r -p "Type 'yes' to confirm: " confirm
if [ "$confirm" != "yes" ]; then
  echo "Aborted."
  exit 0
fi

SVC_USER="featherdeploy"

# ── 1. Stop and disable services ─────────────────────────────────────────────
echo ""
echo "── Stopping services ───────────────────────────────────────────"
systemctl stop featherdeploy 2>/dev/null || true
systemctl stop rqlite        2>/dev/null || true
systemctl disable featherdeploy 2>/dev/null || true
systemctl disable rqlite        2>/dev/null || true
# Kill any remaining processes owned by the service user
pkill -9 -u "$SVC_USER" 2>/dev/null || true
sleep 1
echo "  ✓ services stopped"

# ── 2. Remove systemd units ───────────────────────────────────────────────────
echo ""
echo "── Removing systemd units ──────────────────────────────────────"
rm -f /etc/systemd/system/featherdeploy.service
rm -f /etc/systemd/system/rqlite.service
systemctl daemon-reload
echo "  ✓ systemd units removed"

# ── 3. Remove binaries ────────────────────────────────────────────────────────
echo ""
echo "── Removing binaries ───────────────────────────────────────────"
rm -f /usr/local/bin/featherdeploy
rm -f /usr/local/bin/rqlite
rm -f /usr/local/bin/rqlited
echo "  ✓ binaries removed"

# ── 4. Remove user and data ───────────────────────────────────────────────────
echo ""
echo "── Removing service user and data ─────────────────────────────"
# Remove all container images/volumes/networks created by the service user
# before deleting the user account.
if id -u "$SVC_USER" &>/dev/null; then
  mkdir -p /run/featherdeploy-runtime
  chown "$SVC_USER:$SVC_USER" /run/featherdeploy-runtime 2>/dev/null || true
  su -s /bin/sh "$SVC_USER" -c \
    "HOME=/var/lib/featherdeploy XDG_RUNTIME_DIR=/run/featherdeploy-runtime podman system reset --force 2>/dev/null" \
    || true
  userdel -r "$SVC_USER" 2>/dev/null || true
  echo "  ✓ user '$SVC_USER' removed"
fi
# Remove data dir in case userdel didn't (home may be non-default path)
rm -rf /var/lib/featherdeploy
rm -rf /home/featherdeploy        # clean up if home was incorrectly set here
rm -rf /run/featherdeploy-runtime
echo "  ✓ data directories removed"

# ── 5. Remove config ──────────────────────────────────────────────────────────
echo ""
echo "── Removing config ─────────────────────────────────────────────"
rm -rf /etc/featherdeploy
echo "  ✓ config removed"

# ── 6. Clean up Caddy config ──────────────────────────────────────────────────
echo ""
echo "── Cleaning Caddy config ───────────────────────────────────────"
# Remove the featherdeploy services caddy file
rm -f /etc/caddy/featherdeploy-services.caddy
# Remove the import line added to the main Caddyfile
if [ -f /etc/caddy/Caddyfile ]; then
  # Remove the FeatherDeploy import block (the two lines we added)
  sed -i '/# Service domain routing.*FeatherDeploy/d' /etc/caddy/Caddyfile
  sed -i '/import \/etc\/caddy\/featherdeploy-services\.caddy/d' /etc/caddy/Caddyfile
  systemctl reload caddy 2>/dev/null || true
  echo "  ✓ Caddy config cleaned"
fi

# ── 7. Remove subuid/subgid entries ──────────────────────────────────────────
echo ""
echo "── Removing subuid/subgid entries ─────────────────────────────"
sed -i "/^${SVC_USER}:/d" /etc/subuid 2>/dev/null || true
sed -i "/^${SVC_USER}:/d" /etc/subgid 2>/dev/null || true
echo "  ✓ subuid/subgid entries removed"

echo ""
echo "════════════════════════════════════════════════════════════════"
echo "  ✓ FeatherDeploy fully uninstalled."
echo ""
echo "  To reinstall:"
echo "    curl -fsSL https://raw.githubusercontent.com/ojhapranjal26/FeatherDeploy/main/build.sh | sudo bash"
echo "    sudo featherdeploy install"
echo "════════════════════════════════════════════════════════════════"
echo ""
