#!/usr/bin/env bash
set -euo pipefail

# ==================== 可配置变量 ====================
# 以本脚本所在目录为基准（control.sh 从仓库根调用本脚本，不能用 pwd）
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ZLM_DIR="${SCRIPT_DIR}/ZLMediaKit"
ZLM_REPO="https://gitee.com/xia-chu/ZLMediaKit"
BUILD_DIR="${ZLM_DIR}/build"
# 运行目录是 release/linux/Release，必须编 Release，否则 WebRTC 编进 Debug 也没用
BUILD_TYPE="Release"

# OpenSSL 静态库
OPENSSL_ROOT="/data/sunpf/ffmpeg-builds/build/release"
OPENSSL_INC="${OPENSSL_ROOT}/include"
OPENSSL_SSL_LIB="${OPENSSL_ROOT}/lib/libssl.a"
OPENSSL_CRYPTO_LIB="${OPENSSL_ROOT}/lib/libcrypto.a;-lz;-ldl"

ZLIB_INC="/usr/include"
# libsrtp2（WebRTC 硬依赖，找不到会被 ZLM 静默关闭 ENABLE_WEBRTC）
SRTP_PREFIX="/data/sunpf/tools/zlmediakit/thr3parts/libsrtp2"
LIBSRTP_VER="v2.6.0"
# dst：脚本同级目录；每次编译后强制覆盖同步优化后的二进制与 config.ini
ADMIN_ZLM="${SCRIPT_DIR}"
ZLM_DATA_ROOT="/data/zlm"
# ====================================================

find_zlib_lib() {
    local candidates=(
        "/usr/lib/x86_64-linux-gnu/libz.so"
        "/usr/lib64/libz.so"
        "/usr/lib/libz.so"
    )
    for lib in "${candidates[@]}"; do
        [[ -f "$lib" ]] && echo "$lib" && return 0
    done
    echo "ERROR: 未找到zlib库，请安装 zlib-devel/zlib1g-dev" >&2
    exit 1
}

# 解析系统或本地编译的 libsrtp2
resolve_srtp() {
    local inc="" lib=""
    local inc_candidates=(
        "${SRTP_PREFIX}/include/srtp2/srtp.h"
        "/usr/include/srtp2/srtp.h"
        "/usr/local/include/srtp2/srtp.h"
    )
    local lib_candidates=(
        "${SRTP_PREFIX}/lib/libsrtp2.a"
        "${SRTP_PREFIX}/lib64/libsrtp2.a"
        "/usr/lib/x86_64-linux-gnu/libsrtp2.so"
        "/usr/lib64/libsrtp2.so"
        "/usr/lib/libsrtp2.so"
        "/usr/local/lib/libsrtp2.so"
        "/usr/local/lib/libsrtp2.a"
    )
    for f in "${inc_candidates[@]}"; do
        if [[ -f "$f" ]]; then
            inc="$(dirname "$(dirname "$f")")"
            break
        fi
    done
    for f in "${lib_candidates[@]}"; do
        if [[ -f "$f" ]]; then
            lib="$f"
            break
        fi
    done
    echo "${inc}|${lib}"
}

