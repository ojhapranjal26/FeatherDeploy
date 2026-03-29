#!/bin/bash
# FeatherDeploy — complete uninstall script
# Run as root: sudo bash uninstall.sh
#
# This removes ALL FeatherDeploy components:
#   - systemd services (featherdeploy, rqlite)
#   - binaries (/usr/local/bin/featherdeploy, rqlite, rqlited)
#   - service user (featherdeploy) and all its data
#   - config files (/etc/featherdeploy/)
#   - Caddy FeatherDeploy config lines
#   - container images/networks created by FeatherDeploy
#
# DATA WARNING: This is irreversible. All deployed services, databases,
# deployment history, and project config will be permanently deleted.

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
