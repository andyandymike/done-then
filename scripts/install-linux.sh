#!/usr/bin/env bash
set -euo pipefail

action="plan"
apply="false"
install_user="${SUDO_USER:-${USER:-}}"

usage() {
  cat <<'EOF'
Usage: install-linux.sh [--action plan|install|uninstall] [--user LOGIN] [--apply]

The default action is plan and performs no writes. Install and uninstall require
--apply and root privileges. An unresolved helper power job blocks both changes.
EOF
}

while (($#)); do
  case "$1" in
    --action)
      [[ $# -ge 2 ]] || { usage >&2; exit 2; }
      action="$2"
      shift 2
      ;;
    --user)
      [[ $# -ge 2 ]] || { usage >&2; exit 2; }
      install_user="$2"
      shift 2
      ;;
    --apply)
      apply="true"
      shift
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      printf 'Unknown argument: %s\n' "$1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

case "$action" in
  plan|install|uninstall) ;;
  *) printf 'Unsupported action: %s\n' "$action" >&2; exit 2 ;;
esac

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
cli_source="$script_dir/donethen"
helper_source="$script_dir/donethen-powerd"
service_source="$script_dir/donethen-powerd.service"
tmpfiles_source="$script_dir/donethen.conf"
cli_target="/usr/local/bin/donethen"
helper_dir="/usr/local/libexec/donethen"
helper_target="$helper_dir/donethen-powerd"
service_target="/etc/systemd/system/donethen-powerd.service"
tmpfiles_target="/usr/lib/tmpfiles.d/donethen.conf"
active_state="/var/lib/donethen/active.json"

print_plan() {
  printf 'DoneThen Linux helper plan (no changes)\n'
  printf '  action: %s\n' "${1:-install}"
  printf '  user: %s\n' "${install_user:-<required>}"
  printf '  CLI: %s\n' "$cli_target"
  printf '  helper: %s\n' "$helper_target"
  printf '  service: %s\n' "$service_target"
  printf '  socket: /run/donethen/powerd.sock (root:donethen 0660)\n'
  printf '  state retained on uninstall: /var/lib/donethen\n'
  printf 'Re-run with --action %s --apply as root to mutate the host.\n' "${1:-install}"
}

if [[ "$action" == "plan" ]]; then
  print_plan install
  exit 0
fi
if [[ "$apply" != "true" ]]; then
  print_plan "$action"
  exit 2
fi
if [[ ${EUID:-$(id -u)} -ne 0 ]]; then
  printf '%s requires root privileges.\n' "$action" >&2
  exit 1
fi

runtime_dir="/run/donethen"
power_lock="$runtime_dir/power.lock"
install -d -o root -g root -m 0755 "$runtime_dir"
if [[ -L "$power_lock" || ( -e "$power_lock" && ! -f "$power_lock" ) ]]; then
  printf 'Refusing unsafe machine lock path: %s\n' "$power_lock" >&2
  exit 1
fi
if [[ ! -e "$power_lock" ]]; then
  install -o root -g root -m 0600 /dev/null "$power_lock"
fi
if [[ "$(stat -c '%u' "$power_lock")" != "0" ]]; then
  printf 'Machine lock must be root-owned: %s\n' "$power_lock" >&2
  exit 1
fi
exec {power_lock_fd}<>"$power_lock"
if ! flock -n "$power_lock_fd"; then
  printf 'Another DoneThen power operation is active; retry later.\n' >&2
  exit 1
fi
if [[ -e "$active_state" ]]; then
  printf 'Refusing to %s while %s exists. Cancel or reconcile the power job first.\n' "$action" "$active_state" >&2
  exit 1
fi

case "$action" in
  install)
    [[ -n "$install_user" ]] || { printf '%s\n' '--user LOGIN is required.' >&2; exit 2; }
    getent passwd "$install_user" >/dev/null || { printf 'Unknown login: %s\n' "$install_user" >&2; exit 2; }
    for source in "$cli_source" "$helper_source" "$service_source" "$tmpfiles_source"; do
      [[ -f "$source" && ! -L "$source" ]] || { printf 'Missing or unsafe package file: %s\n' "$source" >&2; exit 1; }
    done
    getent group donethen >/dev/null || groupadd --system donethen
    install -d -o root -g root -m 0755 "$helper_dir"
    install -o root -g root -m 0755 "$cli_source" "$cli_target.new"
    install -o root -g root -m 0755 "$helper_source" "$helper_target.new"
    install -o root -g root -m 0644 "$service_source" "$service_target.new"
    install -o root -g root -m 0644 "$tmpfiles_source" "$tmpfiles_target.new"
    if systemctl cat donethen-powerd.service >/dev/null 2>&1; then
      systemctl stop donethen-powerd.service
    fi
    if [[ -e "$active_state" ]]; then
      printf 'Refusing to install after helper stop because %s appeared.\n' "$active_state" >&2
      exit 1
    fi
    mv -f "$cli_target.new" "$cli_target"
    mv -f "$helper_target.new" "$helper_target"
    mv -f "$service_target.new" "$service_target"
    mv -f "$tmpfiles_target.new" "$tmpfiles_target"
    usermod -a -G donethen "$install_user"
    systemd-tmpfiles --create "$tmpfiles_target"
    systemctl daemon-reload
    systemctl enable --now donethen-powerd.service
    printf 'Installed DoneThen. %s must start a new login session before helper access is available.\n' "$install_user"
    ;;
  uninstall)
    if systemctl cat donethen-powerd.service >/dev/null 2>&1; then
      systemctl disable --now donethen-powerd.service
    fi
    if [[ -e "$active_state" ]]; then
      printf 'Refusing to uninstall after helper stop because %s appeared.\n' "$active_state" >&2
      exit 1
    fi
    rm -f -- "$service_target" "$tmpfiles_target" "$helper_target" "$cli_target"
    systemctl daemon-reload
    printf 'Removed DoneThen binaries and service files. Audit state and the donethen group were retained.\n'
    ;;
esac