build_libsrtp2() {
    echo "===== 编译 libsrtp2（WebRTC 依赖）====="
    local src="/data/sunpf/tools/zlmediakit/thr3parts/src/libsrtp"
    mkdir -p /data/sunpf/tools/zlmediakit/thr3parts/src
    if [[ ! -d "$src/.git" ]]; then
        rm -rf "$src"
        git clone --depth 1 --branch "${LIBSRTP_VER}" \
            https://gitee.com/mirrors/libsrtp.git "$src" \
            || git clone --depth 1 --branch "${LIBSRTP_VER}" \
                https://github.com/cisco/libsrtp.git "$src"
    fi
    rm -rf "$src/build"
    local zlib_lib
    zlib_lib="$(find_zlib_lib)"
    # CMake 4.4 FindOpenSSL 会给 OpenSSL::Crypto 挂上 ZLIB::ZLIB，必须先找到 zlib
    cat > "$src/zlib_first.cmake" <<EOF
if(NOT TARGET ZLIB::ZLIB)
  set(ZLIB_INCLUDE_DIR "${ZLIB_INC}" CACHE PATH "" FORCE)
  set(ZLIB_LIBRARY "${zlib_lib}" CACHE FILEPATH "" FORCE)
  find_package(ZLIB REQUIRED)
endif()
if(NOT TARGET ZLIB::ZLIB)
  add_library(ZLIB::ZLIB UNKNOWN IMPORTED)
  set_target_properties(ZLIB::ZLIB PROPERTIES
    IMPORTED_LOCATION "${zlib_lib}"
    INTERFACE_INCLUDE_DIRECTORIES "${ZLIB_INC}")
endif()
EOF
    cmake -S "$src" -B "$src/build" \
        -DCMAKE_BUILD_TYPE=Release \
        -DCMAKE_INSTALL_PREFIX="${SRTP_PREFIX}" \
        -DCMAKE_POSITION_INDEPENDENT_CODE=ON \
        -DCMAKE_PROJECT_TOP_LEVEL_INCLUDES="$src/zlib_first.cmake" \
        -DCMAKE_EXE_LINKER_FLAGS="-lz -ldl -lpthread" \
        -DZLIB_INCLUDE_DIR="${ZLIB_INC}" \
        -DZLIB_LIBRARY="${zlib_lib}" \
        -DLIBSRTP_TEST_APPS=OFF \
        -DENABLE_OPENSSL=ON \
        -DOPENSSL_USE_STATIC_LIBS=ON \
        -DOPENSSL_ROOT_DIR="${OPENSSL_ROOT}" \
        -DOPENSSL_INCLUDE_DIR="${OPENSSL_INC}" \
        -DOPENSSL_CRYPTO_LIBRARY="${OPENSSL_ROOT}/lib/libcrypto.a" \
        -DOPENSSL_SSL_LIBRARY="${OPENSSL_ROOT}/lib/libssl.a"
    # 只编库，不编 kernel_driver/rtpw 等测试程序
    cmake --build "$src/build" -j"$(nproc)" --target srtp2
    cmake --install "$src/build"
}

resolve_ffmpeg() {
    local candidates=(
        "${OPENSSL_ROOT}/bin/ffmpeg"
        "/usr/local/bin/ffmpeg"
        "/usr/bin/ffmpeg"
    )
    for f in "${candidates[@]}"; do
        if [[ -x "$f" ]]; then
            echo "$f"
            return 0
        fi
    done
    if command -v ffmpeg >/dev/null 2>&1; then
        command -v ffmpeg
        return 0
    fi
    echo ""
}

