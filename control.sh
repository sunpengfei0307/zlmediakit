#!/usr/bin/env bash
# zlm 线上启停：二进制与配置只落在 /data/zlm/{bin,cfg}
# systemd Restart=always：只有 control.sh stop（systemctl stop）才停住；kill/崩溃会再拉起。
set -euo pipefail

DATA="${ZLM_DATA:-/data/zlm}"
BIN="${DATA}/bin"
CFG="${DATA}/cfg"
LOG="${DATA}/log"
SERVER_LOG="${LOG}/zlm-server"
RUN="${DATA}/run"
LOCK="${RUN}/control.lock"
TIMEOUT="${ZLM_CTL_TIMEOUT:-20}"

SERVER_UNIT="zlm-server"
CLIENT_UNIT="zlm-client"
SERVER_BIN="${BIN}/zlm-server"
CLIENT_BIN="${BIN}/zlm-client"
SERVER_INI="${CFG}/zlm-server.ini"
CLIENT_TOML="${CFG}/zlm-client.toml"
CLIENT_BUILD=""

usage() {
  cat <<'EOF'
用法:
  ./control.sh zlm-server  start|stop|restart|reload|status|update
  ./control.sh zlm-client  start|stop|restart|status|update
  ./control.sh zlm         start|stop|restart|reload|status|update

正式线上运行时只使用下列文件和目录:
  /data/zlm/bin/zlm-server  /data/zlm/bin/zlm-client
  /data/zlm/cfg/zlm-server.ini  /data/zlm/cfg/zlm-client.toml
update 会先停掉对应服务，再编译/拷贝到上述目录后拉起。
  zlm-server / zlm-client 只更新自己；zlm 两者一起更新。
systemd Restart=always：只有 control.sh stop 才停住；kill/崩溃会再拉起。
EOF
}

log() { printf '[control] %s\n' "$*"; }
err() { printf '[control] ERROR: %s\n' "$*" >&2; }
die() { err "$*"; exit 1; }

here_dir() {
  cd "$(dirname "${BASH_SOURCE[0]}")" && pwd
}

repo_root() {
  local here parent
  here="$(here_dir)"
  parent="$(cd "${here}/.." && pwd)"
  if [[ -d "${here}/zlm-server" && -d "${here}/zlm-client" ]]; then
    printf '%s\n' "${here}"
  elif [[ -d "${parent}/zlm-server" && -d "${parent}/zlm-client" ]]; then
    printf '%s\n' "${parent}"
  else
    printf '%s\n' "${here}"
  fi
}

REPO="$(repo_root)"
CLIENT_BUILD="${REPO}/zlm-client/.build/zlm-client"

run_to() {
  local sec=$1
  shift
  if command -v timeout >/dev/null 2>&1; then
    timeout "${sec}" "$@"
  else
    "$@"
  fi
}

as_root() {
  if [[ "$(id -u)" -eq 0 ]]; then
    "$@"
  elif command -v sudo >/dev/null 2>&1 && sudo -n true >/dev/null 2>&1; then
    sudo -n "$@"
  else
    "$@"
  fi
}

SYSTEMCTL=()
UNIT_DIR=""
USER_LINE=""
WANTED_BY="multi-user.target"

init_systemd() {
  if [[ "$(id -u)" -eq 0 ]]; then
    SYSTEMCTL=(systemctl)
    UNIT_DIR="/etc/systemd/system"
    USER_LINE=""
    WANTED_BY="multi-user.target"
  elif command -v sudo >/dev/null 2>&1 && sudo -n true >/dev/null 2>&1; then
    SYSTEMCTL=(sudo -n systemctl)
    UNIT_DIR="/etc/systemd/system"
    USER_LINE=""
    WANTED_BY="multi-user.target"
  elif systemctl --user show-environment >/dev/null 2>&1; then
    SYSTEMCTL=(systemctl --user)
    UNIT_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
    USER_LINE=""
    WANTED_BY="default.target"
  else
    die "无法调用 systemctl（需要 root、免密 sudo -n，或可用的 user systemd）"
  fi
}

