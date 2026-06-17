#!/usr/bin/env bash
set -u

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

run() {
  "$@" >/dev/null 2>&1 || true
}

quarantine_path() {
  path="$1"
  exists_or_link "$path" || return 0
  dest="$QUARANTINE_DIR/files$path"
  if exists_or_link "$dest"; then
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
  mv "$path" "$dest" 2>/dev/null && log "moved $path -> $dest" || log "WARN failed to move $path"
}

yaml_set() {
  file="$1"
  key="$2"
  value="$3"
  [ -f "$file" ] || return 0
  if grep -qE "^${key}:" "$file" 2>/dev/null; then
    sed -i -E "s|^${key}:.*|${key}: ${value}|" "$file" 2>/dev/null || true
  else
    printf '%s: %s\n' "$key" "$value" >> "$file"
  fi
}

harden_nezha_config() {
  file="$1"
  [ -f "$file" ] || return 0
  cp -a "$file" "$QUARANTINE_DIR/meta/$(basename "$file").before-hardening.$(date -u +%Y%m%dT%H%M%SZ)" 2>/dev/null || true
  yaml_set "$file" disable_command_execute true
  yaml_set "$file" disable_force_update true
  yaml_set "$file" disable_auto_update true
  yaml_set "$file" disable_send_query true
  yaml_set "$file" self_update_period 0
  log "hardened Nezha config $file"
}

kill_pattern() {
  pattern="$1"
  pgrep -af "$pattern" 2>/dev/null | while read -r pid rest; do
    case "$pid" in ''|*[!0-9]*) continue ;; esac
    log "killing process pid=$pid cmd=$rest"
    kill -TERM "$pid" 2>/dev/null || true
  done || true
  sleep 1
  pgrep -af "$pattern" 2>/dev/null | while read -r pid rest; do
    case "$pid" in ''|*[!0-9]*) continue ;; esac
    kill -KILL "$pid" 2>/dev/null || true
  done || true
}

cleanup_systemlog() {
  log "cleaning known systemlog/Mirai artifacts"
  run systemctl stop systemlog.service
  run systemctl disable systemlog.service
  kill_pattern '/opt/systemlog/SystemLoger|/tmp/b|/tmp/\.a|/tmp/log_de\.log'

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
  log "checking Nezha agents"
  normal_config="/opt/nezha/agent/config.yml"
  normal_service="/etc/systemd/system/nezha-agent.service"
  server=""
  remove_normal=0

  for path in \
    /etc/systemd/system/nezha-agent-*.service \
    /etc/systemd/system/multi-user.target.wants/nezha-agent-*.service \
    /opt/nezha/agent/config-*.yml \
    /opt/nezha/agent/config-*.yaml; do
    exists_or_link "$path" || continue
    unit="$(basename "$path")"
    run systemctl stop "$unit"
    run systemctl disable "$unit"
    quarantine_path "$path"
  done

  if [ -f "$normal_config" ]; then
    server="$(awk -F': ' '/^server:/ {print $2; exit}' "$normal_config" 2>/dev/null || true)"
    log "found normal Nezha config server=${server:-unknown}"
  fi

  if [ "$REMOVE_ALL_NEZHA" = "1" ]; then
    remove_normal=1
  elif [ -f "$normal_config" ] && grep -Eq '207\.58\.173\.192|24\.144\.123\.109|103\.106\.228\.23|disable_command_execute:[[:space:]]*false|disable_force_update:[[:space:]]*false' "$normal_config" 2>/dev/null && [ "$server" != "$APPROVED_NEZHA_SERVER" ]; then
    remove_normal=1
  elif [ -f "$normal_config" ] && [ "$server" != "$APPROVED_NEZHA_SERVER" ] && [ "$REMOVE_UNAPPROVED_NEZHA" = "1" ]; then
    remove_normal=1
  fi

  if [ "$remove_normal" = "1" ]; then
    log "removing normal Nezha agent"
    run systemctl stop nezha-agent.service
    run systemctl disable nezha-agent.service
    kill_pattern '/opt/nezha/agent/nezha-agent'
    quarantine_path /etc/systemd/system/nezha-agent.service
    quarantine_path /etc/systemd/system/multi-user.target.wants/nezha-agent.service
    quarantine_path /opt/nezha
    quarantine_path /tmp/nezha-agent
    return 0
  fi

  if [ "$KEEP_APPROVED_NEZHA" = "1" ] && [ -f "$normal_config" ] && [ "$server" = "$APPROVED_NEZHA_SERVER" ]; then
    harden_nezha_config "$normal_config"
    run systemctl daemon-reload
    if [ -f "$normal_service" ]; then
      run systemctl enable nezha-agent.service
      run systemctl restart nezha-agent.service
      log "kept and restarted approved Nezha agent"
    fi
    pgrep -af '[n]ezha-agent' 2>/dev/null | while read -r pid rest; do
      case "$pid" in ''|*[!0-9]*) continue ;; esac
      case "$rest" in *"/opt/nezha/agent/config.yml"*) ;; *) log "killing unapproved Nezha process pid=$pid cmd=$rest"; kill -TERM "$pid" 2>/dev/null || true ;; esac
    done || true
  elif [ -f "$normal_config" ]; then
    harden_nezha_config "$normal_config"
    log "left unapproved normal Nezha agent in place; set REMOVE_UNAPPROVED_NEZHA=1 to remove it"
  fi
}