# 在 cmake 生成的默认 config.ini 上按节写入直播最佳参数（保留注释和 secret）
apply_live_configs() {
    local root="${ZLM_DIR}/release"
    if [[ ! -d "$root" ]]; then
        echo "WARN: 未找到 $root，跳过配置优化"
        return 0
    fi
    export ZLM_DATA_ROOT
    export ZLM_FFMPEG_BIN="${ZLM_FFMPEG_BIN:-$(resolve_ffmpeg)}"
    python3 - "$root" <<'PY'
import os, sys
root = sys.argv[1]
HOOK = "http://127.0.0.1:7788/hook"

def set_kv(text, section, key, value):
    lines = text.splitlines()
    out = []
    in_sec = False
    found_in_sec = False
    header = "[" + section + "]"
    replaced_any = False
    for line in lines:
        stripped = line.strip()
        if stripped.startswith("[") and stripped.endswith("]"):
            if in_sec and not found_in_sec:
                out.append("%s=%s" % (key, value))
                replaced_any = True
            in_sec = (stripped.lower() == header.lower())
            found_in_sec = False
            out.append(line)
            continue
        if in_sec and stripped and not stripped.startswith("#") and stripped.split("=", 1)[0].strip() == key:
            out.append("%s=%s" % (key, value))
            found_in_sec = True
            replaced_any = True
            continue
        out.append(line)
    if in_sec and not found_in_sec:
        out.append("%s=%s" % (key, value))
        replaced_any = True
    if not replaced_any:
        out.append("")
        out.append(header)
        out.append("%s=%s" % (key, value))
    return "\n".join(out) + ("\n" if text.endswith("\n") else "")

def patch(path, build_kind):
    text = open(path, "r", encoding="utf-8", errors="replace").read().replace("\r\n", "\n")
    log_level = "1" if build_kind == "debug" else "2"
    data_root = os.environ.get("ZLM_DATA_ROOT", "/data/zlm").rstrip("/")
    ffmpeg_bin = os.environ.get("ZLM_FFMPEG_BIN", "").strip()
    pairs = [
        ("api", "apiDebug", "1"),
        ("api", "downloadRoot", data_root),
        ("api", "snapRoot", data_root + "/snap/"),
        ("api", "defaultSnap", ""),
        ("protocol", "enable_audio", "1"),
        ("protocol", "add_mute_audio", "1"),
        ("protocol", "enable_hls", "1"),
        ("protocol", "enable_hls_fmp4", "1"),
        ("protocol", "enable_rtsp", "1"),
        ("protocol", "enable_rtmp", "1"),
        ("protocol", "enable_ts", "1"),
        ("protocol", "enable_fmp4", "1"),
        ("protocol", "hls_demand", "0"),
        ("protocol", "rtsp_demand", "0"),
        ("protocol", "rtmp_demand", "0"),
        ("protocol", "ts_demand", "0"),
        ("protocol", "fmp4_demand", "0"),
        ("protocol", "hls_save_path", data_root),
        ("protocol", "mp4_save_path", data_root + "/mp4"),
        ("general", "enableVhost", "0"),
        ("general", "mergeWriteMS", "50"),
        ("general", "broadcast_player_count_changed", "1"),
        ("hls", "fileBufSize", "262144"),
        ("hls", "fastRegister", "1"),
        ("hls", "segKeep", "0"),
        ("hls", "deleteDelaySec", "600"),
        ("http", "sendBufSize", "262144"),
        ("http", "port", "8090"),
        ("http", "sslport", "8443"),
        ("http", "rootPath", data_root),
        ("http", "dirMenu", "1"),
        ("record", "fileBufSize", "262144"),
        ("record", "fastStart", "1"),
        ("record", "enableFmp4", "1"),
        ("record", "appName", "record"),
        ("rtsp", "directProxy", "0"),
        ("rtsp", "lowLatency", "1"),
        ("hook", "enable", "1"),
        ("hook", "on_play", HOOK + "/on_play"),
        ("hook", "on_publish", HOOK + "/on_publish"),
        ("hook", "on_flow_report", HOOK + "/on_flow_report"),
        ("hook", "on_record_mp4", HOOK + "/on_record_mp4"),
        ("hook", "on_stream_changed", HOOK + "/on_stream_changed"),
        ("hook", "on_stream_not_found", HOOK + "/on_stream_not_found"),
        ("hook", "on_server_started", HOOK + "/on_server_started"),
        ("hook", "on_server_exited", HOOK + "/on_server_exited"),
        ("hook", "on_server_keepalive", HOOK + "/on_server_keepalive"),
        ("hook", "stream_changed_schemas", "rtsp/rtmp/fmp4/ts/hls/hls.fmp4/rtc"),
        ("general", "mediaServerId", "zlm-1"),
        ("log", "level", log_level),
    ]
    if ffmpeg_bin:
        pairs.append(("ffmpeg", "bin", ffmpeg_bin))
    for sec, key, val in pairs:
        text = set_kv(text, sec, key, val)
    bak = path + ".bak-before-liveopt"
    if not os.path.exists(bak):
        open(bak, "w", encoding="utf-8").write(open(path, "r", encoding="utf-8", errors="replace").read())
    open(path, "w", encoding="utf-8", newline="\n").write(text)

found = []
for dirpath, _, files in os.walk(root):
    base = os.path.basename(dirpath).lower()
    parent = os.path.basename(os.path.dirname(dirpath)).lower()
    if "config.ini" not in files:
        continue
    if base in ("debug", "release") or parent in ("debug", "release"):
        p = os.path.join(dirpath, "config.ini")
        kind = "debug" if (base == "debug" or parent == "debug") else "release"
        patch(p, kind)
        found.append(p)
        print("已优化: %s  (log.level=%s http.rootPath=%s)" % (p, "1/Debug" if kind == "debug" else "2/Info", os.environ.get("ZLM_DATA_ROOT", "/data/zlm")))
if not found:
    print("WARN: 未在 %s 下找到 Debug/Release 的 config.ini" % root)
    sys.exit(0)
PY
}