sysctl_run() {
  run_to "${TIMEOUT}" "${SYSTEMCTL[@]}" "$@"
}

ensure_dirs() {
  as_root mkdir -p "${BIN}" "${CFG}" "${LOG}" "${SERVER_LOG}" "${RUN}" "${DATA}/mp4" "${DATA}/snap"
}

acquire_lock() {
  ensure_dirs
  if [[ -w "${RUN}" ]] && touch "${LOCK}" 2>/dev/null; then
    exec 9>"${LOCK}"
  else
    exec 9>"/tmp/zlm-control.lock"
  fi
  flock -w 60 9 || die "另一个 control.sh 正在执行（flock 超时）"
}

ini_get() {
  local file=$1 section=$2 key=$3
  [[ -f "$file" ]] || return 1
  python3 - "$file" "$section" "$key" <<'PY'
import sys
path, section, key = sys.argv[1], sys.argv[2], sys.argv[3]
sec = None
try:
    lines = open(path, encoding="utf-8", errors="replace")
except OSError:
    sys.exit(1)
for raw in lines:
    s = raw.strip()
    if not s or s.startswith("#") or s.startswith(";"):
        continue
    if s.startswith("[") and s.endswith("]"):
        sec = s[1:-1].split("#", 1)[0].strip()
        continue
    if sec is None or sec.lower() != section.lower() or "=" not in s:
        continue
    k, v = s.split("=", 1)
    if k.strip() == key:
        print(v.strip())
        sys.exit(0)
    sys.exit(1)
PY
}

ini_set() {
  local file=$1 section=$2 key=$3 value=$4
  python3 - "$file" "$section" "$key" "$value" <<'PY'
import sys
path, section, key, value = sys.argv[1:5]
try:
    text = open(path, encoding="utf-8", errors="replace").read().replace("\r\n", "\n")
except OSError:
    sys.exit(1)
lines = text.splitlines(True)
out, sec, found, inserted = [], None, False, False
i = 0
while i < len(lines):
    raw = lines[i]
    s = raw.strip()
    if s.startswith("[") and s.endswith("]"):
        if sec == section and not found:
            out.append("%s=%s\n" % (key, value))
            found, inserted = True, True
        sec = s[1:-1].split("#", 1)[0].strip()
        out.append(raw)
        i += 1
        continue
    if sec == section and "=" in s and not s.startswith("#") and not s.startswith(";"):
        k = s.split("=", 1)[0].strip()
        if k == key:
            nl = "\n" if raw.endswith("\n") else ""
            indent = raw[: len(raw) - len(raw.lstrip())]
            out.append("%s%s=%s%s" % (indent, key, value, nl))
            found = True
            i += 1
            continue
    out.append(raw)
    i += 1
if sec == section and not found:
    out.append("%s=%s\n" % (key, value))
    found = True
if not found:
    if out and not out[-1].endswith("\n"):
        out.append("\n")
    out.append("[%s]\n%s=%s\n" % (section, key, value))
open(path, "w", encoding="utf-8", newline="\n").write("".join(out))
PY
}

patch_server_log_paths() {
  local dest=$1
  [[ -f "$dest" ]] || return 1
  local tmp
  tmp="$(mktemp)"
  as_root cat "$dest" >"$tmp"
  ini_set "$tmp" "log" "dir" "${SERVER_LOG}"
  ini_set "$tmp" "ffmpeg" "log" "${SERVER_LOG}/ffmpeg.log"
  as_root cp -f "$tmp" "$dest"
  rm -f "$tmp"
  log "已写入 ${dest} log.dir=${SERVER_LOG} ffmpeg.log=${SERVER_LOG}/ffmpeg.log"
}

install_file() {
  local src=$1 dest=$2 mode=${3:-}
  [[ -f "$src" ]] || die "缺少源文件: ${src}"
  as_root mkdir -p "$(dirname "$dest")"
  as_root cp -f "$src" "$dest"
  if [[ -n "$mode" ]]; then
    as_root chmod "$mode" "$dest"
  fi
}

