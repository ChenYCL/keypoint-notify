#!/bin/sh
# Keypoint 服务端一键安装 —— Linux VPS（systemd）
#
#   curl -fsSL https://raw.githubusercontent.com/ChenYCL/keypoint-notify/main/deploy/install-server.sh | sudo sh
#   curl -fsSL https://raw.githubusercontent.com/ChenYCL/keypoint-notify/main/deploy/install-server.sh | sudo sh -s -- --domain kp.example.com
#
# 参数
#   --domain D     有域名：用 Caddy 做 HTTPS（自动签证书），kp 只听 127.0.0.1。
#                  域名要先解析到这台机器，80/443 要放行。强烈推荐。
#   --port N       kp 监听端口，默认 8787
#   --local        只听 127.0.0.1，不装 Caddy（自己配 Cloudflare 隧道 / 现有反代时用）
#   --url URL      对外地址（写进接入说明）；默认按 --domain 或公网 IP 推断
#   --admin NAME   管理员身份名，默认 admin
#   --version V    装哪个版本（如 v0.2.0），默认最新 release
#   --uninstall    停服务、删二进制和 unit；数据 /var/lib/keypoint 保留
#
# 做的事
#   1. 从 GitHub Release 下载 kp（校验 SHA256），连同 4 个平台的客户端一起放到
#      /opt/keypoint/ —— 服务端的 /install.sh 按同事的平台下发客户端
#   2. 系统用户 keypoint，数据 /var/lib/keypoint，systemd 服务 keypoint（开机自启、崩溃重启）
#   3. --domain 时再起一个 keypoint-caddy 服务做 HTTPS
#   4. 认领管理员（只在空系统上），打印管理员 key 和「怎么拉人进来」
#
# 重跑就是升级：换二进制、重启服务；已有的数据和管理员不会动。
set -eu

REPO="ChenYCL/keypoint-notify"
PREFIX=/opt/keypoint
DATA=/var/lib/keypoint
ETC=/etc/keypoint
ADMIN_HOME="$ETC/admin"

DOMAIN="" PORT=8787 LOCAL=0 PUBLIC_URL="" ADMIN=admin VERSION="" UNINSTALL=0

say()  { printf '\033[36m▸\033[0m %s\n' "$*"; }
ok()   { printf '\033[32m✓\033[0m %s\n' "$*"; }
warn() { printf '\033[33m⚠\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[31m✗\033[0m %s\n' "$*" >&2; exit 1; }

while [ $# -gt 0 ]; do
  case "$1" in
    --domain) DOMAIN="${2:?--domain 需要一个值}"; shift 2 ;;
    --port) PORT="${2:?--port 需要一个值}"; shift 2 ;;
    --local) LOCAL=1; shift ;;
    --url) PUBLIC_URL="${2:?--url 需要一个值}"; shift 2 ;;
    --admin) ADMIN="${2:?--admin 需要一个值}"; shift 2 ;;
    --version) VERSION="${2:?--version 需要一个值}"; shift 2 ;;
    --uninstall) UNINSTALL=1; shift ;;
    -h|--help)
      cat <<'EOF'
curl -fsSL https://raw.githubusercontent.com/ChenYCL/keypoint-notify/main/deploy/install-server.sh | sudo sh -s -- [参数]

  --domain D     有域名：Caddy 自动 HTTPS，kp 只听 127.0.0.1（推荐）
  --port N       kp 监听端口，默认 8787
  --local        只听 127.0.0.1，不装 Caddy（自己配隧道 / 反代）
  --url URL      对外地址（写进接入说明），默认自动推断
  --admin NAME   管理员身份名，默认 admin
  --version V    版本（如 v0.2.0），默认最新 release
  --uninstall    卸载（数据保留）

环境变量 KP_RELEASE_URL 可指向内网镜像或 file:// 离线目录（放 kp-<os>-<arch> 和 SHA256SUMS）。
EOF
      exit 0 ;;
    *) die "不认识的参数 $1（--help 看用法）" ;;
  esac
done

[ "$(id -u)" = 0 ] || die "需要 root：curl -fsSL …/install-server.sh | sudo sh"
[ "$(uname -s)" = Linux ] || die "这个脚本只装 Linux 服务端（本机开发用 make build 或 docs/deploy-tunnel.md）"
command -v curl >/dev/null || die "需要 curl"
case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) die "不支持的架构 $(uname -m)（有 amd64 / arm64 两种预编译）" ;;
esac

