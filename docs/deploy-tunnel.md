# 部署：本机 + Cloudflare Tunnel

推荐形态：**Go 服务跑在你自己的机器上，隧道把 HTTPS 和域名交给 Cloudflare。**
好处是数据（SQLite + 附件）始终在你自己手里，同时外面的人能用
`https://kp.你的域名` 直接访问，不需要开端口、不需要公网 IP、不需要配证书。

```
本地/内网机器                    Cloudflare Edge
┌──────────────────┐            ┌─────────────┐
│ kp serve         │◄───────────│ kp.xxx.com  │
│  ├ SQLite 单文件  │ cloudflared│   (HTTPS)   │
│  ├ 附件 ./data/   │  隧道      └─────────────┘
│  └ 内嵌看板 UI    │                 ▲
│ 127.0.0.1:8787   │                 │
└──────────────────┘      浏览器 / Claude Code / Agent
```

---

> **有一台 Linux VPS？** 不用往下看：
> `curl -fsSL https://raw.githubusercontent.com/ChenYCL/keypoint-notify/main/deploy/install-server.sh | sudo sh -s -- --domain kp.example.com`
> 一条命令装好 systemd 服务 + HTTPS + 管理员，见 README「服务端：一台 Linux VPS」。
> 想让 VPS 只听 loopback、再套 Cloudflare 隧道：加 `--local`，然后看下面第 2 节。

## 1. 起服务

```bash
kp serve --data ~/.keypoint/data
```

默认监听 `127.0.0.1:8787`。**故意绑在 loopback**：隧道会从本机连进来，
没必要让服务在局域网里也裸奔。

开机自启（macOS launchd）：

```bash
cat > ~/Library/LaunchAgents/com.local.keypoint.plist <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>com.local.keypoint</string>
  <key>ProgramArguments</key>
  <array>
    <string>$HOME/.local/bin/kp</string>
    <string>serve</string>
    <string>--data</string><string>$HOME/.keypoint/data</string>
    <string>--quiet</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>$HOME/.keypoint/serve.log</string>
  <key>StandardErrorPath</key><string>$HOME/.keypoint/serve.err</string>
</dict></plist>
EOF
launchctl load ~/Library/LaunchAgents/com.local.keypoint.plist
```

（先把 `kp` 装到 `$HOME/.local/bin`：`make install`）

---

## 2. 建隧道

### 方式 A：临时隧道（30 秒能跑起来，URL 每次变）

```bash
brew install cloudflared
cloudflared tunnel --url http://127.0.0.1:8787
# 输出里会有 https://xxxx-yyyy.trycloudflare.com
```

适合自己临时试。URL 会变，别写进团队配置。

### 方式 B：持久隧道（推荐）

需要一个托管在 Cloudflare 的域名。

```bash
cloudflared tunnel login                       # 浏览器授权，选你的域名
cloudflared tunnel create keypoint             # 生成隧道凭据
cloudflared tunnel route dns keypoint kp.example.com

cat > ~/.cloudflared/config.yml <<EOF
tunnel: keypoint
credentials-file: $HOME/.cloudflared/<tunnel-id>.json
ingress:
  - hostname: kp.example.com
    service: http://127.0.0.1:8787
    originRequest:
      # SSE 是长连接，别让它被缓冲或超时切断
      noTLSVerify: false
      disableChunkedEncoding: false
      connectTimeout: 30s
  - service: http_status:404
EOF

cloudflared tunnel run keypoint
```

自启（macOS）：`sudo cloudflared service install`

### Cloudflare 侧建议

- **Access（Zero Trust）**：如果只想让自己/团队访问，在 Cloudflare 控制台给
  `kp.example.com` 加一条 Access 策略（邮箱域名或 Google 登录）。这样即使 key 泄露，
  外面也进不来。
- 关掉 **Rocket Loader** 和 **Auto Minify**：它们会动 HTML/JS，可能弄坏看板。
- 缓存规则：`/api/*` 走 Bypass，`/api/v1/files/*` 可以长缓存（内容寻址，不会变）。

---

## 3. 建身份、发 key

```bash
# 第一次（在服务器所在机器上）
kp init --server http://127.0.0.1:8787
# → 认领第一个身份，自动拿到 admin

# 改成本机客户端指向公网域名（可选，但团队其他人要用这个）
kp config set server https://kp.example.com
```

给别人发 key：

```bash
kp identity create alice --kind human --roles frontend,review
# → 打印一次性 key

# 对 alice 那边：
kp init --server https://kp.example.com --key kp_xxxx --yes
```

**key 只显示一次。** 丢了就轮换：`kp identity rotate alice`。

---

## 4. 备份

要备份的只有两样：SQLite 数据库和附件目录。

```bash
# 一致快照（WAL 模式下不能直接 cp）
sqlite3 ~/.keypoint/data/keypoint.db ".backup '/backup/keypoint-$(date +%F).db'"
rsync -a ~/.keypoint/data/blobs/ /backup/blobs/
```

附件是内容寻址的（`blobs/<sha256前两位>/<sha256>`），增删都幂等，
`rsync -a` 就是正确的增量备份方式。

---

## 5. 升级

```bash
make build                       # 或 go build -o kp ./cmd/keypoint
launchctl kickstart -k gui/$(id -u)/com.local.keypoint
```

数据库 schema 用 `CREATE TABLE IF NOT EXISTS`，启动时自动补齐，不需要手工迁移。
（加列需要手工 `ALTER TABLE`——在当前规模下没有引入迁移框架。）

---

## 6. Docker（可选）

仓库根目录有 `Dockerfile`，两阶段构建，产出的运行镜像里没有 Go、没有 C 库：

```bash
docker build -t keypoint:latest .

docker run -d --name keypoint \
  -p 127.0.0.1:8787:8787 \
  -v keypoint-data:/data \
  keypoint:latest

# 同一个二进制在容器里也能当 CLI 用
docker exec keypoint kp init --server http://127.0.0.1:8787
docker exec keypoint kp task list
```

`CGO_ENABLED=0` 能成立是因为 SQLite 驱动是纯 Go 的（`modernc.org/sqlite`），
运行镜像里不需要任何 C 运行库。`HEALTHCHECK` 打的是免鉴权的
`/api/v1/health`，所以不用把 key 烤进镜像。

容器里绑 `0.0.0.0`，宿主机只映射到 `127.0.0.1`，安全性不变。
数据库和附件都在 `/data` 卷里，`docker restart` 之后还在。

## 安全清单

- [ ] 服务绑 `127.0.0.1`（或容器映射到 loopback），不要直接 `0.0.0.0` 暴露公网
- [ ] 走 Cloudflare Access 或至少确定域名不被随便扫到
- [ ] 每个使用者（含 agent）一个身份，别共用 key
- [ ] 定期 `kp identity ls` 清掉不用的身份：`kp identity disable <name>`
- [ ] 离开团队的人：`kp identity disable`，必要时 `rotate`
- [ ] `/backup` 定期跑上面那两条备份命令