install_cfg_if_absent() {
  local src=$1 dest=$2
  if as_root test -f "$dest"; then
    log "保留已有配置 ${dest}"
    return 0
  fi
  install_file "$src" "$dest" "644"
}

find_server_bin() {
  local c
  for c in \
    "${REPO}/zlm-server/zlm-server" \
    "${REPO}/zlm-server/MediaServer" \
    "${REPO}/zlm-server/ZLMediaKit/release/linux/Release/MediaServer"; do
    if [[ -x "$c" ]]; then
      printf '%s\n' "$c"
      return 0
    fi
  done
  return 1
}

patch_client_toml() {
  local src=$1 dest=$2
  python3 - "$src" "$dest" "$SERVER_INI" "$LOG" "$DATA" "$SERVER_BIN" <<'PY'
import sys
src, dest, ini, log_dir, root, bin_path = sys.argv[1:7]
text = open(src, encoding="utf-8", errors="replace").read().replace("\r\n", "\n")

def repl(key, value):
    global text
    out, found, seen_nodes = [], False, False
    for line in text.splitlines(True):
        stripped = line.strip()
        if stripped.startswith("[[") and "nodes" in stripped:
            seen_nodes = True
        if seen_nodes and stripped.startswith(key) and "=" in stripped:
            nl = "\n" if line.endswith("\n") else ""
            indent = line[: len(line) - len(line.lstrip())]
            out.append('%s%s = "%s"%s' % (indent, key, value, nl))
            found = True
            continue
        out.append(line)
    text = "".join(out)
    return found

repl("ini", ini)
repl("log_dir", log_dir)
repl("root", root)
repl("bin", bin_path)
open(dest, "w", encoding="utf-8", newline="\n").write(text)
PY
}

publish_server() {
  local src_bin src_ini src_pem
  log "publish_server -> ${SERVER_BIN}"
  ensure_dirs
  src_bin="$(find_server_bin)" || die "未找到已编译的 zlm-server（MediaServer），请先 ./control.sh zlm-server update"
  src_ini="${REPO}/zlm-server/config.ini"
  [[ -f "$src_ini" ]] || die "缺少 ${src_ini}"

  install_file "$src_bin" "$SERVER_BIN" "755"
  src_pem="${REPO}/zlm-server/default.pem"
  if [[ -f "$src_pem" ]]; then
    install_file "$src_pem" "${BIN}/default.pem" "644"
  fi
  install_cfg_if_absent "$src_ini" "$SERVER_INI"
  patch_server_log_paths "$SERVER_INI"
  log "已发布 ${SERVER_BIN}"
}

publish_client() {
  local src_toml tmp_toml
  log "publish_client -> ${CLIENT_BIN}"
  ensure_dirs
  src_toml="${REPO}/zlm-client/core/config/config.toml"
  [[ -f "$src_toml" ]] || die "缺少 ${src_toml}"

  if as_root test -f "$CLIENT_TOML"; then
    log "保留已有配置 ${CLIENT_TOML}"
  else
    tmp_toml="$(mktemp)"
    patch_client_toml "$src_toml" "$tmp_toml"
    install_file "$tmp_toml" "$CLIENT_TOML" "644"
    rm -f "$tmp_toml"
  fi

  if [[ -x "${CLIENT_BUILD}" ]]; then
    install_file "${CLIENT_BUILD}" "$CLIENT_BIN" "755"
  elif [[ -x "$CLIENT_BIN" ]]; then
    log "沿用已有 ${CLIENT_BIN}"
  else
    die "未找到已编译的 zlm-client，请先 ./control.sh zlm-client update"
  fi
  log "已发布 ${CLIENT_BIN}"
}

publish_runtime() {
  publish_server
  publish_client
}