HAS_SYSTEMD=0
[ -d /run/systemd/system ] && command -v systemctl >/dev/null && HAS_SYSTEMD=1

# ── 卸载 ────────────────────────────────────────────────────────────
if [ "$UNINSTALL" = 1 ]; then
  if [ "$HAS_SYSTEMD" = 1 ]; then
    systemctl disable --now keypoint-caddy.service 2>/dev/null || true
    systemctl disable --now keypoint.service 2>/dev/null || true
    rm -f /etc/systemd/system/keypoint.service /etc/systemd/system/keypoint-caddy.service
    systemctl daemon-reload
  else
    pkill -f "$PREFIX/kp serve" 2>/dev/null || true
  fi
  rm -rf "$PREFIX" /usr/local/bin/kp /usr/local/bin/kp-admin
  ok "已卸载。数据还在 $DATA，管理员配置在 $ADMIN_HOME —— 确定不要了再手工删"
  exit 0
fi

[ "$DOMAIN" = "" ] || [ "$LOCAL" = 0 ] || die "--domain 和 --local 二选一"

# ── 1. 下载 ─────────────────────────────────────────────────────────
# KP_RELEASE_URL 可以指向任意一个放着 kp-<os>-<arch> 和 SHA256SUMS 的地址
# （内网镜像、file:// 离线目录），默认是 GitHub Release。
if [ -n "${KP_RELEASE_URL:-}" ]; then
  BASE="$KP_RELEASE_URL"
elif [ -n "$VERSION" ]; then
  BASE="https://github.com/$REPO/releases/download/$VERSION"
else
  BASE="https://github.com/$REPO/releases/latest/download"
fi

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
say "下载 kp（$BASE）"
curl -fsSL "$BASE/SHA256SUMS" -o "$TMP/SHA256SUMS" ||
  die "拿不到 $BASE/SHA256SUMS —— 版本号对吗？还没有 release 的话用 --version 指定，或设 KP_RELEASE_URL"
for p in linux-amd64 linux-arm64 darwin-amd64 darwin-arm64; do
  curl -fsSL "$BASE/kp-$p" -o "$TMP/kp-$p" || die "下载 kp-$p 失败"
done
if command -v sha256sum >/dev/null; then
  (cd "$TMP" && grep ' kp-' SHA256SUMS | sha256sum -c - >/dev/null) || die "SHA256 校验失败，已中止（没有改动任何东西）"
  ok "SHA256 校验通过"
else
  warn "没有 sha256sum，跳过校验"
fi

UPGRADE=0
[ -x "$PREFIX/kp" ] && UPGRADE=1
mkdir -p "$PREFIX/bin"
install -m 0755 "$TMP/kp-linux-$ARCH" "$PREFIX/kp.new"
mv -f "$PREFIX/kp.new" "$PREFIX/kp"
for p in linux-amd64 linux-arm64 darwin-amd64 darwin-arm64; do
  install -m 0755 "$TMP/kp-$p" "$PREFIX/bin/kp-$p"
done
ln -sf "$PREFIX/kp" /usr/local/bin/kp
# 管理员身份单独放，不和这台机器上普通用户的 ~/.keypoint 混在一起
mkdir -p "$ADMIN_HOME"
chmod 700 "$ADMIN_HOME"
cat >/usr/local/bin/kp-admin <<EOF
#!/bin/sh
# 以服务端管理员身份运行 kp（配置在 $ADMIN_HOME）
exec env KEYPOINT_HOME="$ADMIN_HOME" "$PREFIX/kp" "\$@"
EOF
chmod 0755 /usr/local/bin/kp-admin
ok "$("$PREFIX/kp" version) → $PREFIX/kp（客户端 4 个平台 → $PREFIX/bin/）"

# ── 2. 用户、数据、服务 ─────────────────────────────────────────────
if ! id keypoint >/dev/null 2>&1; then
  useradd --system --home-dir "$DATA" --shell /usr/sbin/nologin keypoint 2>/dev/null ||
    adduser -S -D -H -h "$DATA" keypoint 2>/dev/null ||
    die "建不了系统用户 keypoint"
fi
mkdir -p "$DATA"
chown keypoint:keypoint "$DATA"
chmod 750 "$DATA"

