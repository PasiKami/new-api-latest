#!/usr/bin/env bash
set -euo pipefail

# Ubuntu incident cleanup for the 2026 Nezha exploitation chain.
# Defaults:
# - keep the approved Nezha agent server and harden its config
# - remove known malicious services/payloads
# - block Telnet abuse and observed C2 IPs for host and Docker egress
# - intentionally does not modify SSH configuration

APPROVED_NEZHA_SERVER="${APPROVED_NEZHA_SERVER:-nezha.kamipasi.top:443}"
KEEP_APPROVED_NEZHA="${KEEP_APPROVED_NEZHA:-1}"
REMOVE_ALL_NEZHA="${REMOVE_ALL_NEZHA:-0}"
REMOVE_UNAPPROVED_NEZHA="${REMOVE_UNAPPROVED_NEZHA:-0}"
APPLY_FIREWALL="${APPLY_FIREWALL:-1}"
QUARANTINE_DIR="${QUARANTINE_DIR:-/root/incident-quarantine-$(date -u +%Y%m%dT%H%M%SZ)}"

IOC_IPS="207.58.173.192 24.144.123.109 103.106.228.23"

if [ "$(id -u)" -ne 0 ]; then
  echo "ERROR: run as root" >&2
  exit 1
fi

mkdir -p "$QUARANTINE_DIR/files" "$QUARANTINE_DIR/meta"
MANIFEST="$QUARANTINE_DIR/MANIFEST.txt"

log() {
  printf '%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" | tee -a "$MANIFEST"
}

exists_or_link() {
  [ -e "$1" ] || [ -L "$1" ]
}

quarantine_path() {
  local path="$1"
  local dest
  exists_or_link "$path" || return 0
  dest="$QUARANTINE_DIR/files$path"
  if [ -e "$dest" ] || [ -L "$dest" ]; then
    dest="$dest.$(date -u +%s)"
  fi
  mkdir -p "$(dirname "$dest")"
  if [ -f "$path" ]; then
    {
      printf 'HASH %s\n' "$path"
      sha256sum "$path" 2>/dev/null || true
      stat -c '%A %U:%G %s bytes mtime=%y ctime=%z' "$path" 2>/dev/null || true
    } >> "$MANIFEST"
  fi
  mv "$path" "$dest"
  log "moved $path -> $dest"
}

systemctl_quiet() {
  systemctl "$@" >/dev/null 2>&1 || true
}

yaml_set() {
  local file="$1"
  local key="$2"
  local value="$3"
  if grep -qE "^${key}:" "$file"; then
    sed -i -E "s|^${key}:.*|${key}: ${value}|" "$file"
  else
    printf '%s: %s\n' "$key" "$value" >> "$file"
  fi
}

get_nezha_server() {
  local file="$1"
  awk -F': ' '/^server:/ {print $2; exit}' "$file" 2>/dev/null || true
}

is_known_bad_nezha_config() {
  local file="$1"
  grep -Eq '207\.58\.173\.192|24\.144\.123\.109|103\.106\.228\.23|disable_command_execute:[[:space:]]*false|disable_force_update:[[:space:]]*false' "$file" 2>/dev/null
}

harden_nezha_config() {
  local file="$1"
  [ -f "$file" ] || return 0
  cp -a "$file" "$QUARANTINE_DIR/meta/$(basename "$file").before-hardening.$(date -u +%Y%m%dT%H%M%SZ)"
  yaml_set "$file" disable_command_execute true
  yaml_set "$file" disable_force_update true
  yaml_set "$file" disable_auto_update true
  yaml_set "$file" disable_send_query true
  yaml_set "$file" self_update_period 0
  log "hardened Nezha config $file"
}

kill_matching_processes() {
  local pattern="$1"
  pgrep -af "$pattern" 2>/dev/null | while read -r pid rest; do
    case "$pid" in
      ''|*[!0-9]*) continue ;;
    esac
    log "killing process pid=$pid cmd=$rest"
    kill -TERM "$pid" 2>/dev/null || true
  done
  sleep 1
  pgrep -af "$pattern" 2>/dev/null | while read -r pid rest; do
    case "$pid" in
      ''|*[!0-9]*) continue ;;
    esac
    kill -KILL "$pid" 2>/dev/null || true
  done
}