render_unit() {
  local name=$1
  local src="${REPO}/thr3parts/systemd/${name}.service.in"
  local dest="${UNIT_DIR}/${name}.service"
  local tmp
  [[ -f "$src" ]] || die "缺少 unit 模板 ${src}"
  tmp="$(mktemp)"
  sed \
    -e "s|@BIN@|${BIN}|g" \
    -e "s|@CFG@|${CFG}|g" \
    -e "s|@DATA@|${DATA}|g" \
    -e "s|@USER_LINE@|${USER_LINE}|g" \
    -e "s|@WANTED_BY@|${WANTED_BY}|g" \
    "$src" >"$tmp"
  as_root mkdir -p "$UNIT_DIR"
  as_root cp -f "$tmp" "$dest"
  rm -f "$tmp"
  log "已安装 ${dest}"
}

install_units() {
  init_systemd
  render_unit "$SERVER_UNIT"
  render_unit "$CLIENT_UNIT"
  sysctl_run daemon-reload
}

retire_snapd() {
  pkill -f '(^|[[:space:]/])snapd\.sh([[:space:]]|$)' 2>/dev/null || true
}

zlm_api() {
  local api=$1
  shift || true
  case "$api" in
    getServerConfig | reloadServerConfig | setServerConfig) ;;
    *) die "不支持的 ZLM API: ${api}" ;;
  esac
  command -v curl >/dev/null 2>&1 || return 1
  local port secret url
  port="$(ini_get "$SERVER_INI" http port 2>/dev/null || true)"
  port="${port:-8090}"
  secret="$(ini_get "$SERVER_INI" api secret 2>/dev/null || true)"
  url="http://127.0.0.1:${port}/index/api/${api}"
  local args=(curl -fsS -G --data-urlencode "secret=${secret}")
  local kv
  for kv in "$@"; do
    args+=(--data-urlencode "$kv")
  done
  run_to 8 "${args[@]}" "$url"
}

wait_server_api() {
  local i
  for ((i = 1; i <= 20; i++)); do
    if zlm_api getServerConfig >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.5
  done
  return 1
}

unit_is_active() {
  sysctl_run is-active --quiet "$1" 2>/dev/null
}

do_start() {
  local name=$1
  [[ -x "${BIN}/${name}" ]] || die "缺少可执行文件 ${BIN}/${name}，请先 ./control.sh ${name} update"
  if [[ "$name" == "$SERVER_UNIT" && -f "$SERVER_INI" ]]; then
    ensure_dirs
    patch_server_log_paths "$SERVER_INI"
  fi
  install_units
  sysctl_run reset-failed "${name}.service" 2>/dev/null || true
  sysctl_run enable "${name}.service" >/dev/null
  sysctl_run start "${name}.service"
  log "${name} started (Restart=always；停止请用 control.sh stop)"
}

do_stop() {
  local name=$1
  init_systemd
  # 必须走 systemctl stop：kill 会被 Restart=always 立刻拉起
  sysctl_run stop "${name}.service" || true
  log "${name} stopped"
}

do_restart() {
  local name=$1
  do_stop "$name"
  do_start "$name"
}

do_status() {
  local name=$1
  init_systemd
  echo "===== ${name} ====="
  sysctl_run status --no-pager --full "${name}.service" || true
}

do_reload_server() {
  if ! unit_is_active "${SERVER_UNIT}.service"; then
    die "zlm-server 未运行，无法 reload"
  fi
  local out
  # 按磁盘上的 zlm-server.ini 热加载；个别键也可走 setServerConfig
  if out="$(zlm_api reloadServerConfig)"; then
    log "reloadServerConfig ok: ${out}"
    return 0
  fi
  err "reloadServerConfig 失败，尝试 setServerConfig 探活"
  zlm_api setServerConfig >/dev/null || die "zlm-server reload 失败"
  log "setServerConfig 已调用（未改键值）；请确认 ini 已生效"
}

ensure_server_runtime() {
  if [[ -x "$SERVER_BIN" && -f "$SERVER_INI" ]]; then
    return 0
  fi
  log "zlm-server 运行文件不完整，尝试 publish_runtime"
  publish_runtime
}