if [ -n "$DOMAIN" ] || [ "$LOCAL" = 1 ]; then
  LISTEN="127.0.0.1:$PORT"
else
  LISTEN="0.0.0.0:$PORT"
fi

if [ "$HAS_SYSTEMD" = 1 ]; then
  cat >/etc/systemd/system/keypoint.service <<EOF
[Unit]
Description=Keypoint Notify
After=network-online.target
Wants=network-online.target

[Service]
User=keypoint
Group=keypoint
ExecStart=$PREFIX/kp serve --addr $LISTEN --data $DATA --quiet
Restart=always
RestartSec=2
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ReadWritePaths=$DATA

[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
  systemctl enable keypoint.service >/dev/null 2>&1
  systemctl restart keypoint.service
else
  warn "没有 systemd：用 nohup 起服务（重启机器后不会自动起来）"
  pkill -f "$PREFIX/kp serve" 2>/dev/null || true
  su -s /bin/sh keypoint -c "nohup $PREFIX/kp serve --addr $LISTEN --data $DATA --quiet >>$DATA/serve.log 2>&1 &"
fi

LOCAL_URL="http://127.0.0.1:$PORT"
i=0
until curl -fsS "$LOCAL_URL/api/v1/health" >/dev/null 2>&1; do
  i=$((i + 1))
  [ $i -gt 50 ] && die "服务没起来：journalctl -u keypoint -n 50"
  sleep 0.3
done
ok "服务已启动（$LISTEN）"

# ── 3. HTTPS（--domain）─────────────────────────────────────────────
if [ -n "$DOMAIN" ]; then
  if [ ! -x "$PREFIX/caddy" ]; then
    say "下载 Caddy"
    curl -fsSL "https://caddyserver.com/api/download?os=linux&arch=$ARCH" -o "$PREFIX/caddy.new" ||
      die "下载 Caddy 失败；也可以用 --local 起服务，自己配反代到 127.0.0.1:$PORT"
    chmod 0755 "$PREFIX/caddy.new"
    mv -f "$PREFIX/caddy.new" "$PREFIX/caddy"
  fi
  mkdir -p "$ETC"
  cat >"$ETC/Caddyfile" <<EOF
$DOMAIN {
	reverse_proxy 127.0.0.1:$PORT {
		# /api/v1/stream 是 SSE 长连接，别缓冲
		flush_interval -1
	}
}
EOF
  mkdir -p /var/lib/keypoint-caddy
  chown keypoint:keypoint /var/lib/keypoint-caddy
  if [ "$HAS_SYSTEMD" = 1 ]; then
    cat >/etc/systemd/system/keypoint-caddy.service <<EOF
[Unit]
Description=Keypoint HTTPS (Caddy)
After=network-online.target keypoint.service
Wants=network-online.target

[Service]
User=keypoint
Group=keypoint
Environment=XDG_DATA_HOME=/var/lib/keypoint-caddy XDG_CONFIG_HOME=/var/lib/keypoint-caddy
ExecStart=$PREFIX/caddy run --config $ETC/Caddyfile --adapter caddyfile
Restart=always
RestartSec=2
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
EOF
    systemctl daemon-reload
    systemctl enable keypoint-caddy.service >/dev/null 2>&1
    systemctl restart keypoint-caddy.service
    ok "HTTPS：Caddy 在 80/443 反代到 127.0.0.1:$PORT（证书首次签发要几十秒）"
  else
    warn "没有 systemd，Caddy 没有自动起：$PREFIX/caddy run --config $ETC/Caddyfile"
  fi
fi

# ── 4. 对外地址 ─────────────────────────────────────────────────────
if [ -z "$PUBLIC_URL" ]; then
  if [ -n "$DOMAIN" ]; then
    PUBLIC_URL="https://$DOMAIN"
  elif [ "$LOCAL" = 1 ]; then
    PUBLIC_URL="$LOCAL_URL"
  else
    IP=$(curl -fsS --max-time 5 https://api.ipify.org 2>/dev/null || true)
    [ -n "$IP" ] || IP=$(hostname -I 2>/dev/null | awk '{print $1}')
    PUBLIC_URL="http://${IP:-127.0.0.1}:$PORT"
  fi
fi
PUBLIC_URL=${PUBLIC_URL%/}

if [ -z "$DOMAIN" ] && [ "$LOCAL" = 0 ] && command -v ufw >/dev/null && ufw status 2>/dev/null | grep -q "Status: active"; then
  ufw allow "$PORT/tcp" >/dev/null && ok "ufw 已放行 $PORT/tcp"
fi

# ── 5. 管理员 ───────────────────────────────────────────────────────
ADMIN_KEY=""
if KEYPOINT_HOME="$ADMIN_HOME" "$PREFIX/kp" whoami --server "$LOCAL_URL" >/dev/null 2>&1; then
  ok "管理员已存在（$(KEYPOINT_HOME="$ADMIN_HOME" "$PREFIX/kp" config get identity)），未改动"
elif curl -fsS "$LOCAL_URL/api/v1/health" | grep -q '"bootstrapped": *true'; then
  warn "服务端已经初始化过，但这台机器上没有管理员配置（$ADMIN_HOME）。"
  warn "  持有管理员 key 的人：kp-admin init --server $LOCAL_URL --key kp_… --yes"
else
  say "认领管理员 $ADMIN"
  KEYPOINT_HOME="$ADMIN_HOME" "$PREFIX/kp" init --server "$LOCAL_URL" \
    --name "$ADMIN" --kind human --roles admin,member --yes >/dev/null
  ADMIN_KEY=$(sed -n 's/.*"api_key": *"\(kp_[^"]*\)".*/\1/p' "$ADMIN_HOME/config.json")
  ok "管理员 $ADMIN 已认领（配置 $ADMIN_HOME/config.json）"
fi

# 管理员 CLI 在这台机器上永远走 127.0.0.1（DNS、证书、安全组都影响不到它）；
# 接入说明里给别人的是 public_url。
if [ -f "$ADMIN_HOME/config.json" ]; then
  KEYPOINT_HOME="$ADMIN_HOME" "$PREFIX/kp" config set server "$LOCAL_URL" >/dev/null
  KEYPOINT_HOME="$ADMIN_HOME" "$PREFIX/kp" config set public_url "$PUBLIC_URL" >/dev/null
fi
REACHABLE=1
curl -fsS --max-time 8 "$PUBLIC_URL/api/v1/health" >/dev/null 2>&1 || REACHABLE=0

# ── 完成 ────────────────────────────────────────────────────────────
echo
if [ "$UPGRADE" = 1 ]; then ok "Keypoint 已升级"; else ok "Keypoint 已安装"; fi
cat <<EOF

  对外地址   $PUBLIC_URL
  看板       $PUBLIC_URL/board
  管理       $PUBLIC_URL/admin
  数据       $DATA（备份：见 docs/deploy-tunnel.md「备份」）
EOF
if [ -n "$ADMIN_KEY" ]; then
  cat <<EOF
  管理员     $ADMIN
  管理员 key $ADMIN_KEY
             ↑ 只显示这一次（也存在 $ADMIN_HOME/config.json，仅 root 可读）
EOF
fi
cat <<EOF

拉人进来（在这台机器上）：
  sudo kp-admin identity create alice --kind human --roles frontend
  sudo kp-admin identity create be-bot --kind agent --roles backend
  → 每条都会打印一段接入说明，整段发给对方。对方一条命令装好 kp + skill：
      curl -fsSL "$PUBLIC_URL/install.sh?key=kp_…" | sh

常用：
  sudo kp-admin role ls --holders       角色 → 谁持有
  systemctl status keypoint             journalctl -u keypoint -f
  升级：重跑这条安装命令                 卸载：… | sudo sh -s -- --uninstall
EOF
if [ "$REACHABLE" = 0 ]; then
  echo
  if [ -n "$DOMAIN" ]; then
    warn "暂时还访问不到 $PUBLIC_URL：证书首次签发要几十秒，也可能是 DNS 还没指过来 / 80、443 没放行。"
    warn "  看进度：journalctl -u keypoint-caddy -f"
  else
    warn "从这台机器访问不到 $PUBLIC_URL（安全组没放行，或云主机 NAT 不回环 —— 后者不影响外部访问）。"
  fi
  warn "  地址不对的话：sudo kp-admin config set public_url <对外地址>（只影响接入说明）"
fi
if [ -z "$DOMAIN" ] && [ "$LOCAL" = 0 ]; then
  echo
  warn "现在是明文 HTTP：API key 会在网络上明文传输。有域名的话重跑并加 --domain <域名> 换成 HTTPS。"
  warn "  云服务器还要在安全组里放行 $PORT/tcp。"
fi