install_firewall() {
  [ "$APPLY_FIREWALL" = "1" ] || { log "skipping firewall hardening because APPLY_FIREWALL=$APPLY_FIREWALL"; return 0; }
  log "installing outbound host and Docker egress blocks"

  cat > /usr/local/sbin/incident-egress-block.sh <<'EOF'
#!/bin/sh
set -u
add4() { chain="$1"; shift; iptables -C "$chain" "$@" 2>/dev/null || iptables -I "$chain" 1 "$@" 2>/dev/null || true; }
add6() { chain="$1"; shift; ip6tables -C "$chain" "$@" 2>/dev/null || ip6tables -I "$chain" 1 "$@" 2>/dev/null || true; }
if command -v iptables >/dev/null 2>&1; then
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
  add6 OUTPUT -p tcp --dport 23 -j REJECT
  add6 OUTPUT -p tcp --dport 2323 -j REJECT
  if ip6tables -S DOCKER-USER >/dev/null 2>&1; then
    add6 DOCKER-USER -p tcp --dport 23 -j REJECT
    add6 DOCKER-USER -p tcp --dport 2323 -j REJECT
  fi
fi
EOF
  chmod 0755 /usr/local/sbin/incident-egress-block.sh

  cat > /etc/systemd/system/incident-egress-block.service <<'EOF'
[Unit]
Description=Incident response outbound Telnet and C2 blocks
After=network-online.target docker.service
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=/usr/local/sbin/incident-egress-block.sh
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
EOF
  run systemctl daemon-reload
  /usr/local/sbin/incident-egress-block.sh || true
  run systemctl enable incident-egress-block.service
  run systemctl start incident-egress-block.service

  if command -v ufw >/dev/null 2>&1; then
    ufw deny out 23/tcp >/dev/null 2>&1 || true
    ufw deny out 2323/tcp >/dev/null 2>&1 || true
    for ip in $IOC_IPS; do ufw deny out to "$ip" >/dev/null 2>&1 || true; done
  fi
}

validate_cleanup() {
  failed=0
  log "validating cleanup"
  if ps auxww | grep -Ei 'systemlog|SystemLoger|/tmp/b|/tmp/\.a|jjdjiysiys|103\.106\.228\.23|207\.58\.173\.192|24\.144\.123\.109' | grep -v grep >/dev/null 2>&1; then
    log "ERROR suspicious process still present"
    ps auxww | grep -Ei 'systemlog|SystemLoger|/tmp/b|/tmp/\.a|jjdjiysiys|103\.106\.228\.23|207\.58\.173\.192|24\.144\.123\.109' | grep -v grep | tee -a "$MANIFEST"
    failed=1
  fi
  for path in /etc/systemd/system/systemlog.service /etc/systemd/system/multi-user.target.wants/systemlog.service /opt/systemlog /tmp/.a /tmp/b /tmp/SystemLog.log; do
    if exists_or_link "$path"; then log "ERROR active bad path remains: $path"; failed=1; fi
  done
  chmod -R go-rwx "$QUARANTINE_DIR" 2>/dev/null || true
  log "quarantine directory: $QUARANTINE_DIR"
  [ "$failed" -eq 0 ] && log "cleanup complete" || log "cleanup completed with warnings"
  return "$failed"
}

main() {
  log "starting incident cleanup v3"
  log "approved Nezha server: $APPROVED_NEZHA_SERVER"
  cleanup_systemlog
  cleanup_nezha
  run systemctl daemon-reload
  install_firewall
  validate_cleanup
}

main "$@"