ensure_client_runtime() {
  if [[ -x "$CLIENT_BIN" && -f "$CLIENT_TOML" ]]; then
    return 0
  fi
  log "zlm-client 运行文件不完整，尝试 publish_runtime"
  publish_runtime
}

cmd_zlm_server() {
  local action=$1
  case "$action" in
    start)
      ensure_server_runtime
      do_start "$SERVER_UNIT"
      wait_server_api || log "WARN: zlm-server 已拉起但 API 暂未就绪"
      ;;
    stop) do_stop "$SERVER_UNIT" ;;
    restart)
      ensure_server_runtime
      do_restart "$SERVER_UNIT"
      wait_server_api || log "WARN: zlm-server 已拉起但 API 暂未就绪"
      ;;
    reload) do_reload_server ;;
    status) do_status "$SERVER_UNIT" ;;
    update)
      do_stop "$SERVER_UNIT"
      build_server
      publish_server
      do_start "$SERVER_UNIT"
      wait_server_api || log "WARN: zlm-server 已拉起但 API 暂未就绪"
      ;;
    *)
      usage
      exit 2
      ;;
  esac
}

cmd_zlm_client() {
  local action=$1
  case "$action" in
    start)
      ensure_client_runtime
      retire_snapd
      do_start "$CLIENT_UNIT"
      ;;
    stop)
      retire_snapd
      do_stop "$CLIENT_UNIT"
      ;;
    restart)
      ensure_client_runtime
      retire_snapd
      do_restart "$CLIENT_UNIT"
      ;;
    status) do_status "$CLIENT_UNIT" ;;
    update)
      retire_snapd
      do_stop "$CLIENT_UNIT"
      build_client
      publish_client
      retire_snapd
      do_start "$CLIENT_UNIT"
      ;;
    reload)
      die "zlm-client 不支持 reload，请 restart"
      ;;
    *)
      usage
      exit 2
      ;;
  esac
}

build_server() {
  local sh="${REPO}/zlm-server/build.zlm.sh"
  [[ -f "$sh" ]] || die "缺少 ${sh}"
  log "编译 zlm-server"
  bash "$sh"
}

build_client() {
  command -v go >/dev/null 2>&1 || die "编译 zlm-client 需要 go"
  log "编译 zlm-client"
  mkdir -p "$(dirname "${CLIENT_BUILD}")"
  (
    cd "${REPO}/zlm-client"
    GOWORK=off go build -trimpath -o "${CLIENT_BUILD}" .
  )
}

cmd_zlm() {
  local action=$1
  case "$action" in
    start | restart | reload | update)
      log "先操作 server 再操作 client"
      ;;
  esac
  case "$action" in
    start)
      cmd_zlm_server start
      cmd_zlm_client start
      ;;
    stop)
      # 先停 client，避免钩子打到正在退出的 server；最终仍会停 server
      cmd_zlm_client stop
      cmd_zlm_server stop
      ;;
    restart)
      cmd_zlm stop
      log "先操作 server 再操作 client"
      cmd_zlm_server start
      cmd_zlm_client start
      ;;
    reload)
      cmd_zlm_server reload
      ;;
    status)
      cmd_zlm_server status
      cmd_zlm_client status
      ;;
    update)
      cmd_zlm stop
      build_server
      build_client
      publish_runtime
      install_units
      log "先操作 server 再操作 client"
      cmd_zlm_server start
      cmd_zlm_client start
      ;;
    *)
      usage
      exit 2
      ;;
  esac
}

main() {
  local target=${1:-} action=${2:-}
  if [[ -z "$target" || "$target" == "-h" || "$target" == "--help" || "$target" == "help" ]]; then
    usage
    exit 0
  fi
  [[ -n "$action" ]] || { usage; exit 2; }
  init_systemd
  acquire_lock
  case "$target" in
    zlm-server) cmd_zlm_server "$action" ;;
    zlm-client) cmd_zlm_client "$action" ;;
    zlm) cmd_zlm "$action" ;;
    *)
      usage
      exit 2
      ;;
  esac
}

main "$@"