cleanup_systemlog_and_payloads() {
  log "cleaning known systemlog/Mirai artifacts"
  systemctl_quiet stop systemlog.service
  systemctl_quiet disable systemlog.service
  kill_matching_processes '/opt/systemlog/SystemLoger|/tmp/b|/tmp/\.a|/tmp/log_de\.log'

  quarantine_path /etc/systemd/system/systemlog.service
  quarantine_path /etc/systemd/system/multi-user.target.wants/systemlog.service
  quarantine_path /opt/systemlog
  quarantine_path /root/SystemLoger.quarantine
  quarantine_path /root/systemlog.service.quarantine
  quarantine_path /tmp/.a
  quarantine_path /tmp/b
  quarantine_path /tmp/SystemLog.log
  quarantine_path /tmp/log_de.log
}

cleanup_nezha() {
  local normal_config="/opt/nezha/agent/config.yml"
  local normal_service="/etc/systemd/system/nezha-agent.service"
  local server=""
  local remove_normal=0

  log "checking Nezha agents"

  # Always quarantine randomly named or additional Nezha agent units/configs.
  for path in \
    /etc/systemd/system/nezha-agent-*.service \
    /etc/systemd/system/multi-user.target.wants/nezha-agent-*.service \
    /opt/nezha/agent/config-*.yml \
    /opt/nezha/agent/config-*.yaml; do
    if exists_or_link "$path"; then
      systemctl_quiet stop "$(basename "$path")"
      systemctl_quiet disable "$(basename "$path")"
      quarantine_path "$path"
    fi
  done

  if [ -f "$normal_config" ]; then
    server="$(get_nezha_server "$normal_config")"
    log "found normal Nezha config server=${server:-unknown}"
  fi

  if [ "$REMOVE_ALL_NEZHA" = "1" ]; then
    remove_normal=1
  elif [ -f "$normal_config" ] && is_known_bad_nezha_config "$normal_config" && [ "$server" != "$APPROVED_NEZHA_SERVER" ]; then
    remove_normal=1
  elif [ -f "$normal_config" ] && [ "$server" != "$APPROVED_NEZHA_SERVER" ] && [ "$REMOVE_UNAPPROVED_NEZHA" = "1" ]; then
    remove_normal=1
  fi

  if [ "$remove_normal" = "1" ]; then
    log "removing normal Nezha agent"
    systemctl_quiet stop nezha-agent.service
    systemctl_quiet disable nezha-agent.service
    kill_matching_processes '/opt/nezha/agent/nezha-agent'
    quarantine_path /etc/systemd/system/nezha-agent.service
    quarantine_path /etc/systemd/system/multi-user.target.wants/nezha-agent.service
    quarantine_path /opt/nezha
    quarantine_path /tmp/nezha-agent
    return 0
  fi

  if [ "$KEEP_APPROVED_NEZHA" = "1" ] && [ -f "$normal_config" ] && [ "$server" = "$APPROVED_NEZHA_SERVER" ]; then
    harden_nezha_config "$normal_config"
    systemctl daemon-reload
    if [ -f "$normal_service" ]; then
      systemctl enable nezha-agent.service >/dev/null 2>&1 || true
      systemctl restart nezha-agent.service >/dev/null 2>&1 || true
      log "kept and restarted approved Nezha agent"
    fi
    # Kill orphaned Nezha processes that are not using the approved config path.
    pgrep -af '[n]ezha-agent' 2>/dev/null | while read -r pid rest; do
      case "$rest" in
        *"/opt/nezha/agent/config.yml"*) ;;
        *)
          log "killing unapproved Nezha process pid=$pid cmd=$rest"
          kill -TERM "$pid" 2>/dev/null || true
          ;;
      esac
    done
  elif [ -f "$normal_config" ]; then
    harden_nezha_config "$normal_config"
    log "left unapproved normal Nezha agent in place; set REMOVE_UNAPPROVED_NEZHA=1 to remove it"
  fi
}

