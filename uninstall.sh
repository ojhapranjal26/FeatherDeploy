#!/usr/bin/env bash
# =============================================================================
#  FeatherDeploy -- uninstaller script
# =============================================================================
set -u

# -- 0. Must run as root
if [ "$(id -u)" -ne 0 ]; then
  echo "ERROR: This script must be run as root (use sudo)." >&2
  exit 1
fi

echo ""
echo "  +=====================================================+"
# shellcheck disable=SC2016
echo "  |          FeatherDeploy  --  Uninstaller             |"
echo "  +=====================================================+"
echo ""

printf "  This will stop all services and delete ALL FeatherDeploy data.\n"
printf "  Type YES to confirm: "
read -r confirm </dev/tty
if [ "$confirm" != "YES" ]; then
  echo "  Aborted."
  exit 0
fi

echo "==> Stopping services..."
SERVICES=("featherdeploy" "featherdeploy-node" "rqlite" "rqlite-node" "etcd" "etcd-node")
for svc in "${SERVICES[@]}"; do
  if systemctl is-active --quiet "$svc"; then
    echo "  Stopping $svc..."
    systemctl stop "$svc" 2>/dev/null || true
  fi
  if [ -f "/etc/systemd/system/$svc.service" ]; then
    echo "  Disabling $svc..."
    systemctl disable "$svc" 2>/dev/null || true
    rm -f "/etc/systemd/system/$svc.service"
  fi
done

echo "==> Reloading systemd..."
systemctl daemon-reload
systemctl reset-failed

echo "==> Removing binaries..."
rm -f /usr/local/bin/featherdeploy
rm -f /usr/local/bin/featherdeploy-node
rm -f /usr/local/bin/featherdeploy-update
rm -f /usr/local/bin/rqlite
rm -f /usr/local/bin/rqlited
rm -f /usr/local/bin/etcd
rm -f /usr/local/bin/etcdctl

echo "==> Removing data and configuration..."
rm -rf /etc/featherdeploy
rm -rf /var/lib/featherdeploy
rm -rf /opt/featherdeploy-src

echo "==> Cleaning up Nginx configurations..."
# We only remove FeatherDeploy-managed configs if they exist
if [ -d "/etc/nginx/sites-enabled" ]; then
  find /etc/nginx/sites-enabled -lname '/etc/featherdeploy/*' -delete 2>/dev/null || true
fi

echo "==> Removing system user (optional)..."
if id "featherdeploy" &>/dev/null; then
  userdel featherdeploy 2>/dev/null || true
  echo "  User 'featherdeploy' removed."
fi

echo ""
echo "==> FeatherDeploy has been uninstalled successfully."
echo ""
