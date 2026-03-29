#!/usr/bin/env bash
# FeatherDeploy — complete uninstall / fresh reset script
# Run as root: sudo bash uninstall.sh
# Or piped:    curl -fsSL https://.../uninstall.sh | sudo bash

set -euo pipefail

SVC_USER="featherdeploy"
INSTALL_DIR="/opt/featherdeploy-src"

run_as_user_session() {
  local user="$1"
  shift
  local shell_cmd="$*"
  if command -v systemd-run >/dev/null 2>&1; then
    systemd-run --machine="${user}@" --quiet --user --collect --pipe --wait \
      /bin/sh -lc "cd / && ${shell_cmd}" && return 0
  fi
  su -s /bin/sh "$user" -c "cd / && ${shell_cmd}"
}

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
echo "    - main-server services and node services"
echo "    - FeatherDeploy binaries, config, databases, and source tree"
echo "    - OS user '${SVC_USER}' and all rootless Podman state"
echo "    - podman, crun, netavark, aardvark-dns, slirp4netns, passt"
echo "    - FeatherDeploy-managed Caddy config"
echo ""

if [ -t 0 ]; then
  read -r -p "Type 'yes' to confirm full uninstall: " confirm
else
  read -r -p "Type 'yes' to confirm full uninstall: " confirm </dev/tty
fi

if [ "$confirm" != "yes" ]; then
  echo "Aborted."
  exit 0
fi

echo ""
echo "── Stopping services ───────────────────────────────────────────"
for svc in featherdeploy featherdeploy-node featherdeploy-brain rqlite rqlite-node; do
  systemctl stop "$svc" 2>/dev/null || true
  systemctl disable "$svc" 2>/dev/null || true
done
echo "  ✓ services stopped"

echo ""
echo "── Resetting rootless Podman state ─────────────────────────────"
if id -u "$SVC_USER" >/dev/null 2>&1; then
  svc_uid=$(id -u "$SVC_USER")
  svc_home=$(getent passwd "$SVC_USER" | cut -d: -f6 || echo "/var/lib/featherdeploy")
  mkdir -p "/run/user/${svc_uid}/containers"
  chown -R "$SVC_USER:$SVC_USER" "/run/user/${svc_uid}" 2>/dev/null || true
  chmod 700 "/run/user/${svc_uid}" "/run/user/${svc_uid}/containers" 2>/dev/null || true
  if command -v podman >/dev/null 2>&1; then
    run_as_user_session "$SVC_USER" \
      "HOME=${svc_home} XDG_RUNTIME_DIR=/run/user/${svc_uid} XDG_CONFIG_HOME=${svc_home}/.config XDG_DATA_HOME=${svc_home}/.local/share XDG_CACHE_HOME=${svc_home}/.cache podman system reset --force 2>&1" \
      || true
  fi
  if command -v loginctl >/dev/null 2>&1; then
    loginctl disable-linger "$SVC_USER" 2>/dev/null || true
  fi
  pkill -9 -u "$SVC_USER" 2>/dev/null || true
  echo "  ✓ rootless podman state cleared"
else
  echo "  ✓ service user not present; skipping rootless podman reset"
fi

echo ""
echo "── Removing systemd units ──────────────────────────────────────"
rm -f /etc/systemd/system/featherdeploy.service
rm -f /etc/systemd/system/featherdeploy-node.service
rm -f /etc/systemd/system/featherdeploy-brain.service
rm -f /etc/systemd/system/rqlite.service
rm -f /etc/systemd/system/rqlite-node.service
systemctl daemon-reload
systemctl reset-failed featherdeploy featherdeploy-node featherdeploy-brain rqlite rqlite-node 2>/dev/null || true
echo "  ✓ systemd units removed"

echo ""
echo "── Removing binaries and source tree ───────────────────────────"
rm -f /usr/local/bin/featherdeploy
rm -f /usr/local/bin/featherdeploy-node
rm -f /usr/local/bin/featherdeploy-update
rm -f /usr/local/bin/rqlite
rm -f /usr/local/bin/rqlited
rm -rf "$INSTALL_DIR"
echo "  ✓ binaries and source tree removed"

echo ""
echo "── Removing data and config ────────────────────────────────────"
rm -rf /etc/featherdeploy
rm -rf /var/lib/featherdeploy
rm -rf /home/featherdeploy
rm -rf /run/featherdeploy-runtime
rm -rf /etc/containers
rm -rf /var/lib/containers
rm -rf /var/cache/libpod
rm -f /etc/sudoers.d/featherdeploy-podman
rm -f /etc/sysctl.d/99-featherdeploy.conf
echo "  ✓ config and data removed"

echo ""
echo "── Cleaning Caddy config (keeping Caddy) ───────────────────────"
rm -f /etc/caddy/featherdeploy-services.caddy
if [ -f /etc/caddy/Caddyfile ]; then
  sed -i '/# Service domain routing.*FeatherDeploy/d' /etc/caddy/Caddyfile
  sed -i '/import \/etc\/caddy\/featherdeploy-services\.caddy/d' /etc/caddy/Caddyfile
  systemctl reload caddy 2>/dev/null || true
fi
echo "  ✓ FeatherDeploy Caddy config removed"

echo ""
echo "── Removing service user and subuid/subgid entries ─────────────"
sed -i "/^${SVC_USER}:/d" /etc/subuid 2>/dev/null || true
sed -i "/^${SVC_USER}:/d" /etc/subgid 2>/dev/null || true
rm -f "/var/lib/systemd/linger/${SVC_USER}"
if id -u "$SVC_USER" >/dev/null 2>&1; then
  userdel -r "$SVC_USER" 2>/dev/null || userdel "$SVC_USER" 2>/dev/null || true
fi
echo "  ✓ service user removed"

echo ""
echo "── Removing podman helper packages ─────────────────────────────"
if command -v dnf >/dev/null 2>&1; then
  dnf remove -y podman crun netavark aardvark-dns slirp4netns passt 2>/dev/null || true
elif command -v apt-get >/dev/null 2>&1; then
  apt-get remove -y --purge podman podman-docker crun netavark aardvark-dns slirp4netns passt 2>/dev/null || true
  apt-get autoremove -y 2>/dev/null || true
elif command -v yum >/dev/null 2>&1; then
  yum remove -y podman crun netavark aardvark-dns slirp4netns passt 2>/dev/null || true
elif command -v pacman >/dev/null 2>&1; then
  pacman -Rns --noconfirm podman crun netavark aardvark-dns slirp4netns passt 2>/dev/null || true
elif command -v apk >/dev/null 2>&1; then
  apk del podman crun netavark aardvark-dns slirp4netns passt 2>/dev/null || true
else
  echo "  WARNING: no supported package manager found — remove podman/crun manually"
fi
echo "  ✓ helper package removal attempted"

echo ""
echo "════════════════════════════════════════════════════════════════"
echo "  ✓ FeatherDeploy fully uninstalled."
echo ""
echo "  Fresh reinstall command:"
echo "    curl -fsSL https://raw.githubusercontent.com/ojhapranjal26/FeatherDeploy/main/build.sh | sudo bash"
echo "════════════════════════════════════════════════════════════════"
echo ""