install_firewall_blocks() {
  [ "$APPLY_FIREWALL" = "1" ] || {
    log "skipping firewall hardening because APPLY_FIREWALL=$APPLY_FIREWALL"
    return 0
  }

  log "installing host and Docker egress blocks"
  cat > /usr/local/sbin/incident-egress-block.sh <<'EOF'
#!/bin/sh
set -eu

add4() {
  chain="$1"
  shift
  iptables -C "$chain" "$@" 2>/dev/null || iptables -I "$chain" 1 "$@"
}

add6() {
  chain="$1"
  shift
  ip6tables -C "$chain" "$@" 2>/dev/null || ip6tables -I "$chain" 1 "$@"
}

if command -v iptables >/dev/null 2>&1; then
  add4 INPUT -p tcp --dport 23 -j DROP
  add4 INPUT -p tcp --dport 2323 -j DROP
  add4 OUTPUT -p tcp --dport 23 -j REJECT
  add4 OUTPUT -p tcp --dport 2323 -j REJECT
  add4 OUTPUT -d 207.58.173.192/32 -j REJECT
  add4 OUTPUT -d 24.144.123.109/32 -j REJECT
  add4 OUTPUT -d 103.106.228.23/32 -j REJECT
  if iptables -S DOCKER-USER >/dev/null 2>&1; then
    add4 DOCKER-USER -p tcp --dport 23 -j REJECT
    add4 DOCKER-USER -p tcp --dport 2323 -j REJECT
    add4 DOCKER-USER -d 207.58.173.192/32 -j REJECT
    add4 DOCKER-USER -d 24.144.123.109/32 -j REJECT
    add4 DOCKER-USER -d 103.106.228.23/32 -j REJECT
  fi
fi

if command -v ip6tables >/dev/null 2>&1; then
  add6 INPUT -p tcp --dport 23 -j DROP || true
  add6 INPUT -p tcp --dport 2323 -j DROP || true
  add6 OUTPUT -p tcp --dport 23 -j REJECT || true
  add6 OUTPUT -p tcp --dport 2323 -j REJECT || true
  if ip6tables -S DOCKER-USER >/dev/null 2>&1; then
    add6 DOCKER-USER -p tcp --dport 23 -j REJECT || true
    add6 DOCKER-USER -p tcp --dport 2323 -j REJECT || true
  fi
fi
EOF
  chmod 0755 /usr/local/sbin/incident-egress-block.sh

  cat > /etc/systemd/system/incident-egress-block.service <<'EOF'
[Unit]
Description=Incident response Telnet and C2 egress blocks
After=network-online.target docker.service
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=/usr/local/sbin/incident-egress-block.sh
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
  /usr/local/sbin/incident-egress-block.sh || true
  systemctl enable incident-egress-block.service >/dev/null 2>&1 || true
  systemctl start incident-egress-block.service >/dev/null 2>&1 || true

  if command -v ufw >/dev/null 2>&1; then
    ufw deny in 23/tcp >/dev/null 2>&1 || true
    ufw deny in 2323/tcp >/dev/null 2>&1 || true
    ufw deny out 23/tcp >/dev/null 2>&1 || true
    ufw deny out 2323/tcp >/dev/null 2>&1 || true
    for ip in $IOC_IPS; do
      ufw deny out to "$ip" >/dev/null 2>&1 || true
    done
  fi
}

validate_cleanup() {
  local failed=0
  log "validating cleanup"

  if ps auxww | grep -Ei 'systemlog|SystemLoger|/tmp/b|/tmp/\.a|jjdjiysiys|103\.106\.228\.23|207\.58\.173\.192|24\.144\.123\.109' | grep -v grep >/dev/null; then
    log "ERROR suspicious process still present"
    ps auxww | grep -Ei 'systemlog|SystemLoger|/tmp/b|/tmp/\.a|jjdjiysiys|103\.106\.228\.23|207\.58\.173\.192|24\.144\.123\.109' | grep -v grep | tee -a "$MANIFEST"
    failed=1
  fi

  for path in \
    /etc/systemd/system/systemlog.service \
    /etc/systemd/system/multi-user.target.wants/systemlog.service \
    /opt/systemlog \
    /tmp/.a \
    /tmp/b \
    /tmp/SystemLog.log; do
    if exists_or_link "$path"; then
      log "ERROR active bad path remains: $path"
      failed=1
    fi
  done

  if [ "$KEEP_APPROVED_NEZHA" = "1" ] && [ "$REMOVE_ALL_NEZHA" != "1" ] && [ -f /opt/nezha/agent/config.yml ]; then
    if grep -Fxq "server: ${APPROVED_NEZHA_SERVER}" /opt/nezha/agent/config.yml; then
      systemctl is-active nezha-agent.service >/dev/null 2>&1 && log "approved Nezha agent active" || log "approved Nezha agent not active"
    fi
  fi

  chmod -R go-rwx "$QUARANTINE_DIR"
  log "quarantine directory: $QUARANTINE_DIR"
  return "$failed"
}

main() {
  log "starting incident cleanup"
  log "approved Nezha server: $APPROVED_NEZHA_SERVER"
  cleanup_systemlog_and_payloads
  cleanup_nezha
  systemctl daemon-reload
  install_firewall_blocks
  validate_cleanup
  log "cleanup complete"
}

main "$@"