clean_unused_zlm_dirs() {
    local root="${1:-$ZLM_DATA_ROOT}"
    local ini="${2:-}"
    mkdir -p "${root}" "${root}/mp4" "${root}/snap"
    local d
    for d in hls rec ts flv dash; do
        if [[ -e "${root}/${d}" ]]; then
            echo "清理无用目录 ${root}/${d}"
            rm -rf "${root:?}/${d}"
        fi
    done
    local vhost_on=0
    if [[ -n "$ini" && -f "$ini" ]] && grep -Eiq '^[[:space:]]*enableVhost[[:space:]]*=[[:space:]]*1' "$ini"; then
        vhost_on=1
    fi
    if [[ "$vhost_on" -eq 0 && -e "${root}/__defaultVhost__" ]]; then
        echo "关闭虚拟主机，清理 ${root}/__defaultVhost__"
        rm -rf "${root:?}/__defaultVhost__"
    fi
}

sync_file() {
    local src=$1 dest=$2 mode=${3:-}
    [[ -f "$src" ]] || { echo "ERROR: 缺少源文件 ${src}" >&2; exit 1; }
    cp -f "$src" "$dest"
    if [[ -n "$mode" ]]; then
        chmod "$mode" "$dest"
    fi
    if ! cmp -s "$src" "$dest"; then
        echo "ERROR: 同步失败 ${src} -> ${dest}" >&2
        exit 1
    fi
}

install_to_admin() {
    local src="${OUT_DIR}"
    local dst="${ADMIN_ZLM}"
    mkdir -p "$dst/log"
    if [[ ! -x "${src}/MediaServer" ]]; then
        echo "ERROR: 未找到编译产物 ${src}/MediaServer" >&2
        exit 1
    fi
    if [[ ! -f "${src}/config.ini" ]]; then
        echo "ERROR: 未找到优化后的 ${src}/config.ini" >&2
        exit 1
    fi
    echo "===== 强制同步 dst: ${dst} ====="
    # sync_file "${src}/MediaServer" "${dst}/MediaServer" "755"
    # 运维副本名与 MediaServer 同源，始终覆盖
    sync_file "${src}/MediaServer" "${dst}/zlm-server" "755"
    sync_file "${src}/config.ini" "${dst}/config.ini" "644"
    if [[ -f "${src}/default.pem" ]]; then
        sync_file "${src}/default.pem" "${dst}/default.pem" "644"
    fi
    clean_unused_zlm_dirs "${ZLM_DATA_ROOT}" "${dst}/config.ini"
    echo "已同步优化后的二进制 + config.ini -> ${dst}"
    # echo "  ${dst}/MediaServer"
    echo "  ${dst}/zlm-server"
    echo "  ${dst}/config.ini"
    echo "  ffmpeg.bin=$(resolve_ffmpeg)"
    echo "  落盘根目录=${ZLM_DATA_ROOT}（仅保留 mp4/ 录像与 snap/ 截图；直播切片在 {app}/{stream}/）"
}

CLEAN_BUILD=false
[[ "${1:-}" == "--clean" ]] && CLEAN_BUILD=true

echo "脚本目录: ${SCRIPT_DIR}"
echo "源码目录: ${ZLM_DIR}"
echo "安装目录: ${ADMIN_ZLM}"

echo "===== 步骤1：检测ZLMediaKit源码 ====="
if [[ ! -d "$ZLM_DIR" ]]; then
    echo "克隆 ZLMediaKit..."
    git clone --depth 1 "$ZLM_REPO" "$ZLM_DIR" || exit 1
fi

cd "$ZLM_DIR" || exit 1
if [[ ! -d ".git/modules" ]]; then
    echo "初始化子模块..."
    git submodule update --init || exit 1
fi

echo "===== 步骤2：确保 libsrtp2 可用 ====="
SRTP_PAIR="$(resolve_srtp)"
SRTP_INC="${SRTP_PAIR%%|*}"
SRTP_LIB="${SRTP_PAIR#*|}"
if [[ -z "$SRTP_INC" || -z "$SRTP_LIB" ]]; then
    echo "未找到 libsrtp2，开始本地编译..."
    build_libsrtp2
    SRTP_PAIR="$(resolve_srtp)"
    SRTP_INC="${SRTP_PAIR%%|*}"
    SRTP_LIB="${SRTP_PAIR#*|}"
fi
if [[ -z "$SRTP_INC" || -z "$SRTP_LIB" || ! -f "${SRTP_INC}/srtp2/srtp.h" || ! -f "$SRTP_LIB" ]]; then
    echo "ERROR: libsrtp2 仍不可用。WebRTC 无法编译。" >&2
    echo "  期望头文件: ${SRTP_PREFIX}/include/srtp2/srtp.h 或 /usr/include/srtp2/srtp.h" >&2
    exit 1
