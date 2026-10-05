# LiProxy

**一个 Web 驱动的全能代理平台 —— 一个二进制文件，一个浏览器标签页，搞定所有代理。**

LiProxy 把 Mihomo 级别的客户端能力和 sing-box 级别的服务端能力装进同一个 Go 二进制里，并且用 Web 界面替代了手写 YAML。你不再需要编辑几千行配置文件：配置存在数据库里，Web 就是唯一主入口。

[![Go Version](https://img.shields.io/badge/Go-1.22%2B-00ADD8?logo=go&logoColor=white)](https://go.dev/dl/)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Build](https://github.com/liproxy/liproxy/actions/workflows/build.yml/badge.svg)](https://github.com/liproxy/liproxy/actions/workflows/build.yml)

---

## 特性

### 协议

- **入站与出站同源**：每个协议都同时支持作为入站（服务端）和出站（客户端），一份实现两边复用
- **支持协议**：HTTP / SOCKS 混合、Shadowsocks / SS2022、VMess、VLESS、Trojan、Hysteria2、TUIC、ShadowTLS v3、AnyTLS、Snell、NaiveProxy、WireGuard、TUN
- **传输层**：TCP/RAW、WebSocket、gRPC、HTTP/2、QUIC、XHTTP
- 混合入站：HTTP 与 SOCKS5 共用同一个端口，浏览器插件无需分辨

### 伪装与抗封锁

- **Reality**：无需域名和证书，借用真实站点的 TLS 指纹
- **ShadowTLS v3**：把代理流量藏在真实的 TLS 握手之后
- **Domain Fronting**：CDN 前置域名分流
- **uTLS 指纹**：可模拟 Chrome / Firefox / Safari / iOS 等客户端指纹
- **Sniffer**：自动嗅探 TLS SNI、HTTP Host、QUIC 域名，让基于域名的规则在纯 IP 连接下也能生效

### DNS

- **客户端 DNS**：Fake-IP 加速、DoH / DoT / DoQ 上游、Bootstrap DNS 防污染、按域名分流解析
- **自建 DNS 服务器**：UDP / TCP / DoT / DoH / DoQ 五种入站方式，内置 Router / Modifier / Resolver 管线
- 支持 Cache、Blocklist（广告拦截）、DNSSEC 校验、Split-Horizon（内外网不同解析）

### 服务端

- **多用户**：一套服务端，多用户独立凭据、独立配额
- **流量计量与配额**：按用户统计上下行流量，超额自动停用
- **ACME 证书自动化**：内置 Let's Encrypt 申请与自动续期，无需 Certbot
- **订阅生成与分发**：一键生成 Clash / V2Ray / sing-box 订阅链接，支持按用户分发
- **Provider 系统**：直接从订阅源拉取 Clash YAML、V2Ray Base64、sing-box JSON、单节点 URI 并转换为本地节点

### 性能

- **内存**：空闲 < 15 MB，1000 并发连接 < 100 MB
- **二进制**：全协议版本约 25 MB，精简版约 8 MB
- 单二进制、零外部运行时依赖（SQLite 驱动为纯 Go 实现，无需 CGO）

### Web 界面

- 节点、规则、Provider、DNS、用户、流量全部图形化管理
- **版本管理**：每次修改自动生成新版本，支持 diff、一键回滚、导出
- **实时流量图**：基于 WebSocket 的连接列表与速率曲线
- 支持 PWA，可安装到桌面/手机，离线也能打开面板

---

## 快速开始

### 方式一：一键脚本（推荐）

```bash
curl -fsSL https://get.liproxy.dev | sh
```

脚本会自动识别系统架构、下载最新版本、安装 `lipd` 与 `lip` 到 `/usr/local/bin`，并注册系统服务。

### 方式二：Docker

```bash
docker run -d \
  --name liproxy \
  --restart unless-stopped \
  --network host \
  -v ~/.li/liproxy:/root/.li/liproxy \
  ghcr.io/liproxy/liproxy:latest
```

> TUN 模式需要 `--cap-add=NET_ADMIN --device=/dev/net/tun`，并使用 `--network host`。

Web 界面默认监听 `http://127.0.0.1:9090`，浏览器打开即可。

### 方式三：手动编译

```bash
# 需要 Go 1.22+ 与 Node.js 20+ / pnpm 9+
git clone https://github.com/liproxy/liproxy.git
cd liproxy

# 构建前端与后端（前端产物会嵌入二进制）
make build

# 或者只要后端精简版本
make build-minimal

sudo install -m 0755 bin/lipd /usr/local/bin/lipd
sudo install -m 0755 bin/lip  /usr/local/bin/lip
```

---

## 首次启动

```bash
lipd
```

首次启动时 LiProxy 会创建数据目录、初始化数据库并生成随机管理员密码，控制台输出如下：

```text
2026-01-01T12:00:00+08:00 INF LiProxy v0.1.0 (commit 0a1b2c3, go1.22.5)
2026-01-01T12:00:00+08:00 INF data directory ready path=/home/you/.li/liproxy
2026-01-01T12:00:00+08:00 INF database initialized path=/home/you/.li/liproxy/liproxy.db version=1
2026-01-01T12:00:00+08:00 WRN ┌────────────────────────────────────────────────┐
2026-01-01T12:00:00+08:00 WRN │  Initial admin credentials (shown once only)   │
2026-01-01T12:00:00+08:00 WRN │  Username: admin                               │
2026-01-01T12:00:00+08:00 WRN │  Password: 7Kq2-vXp9-mN4t-Rz8W                 │
2026-01-01T12:00:00+08:00 WRN └────────────────────────────────────────────────┘
2026-01-01T12:00:00+08:00 INF web server listening addr=http://127.0.0.1:9090
2026-01-01T12:00:00+08:00 INF engine started mode=client inbound=0 outbound=0
```

> 该密码只在首次启动时打印一次。请立即登录并修改。

**下一步：**

1. 浏览器打开 `http://127.0.0.1:9090`
2. 用 `admin` 和上面的随机密码登录
3. 进入 **设置 → 账户** 修改密码
4. 进入 **节点** 添加你的第一个代理节点，回到 **概览** 启动引擎

**忘记密码怎么办？**

```bash
lip passwd --reset    # 重置为新的随机密码并打印到控制台
```

---

## 配置文件说明

LiProxy 有且只有一个配置文件：`~/.li/liproxy/liproxy.toml`。
它**只负责进程如何启动**（监听地址、认证入口、数据库位置、日志）。节点、规则、DNS、Provider、用户、流量等全部存在数据库中，通过 Web 管理。

### 字段一览（共 10 个）

| 段落 | 字段 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `[server]` | `listen` | string | `127.0.0.1` | Web 服务监听地址。对外开放填 `0.0.0.0`，但务必同时开启认证 |
| `[server]` | `port` | int | `9090` | Web 服务与 REST API 端口 |
| `[auth]` | `enabled` | bool | `true` | 是否启用 Web 登录认证。**监听非回环地址时禁止关闭** |
| `[auth]` | `username` | string | `admin` | 管理员用户名 |
| `[auth]` | `password` | string | 首次启动随机生成 | 管理员密码。留空表示使用数据库中已保存的密码 |
| `[tls]` | `enabled` | bool | `false` | 是否为 Web 界面启用 HTTPS |
| `[tls]` | `cert` | string | `""` | 证书文件路径（PEM） |
| `[tls]` | `key` | string | `""` | 私钥文件路径（PEM） |
| `[database]` | `path` | string | `~/.li/liproxy/liproxy.db` | SQLite 数据库文件路径，支持相对路径与 `~` |
| `[log]` | `level` | string | `info` | 日志级别：`debug` / `info` / `warn` / `error` |
| `[log]` | `file` | string | `~/.li/liproxy/logs/liproxy.log` | 日志文件路径。留空表示只输出到标准输出 |

### 完整示例

```toml
# ~/.li/liproxy/liproxy.toml

[server]
listen = "127.0.0.1"
port   = 9090

[auth]
enabled  = true
username = "admin"
password = ""          # 留空 = 沿用数据库中已保存的密码

[tls]
enabled = false        # 反向代理后面通常保持 false
cert    = ""
key     = ""

[database]
path = "~/.li/liproxy/liproxy.db"

[log]
level = "info"
file  = "~/.li/liproxy/logs/liproxy.log"
```

修改配置后重启进程即可生效：

```bash
lip restart
# 或者 systemd
sudo systemctl restart liproxy
```

---

## Web 界面

![dashboard](docs/images/dashboard.png)

<details>
<summary>更多截图</summary>

| 页面 | 预览 |
| --- | --- |
| 节点管理 | ![proxies](docs/images/proxies.png) |
| 规则编辑 | ![rules](docs/images/rules.png) |
| 实时连接 | ![connections](docs/images/connections.png) |
| DNS 设置 | ![dns](docs/images/dns.png) |
| 服务端用户与流量 | ![users](docs/images/users.png) |
| 版本历史与回滚 | ![versions](docs/images/versions.png) |

</details>

---

## 常见场景

### 客户端模式：本地代理

最典型的用法 —— 本机起一个混合端口，浏览器和终端都走代理。

```bash
lip start --mode client
curl -x http://127.0.0.1:7890 https://api.ipify.org
```

在 Web 的 **节点** 页面添加订阅或手动节点，在 **入站** 页面添加一个混合入站（默认端口 `7890`），即可使用。

### 服务端模式：给朋友开账号

一台公网服务器，为多个用户提供代理服务并统计流量。

```bash
# 在 Web 的「服务端」页面完成配置后：
lip server user add alice --quota 100GB
lip server sub alice --format clash --output alice.yaml
```

- 用户凭据、配额、订阅链接均在 Web **用户** 页面管理
- 证书由内置 ACME 自动申请与续期
- 订阅支持 `clash` / `v2ray` / `sing-box` 三种格式

### 自建 DNS 服务器

把 LiProxy 当作一个支持 DoH/DoQ 的家庭 DNS，带广告拦截与内外网分别解析。

```bash
lip dns server enable --listen 0.0.0.0:53
```

在 Web **DNS 服务器** 页面配置：

- **Blocklist**：加载广告/追踪域名规则集
- **Split-Horizon**：`*.home.lan` 解析到内网地址，其他走公共解析
- **DNSSEC**：对支持的上游开启校验
- **Cache**：默认开启，TTL 尊重上游响应

### TUN 全局代理

不依赖任何应用配置，接管整机流量（需要 root 或 `NET_ADMIN`）。

```bash
sudo lip start --mode tun --tun-stack gvisor
```

- `gvisor`：用户态协议栈，内存占用低，无需内核模块（推荐）
- `system`：使用内核协议栈，性能更高但需要 root 且依赖系统 TUN 支持
- 可在 Web **TUN** 页面配置路由绕过（局域网、Docker 网段等）

---

## 从 Mihomo / Clash 迁移

LiProxy 可以直接导入现有订阅和配置文件，不需要重写节点。

**第一步：导入节点**

1. 打开 Web → **Provider** → **新建**
2. 填入你的订阅链接（支持 Clash YAML、V2Ray Base64、sing-box JSON、单节点 URI）
3. 设置更新间隔（例如 `24h`），保存
4. LiProxy 会立即拉取一次，节点出现在 **节点** 列表中

**第二步：导入规则**

1. Web → **规则集** → **导入**
2. 支持 Clash 规则集（`RULE-SET` / `DOMAIN-SUFFIX` / `IP-CIDR` / `GEOIP` 等）
3. 也可以直接粘贴完整的 Clash/ Mihomo `rules:` 段落，LiProxy 会自动解析

**第三步：直接用文件导入（可选）**

```bash
# 导入完整的 Clash / Mihomo 配置
lip import clash ~/.config/mihomo/config.yaml

# 导入 sing-box 配置
lip import singbox /etc/sing-box/config.json

# 导入后预览差异，不提交
lip import clash config.yaml --dry-run
```

**从 Mihomo 迁移的对应关系：**

| Mihomo / Clash | LiProxy |
| --- | --- |
| `proxies:` | Provider 或节点列表（数据库） |
| `proxy-groups:` | 策略组（Web 可视化编辑） |
| `rules:` | 规则引擎（同一套类型语法） |
| `dns:` | DNS 页面（含 Fake-IP、DoH/DoT/DoQ） |
| `tun:` | TUN 页面（gVisor / system 双栈可选） |
| `external-controller` | 内置 Web 界面 + REST API |
| 控制台改配置 + 重启 | Web 改配置，实时热重载 |

> 导入的配置会生成一个新版本，可在 **版本历史** 中对比或回滚，不会影响当前运行的配置。

---

## CLI 命令速查

| 命令 | 说明 |
| --- | --- |
| `lip start` | 启动代理引擎（前台运行） |
| `lip stop` | 停止正在运行的引擎 |
| `lip restart` | 重启引擎（配置变更后使用） |
| `lip status` | 查看运行状态、当前模式、连接数、内存占用 |
| `lip passwd` | 修改管理员密码 |
| `lip passwd --reset` | 重置为随机密码并打印 |
| `lip import clash <file>` | 导入 Clash / Mihomo 配置 |
| `lip import singbox <file>` | 导入 sing-box 配置 |
| `lip import uri <uri>` | 导入单节点 URI（vmess:// / vless:// / ss:// / trojan:// / hysteria2://） |
| `lip export --format clash` | 导出当前配置为 Clash YAML |
| `lip export --format singbox` | 导出当前配置为 sing-box JSON |
| `lip provider update` | 手动触发所有 Provider 更新 |
| `lip provider update <name>` | 更新指定 Provider |
| `lip rule test <domain>` | 测试某个域名会命中哪条规则与哪个出站 |
| `lip dns query <domain>` | 调试 DNS 解析路径与结果 |
| `lip version` | 显示版本、提交哈希、Go 版本、构建时间 |
| `lip upgrade` | 检查并升级到最新版本 |

**守护进程：**

| 命令 | 说明 |
| --- | --- |
| `lipd` | 前台运行守护进程（默认读取 `~/.li/liproxy/liproxy.toml`） |
| `lipd --config /path/to/liproxy.toml` | 指定配置文件启动 |
| `lipd --check` | 只校验配置文件与数据库，不启动服务 |

---

## 目录结构

```text
~/.li/liproxy/
├── liproxy.toml     # 唯一配置文件（引导参数，10 个字段）
├── liproxy.db       # 数据库（节点、规则、DNS、用户、流量、版本历史）
├── backups/         # 自动备份与新版本快照
└── logs/            # 运行日志，按天轮转，默认保留 7 天
```

| 路径 | 是否可手改 | 说明 |
| --- | --- | --- |
| `liproxy.toml` | ✅ | 只有它需要手写。改完重启生效 |
| `liproxy.db` | ❌ | 权威数据源。请通过 Web 或 CLI 修改，不要直接编辑 |
| `backups/` | ❌ | 每次配置变更自动生成，用于回滚 |
| `logs/` | ✅ | 可随时删除，会自动重建 |

---

## 常见问题

**Q1：LiProxy 和 Mihomo / Clash / sing-box 是什么关系？**

LiProxy 是独立实现，不复用它们的代码。设计目标是客户端能力对齐 Mihomo（协议覆盖、规则语义、Provider），服务端能力对齐 sing-box（多用户、订阅、DNS 服务器），并在此基础上提供统一的 Web 管理界面和更低的内存占用。它可以导入它们的配置文件，也可以导出成它们的格式。

**Q2：为什么只有一个配置文件？我想用 YAML 管理节点怎么办？**

因为手写 YAML 在节点数量上百之后会失控。LiProxy 把"进程怎么跑"和"跑什么配置"彻底分开：前者留在 `liproxy.toml`，后者进数据库由 Web 管理。如果你确实喜欢文件化，用 `lip export` 随时导出，用 `lip import` 随时导入，双向都不锁死。

**Q3：端口 9090 被占用了怎么办？**

改 `liproxy.toml` 里的 `[server] port`，然后 `lip restart`。如果只是想临时换端口，也可以 `lipd --config /tmp/liproxy.toml` 用一个临时配置文件启动。

**Q4：忘了管理员密码？**

```bash
lip passwd --reset
```

会生成一个新的随机密码并打印到控制台。如果连 CLI 都用不了，直接删除数据库中的认证记录（或删库重建，代价是配置丢失），重启后会重新走首次启动流程。

**Q5：把 Web 界面暴露到公网安全吗？**

可以，但必须满足三条：`[auth] enabled = true`、密码足够强、建议在前面加一层 HTTPS 反向代理并把 `[server] listen` 保持为 `127.0.0.1`。LiProxy 在检测到监听非回环地址且关闭认证时会拒绝启动。也可以在 **设置 → API Key** 中生成受限的 API Key 给自动化脚本使用。

**Q6：TUN 模式起不来，提示权限不足？**

TUN 需要 root 或 `CAP_NET_ADMIN`：

```bash
sudo setcap cap_net_admin,cap_net_raw+ep $(which lipd)
```

或者直接用 `sudo lipd`。Docker 下需要 `--cap-add=NET_ADMIN --device=/dev/net/tun`。

**Q7：内存占用比宣传的高？**

先看连接数。空闲态如果明显超过 15 MB，通常是：规则集过大（查看 **规则集** 页面的条目数）、日志级别设成了 `debug`、或 TUN 用了 `system` 协议栈。把日志改回 `info`、把超大规则集换成 `RULE-SET` 远程引用通常能显著下降。

**Q8：Provider 更新失败怎么办？**

在 **Provider** 页面点击单条 **立即更新** 查看具体错误。常见原因是订阅链接需要代理才能访问 —— 在 Provider 的 **出站** 字段中指定一个可用的出站即可。另外注意订阅一般有 UA 限制，LiProxy 默认使用通用 UA，必要时可在 Provider 高级设置中自定义。

**Q9：如何备份和迁移到另一台机器？**

```bash
lip stop
tar czf liproxy-backup.tar.gz -C ~/.li liproxy
# 在新机器上解开后 lipd 即可，配置与数据完整保留
```

**Q10：支持订阅链接给其他客户端用吗？**

支持。服务端模式下，Web **用户** 页面为每个用户生成三个订阅链接（Clash / V2Ray / sing-box），可直接粘贴到对应客户端。

---

## 贡献指南

欢迎提交 Issue 和 PR。参与开发前请先阅读 [AGENTS.md](AGENTS.md)（面向开发者与 AI 协作工具的完整指南），其中包含架构说明、代码规范、开发环境搭建与提交流程。

快速上手：

```bash
git clone https://github.com/liproxy/liproxy.git
cd liproxy
make dev      # 同时启动后端热重载与前端 Vite dev server
make test     # 运行全部测试
make lint     # golangci-lint + eslint
```

- 提交前请确保 `make test` 与 `make lint` 通过
- 新增协议请参考 AGENTS.md 中的《新增一个协议的标准流程》
- Commit message 遵循 Conventional Commits
- 大改动请先开 Issue 讨论设计，避免无效实现

## License

[MIT](LICENSE) © LiProxy Contributors