fi
echo "使用 SRTP include: ${SRTP_INC}"
echo "使用 SRTP library: ${SRTP_LIB}"

echo "===== 步骤3：准备构建目录 ====="
if [[ "$CLEAN_BUILD" == "true" ]]; then
    echo "清理旧构建目录..."
    rm -rf "$BUILD_DIR"
fi
mkdir -p "$BUILD_DIR"
cd "$BUILD_DIR" || exit 1
rm -f CMakeCache.txt

OUT_DIR="${ZLM_DIR}/release/linux/${BUILD_TYPE}"

echo "===== 步骤4：执行CMake配置 ====="
ZLIB_LIB=$(find_zlib_lib)
echo "使用 zlib: $ZLIB_LIB"
echo "构建类型: ${BUILD_TYPE}"

cmake .. \
  -DCMAKE_BUILD_TYPE="${BUILD_TYPE}" \
  -DCMAKE_PREFIX_PATH="${SRTP_PREFIX};${OPENSSL_ROOT}" \
  -DENABLE_WEBRTC=ON \
  -DENABLE_PYTHON=ON \
  -DENABLE_SRT=ON \
  -DENABLE_FFMPEG=ON \
  -DOPENSSL_ROOT_DIR="${OPENSSL_ROOT}" \
  -DOPENSSL_USE_STATIC_LIBS=ON \
  -DOPENSSL_INCLUDE_DIR="${OPENSSL_INC}" \
  -DOPENSSL_SSL_LIBRARY="${OPENSSL_SSL_LIB}" \
  -DOPENSSL_CRYPTO_LIBRARY="${OPENSSL_CRYPTO_LIB}" \
  -DZLIB_INCLUDE_DIR="${ZLIB_INC}" \
  -DZLIB_LIBRARY="${ZLIB_LIB}" \
  -DSRTP_INCLUDE_DIRS="${SRTP_INC}" \
  -DSRTP_LIBRARIES="${SRTP_LIB}"

# 关键校验：option=ON 但缺 srtp 时 ZLM 会静默关掉宏，二进制里仍然没有 WebRTC
if ! grep -q 'MK_COMPILE_DEFINITIONS:INTERNAL=.*ENABLE_WEBRTC' CMakeCache.txt; then
    echo "ERROR: CMake 未把 ENABLE_WEBRTC 写进编译宏（通常是 libsrtp2 没链上）。" >&2
    grep -E 'ENABLE_WEBRTC|SRTP_' CMakeCache.txt | head -n 40 >&2 || true
    exit 1
fi
echo "已确认 MK_COMPILE_DEFINITIONS 含 ENABLE_WEBRTC"

echo "===== 步骤5：编译 MediaServer ====="
make -j"$(nproc)" MediaServer

echo "===== 步骤6：查找并写入直播最佳 config.ini ====="
ZLM_FFMPEG_BIN="$(resolve_ffmpeg)"
export ZLM_FFMPEG_BIN
if [[ -n "$ZLM_FFMPEG_BIN" ]]; then
    echo "FFmpeg: ${ZLM_FFMPEG_BIN}"
else
    echo "WARN: 未找到 ffmpeg 可执行文件，config.ini [ffmpeg] bin 保持原值"
fi
apply_live_configs

echo "===== 步骤7：强制同步到 dst（${ADMIN_ZLM}）====="
install_to_admin

echo "===== 编译完成 ====="
echo "二进制: ${OUT_DIR}/MediaServer"
echo "配置:   ${OUT_DIR}/config.ini"
echo "dst 副本: ${ADMIN_ZLM}/{MediaServer,zlm-server,config.ini}"
echo ""
echo "启动建议（96 核机器不要用默认 96 线程）:"
echo "  /data/sunpf/tools/zlm/start.sh"
echo "  或: cd ${ADMIN_ZLM} && ./zlm-server -c ./config.ini -t 16"
echo "  日志等级: Info (config.ini [log] level=2；播放/断开见钩子 on_play / on_flow_report)"
echo "  HTTP 根目录: ${ZLM_DATA_ROOT}  （enableVhost=0，打开 http://host:8090/ 可浏览切片）"
