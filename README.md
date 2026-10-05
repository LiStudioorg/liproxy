# LiProxy

**一个 Web 驱动的全能代理平台 —— 一个二进制文件，一个浏览器标签页，搞定所有代理。**

LiProxy 把 Mihomo 级别的客户端能力和 sing-box 级别的服务端能力装进同一个 Go 二进制里。

**配置就是文件，且永远是你自己的文件。** 所有业务配置写在 `~/.li/liproxy/` 下的主配置与 `toml.d/` `yaml.d/` 片段里，可以用 Git 管理、可以 code review、可以随时手改。Web 界面负责**看**和**调运行时**（切节点、临时规则、日志级别），它不会偷偷改写你的配置文件。

[![Go Version](https://img.shields.io/badge/Go-1.22%2B-00ADD8?logo=go&logoColor=white)](https://go.dev/dl/)
[![License](https://img.shields.io/badge/License-AGPL--3.0-blue.svg)](LICENSE)
[![Build](https://github.com/LiStudioorg/liproxy/actions/workflows/build.yml/badge.svg)](https://github.com/LiStudioorg/liproxy/actions/workflows/build.yml)

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

Web 是**观察台 + 运行时调节器**，不是配置编辑器：

- **只读**：当前生效的 IR、节点、规则、Provider、DNS、连接列表、实时流量曲线、日志
- **可写**：只写 `state/overrides.toml` 里的运行时字段 —— 切换当前节点、临时规则、日志级别、运行模式
- **不写**：主配置与 `toml.d/` `yaml.d/` 里的任何文件。你的配置文件只有一个写入者，就是你自己
- 支持 PWA，可安装到桌面/手机，离线也能打开面板

配套两条命令，让文件与界面互相印证：

```bash
lip config show       # 打印当前生效的完整配置（合并后的结果）
lip config sources    # 打印每个字段来自哪个文件、第几行
```

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
git clone https://github.com/LiStudioorg/liproxy.git
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

首次启动时 LiProxy 会创建数据目录、生成一份默认配置与随机管理员密码，控制台输出如下：

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

## 配置说明

LiProxy 的配置**就是文件**，没有第二份藏在数据库里。

### 目录布局

```text
~/.li/liproxy/
├── liproxy.toml          # 主配置（TOML，优先）
├── liproxy.yaml          # 主配置（YAML，仅当 liproxy.toml 不存在时使用）
├── toml.d/               # TOML 片段，按文件名字典序加载
├── yaml.d/               # YAML 片段，按文件名字典序加载
├── state/                # 运行时状态（删掉可重建，不是配置）
│   ├── li.db             #   会话 · API Key · 审计 · 流量 · 订阅缓存
│   ├── overrides.toml    #   Web 写入的运行时覆盖
│   └── fakeip.db         #   Fake-IP 映射
├── backups/              # 自动备份
└── logs/                 # 运行日志，按天轮转
```

`toml.d/` `yaml.d/` 让你把配置拆成小块：一个文件放节点，一个放规则，一个放 DNS。改哪块改哪个文件，不用在几千行里翻找。

### 加载优先级（低 → 高）

| # | 来源 | 说明 |
| --- | --- | --- |
| 1 | 内置默认值 | 空配置也能启动 |
| 2 | `liproxy.toml` **或** `liproxy.yaml` | 二选一；TOML 存在时完全忽略 YAML |
| 3 | `toml.d/*.toml` | 按文件名字典序 |
| 4 | `yaml.d/*.yaml` | 按文件名字典序 |
| 5 | `state/overrides.toml` | Web 写入，**仅运行时白名单字段** |

> 字典序是逐字节比较，所以 `10-a.toml` 排在 `9-a.toml` **前面**。需要严格顺序就用零填充前缀：`001-`、`002-`。

### 合并规则

| 类型 | 行为 |
| --- | --- |
| 表 / 结构体 | 深度合并，后者覆盖前者 |
| 数组 | **默认追加**，不是替换 |
| `rules` | 追加，可用 `position = "prepend"` 插到前面 |
| 同名出站 / Provider | **报错**，并指出两个来源文件与行号 |

### include：把配置拆开

```toml
include = [
  "extra/nodes.toml",          # 单文件，相对当前文件所在目录
  "rules/*.toml",              # 单层通配符
  "providers/**/*.yaml",       # 递归通配符
  "conf/{dns,log}.toml",       # 花括号展开
]
exclude = ["providers/**/disabled-*.yaml"]

# 需要指定格式或允许缺失时用结构化写法
[[include]]
path     = "remote/rules.yaml"
format   = "yaml"
optional = true
```

支持 `*`、`**`、`?`、`{a,b}`，可显式指定 `format`，`optional = true` 时文件不存在就跳过。

三道防线保证不会把自己写死：循环引用会被检出并打印完整引用链（含符号链接环）、include 深度上限 32 层、单次通配符展开上限 1000 个文件。

### 配置出错会怎样

**坏配置不会让服务挂掉。** 某个片段文件写错语法时，LiProxy 会报出该文件与行号，**继续使用上一份可用的配置**继续运行，并在 Web 顶部显示降级告警。你可以在线修好文件，热重载会自动生效。

### overrides 里能写什么

Web 唯一能写的地方，且只允许这几个字段：

| 允许 | 含义 |
| --- | --- |
| `log.level` | 运行日志级别 |
| `runtime.mode` | `rule` / `global` / `direct` |
| `runtime.active_node` | 当前选中的出站 |
| `runtime.temp_rules` | 内存态临时规则 |

节点、规则、DNS、监听地址**不在**此列 —— 它们只能来自你的配置文件。

### 完整示例

```toml
# ~/.li/liproxy/liproxy.toml

[server]
listen = "127.0.0.1"
port   = 9090

[auth]
enabled  = true
username = "admin"
password = ""          # 留空 = 沿用已有的密码哈希

[tls]
enabled = false
cert    = ""
key     = ""

[database]
enabled = true         # 纯客户端模式可设为 false，不创建数据库
path    = "~/.li/liproxy/state/li.db"

[log]
level = "info"
file  = "~/.li/liproxy/logs/liproxy.log"
```

节点、规则、DNS、入站出站等业务配置同样写在这个文件（或 `toml.d/` `yaml.d/` 片段）里，字段说明见 `docs/`。

### 让改动生效

LiProxy 监听配置文件变化，改完自动热重载，**不需要重启**：

```bash
lip config check      # 先校验，不启动
lip config sources    # 看这个值到底来自哪个文件
lip reload            # 手动触发重载（容器里没监听时用）
```

系统服务模式：

```bash
sudo systemctl restart liproxy
```

---

## Web 界面

![dashboard](docs/images/dashboard.png)

Web 是**观察台 + 运行时调节器**，不是配置编辑器：

- **只读**：当前生效的 IR、节点、规则、Provider、DNS、连接列表、实时流量曲线、日志
- **可写**：只写 `state/overrides.toml` 里的运行时字段 —— 切换当前节点、临时规则、日志级别、运行模式
- **不写**：主配置与 `toml.d/` `yaml.d/` 里的任何文件。你的配置文件只有一个写入者，就是你自己
- 支持 PWA，可安装到桌面/手机，离线也能打开面板

配套两条命令，让文件与界面互相印证：

```bash
lip config show       # 打印当前生效的完整配置（合并后的结果）
lip config sources    # 打印每个字段来自哪个文件、第几行
```

<details>
<summary>更多截图</summary>

| 页面 | 预览 |
| --- | --- |
| 节点总览（只读） | ![proxies](docs/images/proxies.png) |
| 规则查看与命中测试 | ![rules](docs/images/rules.png) |
| 实时连接 | ![connections](docs/images/connections.png) |
| DNS 状态 | ![dns](docs/images/dns.png) |
| 服务端用户与流量 | ![users](docs/images/users.png) |
| 配置来源与运行时覆盖 | ![config](docs/images/config.png) |

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
| `proxies:` | Provider 或节点列表（`toml.d/nodes.toml`） |
| `proxy-groups:` | 策略组（Web 可视化编辑） |
| `rules:` | 规则引擎（同一套类型语法） |
| `dns:` | DNS 页面（含 Fake-IP、DoH/DoT/DoQ） |
| `tun:` | TUN 页面（gVisor / system 双栈可选） |
| `external-controller` | 内置 Web 界面 + REST API |
| 控制台改配置 + 重启 | 改配置文件，实时热重载 |

> 导入只做**合并预览**，不会替你写文件。确认无误后再 `lip export` 落盘 —— 你的配置目录始终只有你自己在写。

---

## CLI 命令速查

| 命令 | 说明 |
| --- | --- |
| `lip start` | 启动代理引擎（前台运行） |
| `lip stop` | 停止正在运行的引擎 |
| `lip reload` | 手动触发配置热重载（改完文件通常自动生效） |
| `lip config show` | 打印当前生效的完整配置 |
| `lip config show --format yaml` | 以 YAML 形式导出当前配置 |
| `lip config sources` | 打印每个字段来自哪个文件、第几行 |
| `lip config check` | 校验配置但不启动 |
| `lip restart` | 重启进程 |
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
| `lipd --check` | 只校验配置文件，不启动服务 |

---

## 目录结构

```text
~/.li/liproxy/
├── liproxy.toml     # 主配置：进程参数 + 全部业务配置
├── liproxy.yaml     # 主配置的 YAML 版本（与 TOML 二选一）
├── toml.d/          # TOML 配置片段
├── yaml.d/          # YAML 配置片段
├── state/           # 运行时状态：数据库、overrides、Fake-IP
├── backups/         # 自动备份
└── logs/            # 运行日志，按天轮转，默认保留 7 天
```

| 路径 | 是否可手改 | 说明 |
| --- | --- | --- |
| `liproxy.toml` / `liproxy.yaml` | ✅ | **权威源**。改完自动热重载 |
| `toml.d/` `yaml.d/` | ✅ | **权威源**。拆分配置用，按文件名字典序加载 |
| `state/` | ⚠️ | 运行时状态，删掉会重建（历史流量与审计随之丢失） |
| `state/overrides.toml` | ⚠️ | 由 Web 生成，手改会被覆盖。业务配置请改上面的文件 |
| `backups/` | ❌ | 自动生成 |
| `logs/` | ✅ | 可随时删除，会自动重建 |

**一句话区分**：`liproxy.toml` 与 `*.d/` 没了，你就丢了配置；`state/` 没了，只是丢了历史。

---

## 常见问题

**Q1：LiProxy 和 Mihomo / Clash / sing-box 是什么关系？**

LiProxy 是独立实现，不复用它们的代码。设计目标是客户端能力对齐 Mihomo（协议覆盖、规则语义、Provider），服务端能力对齐 sing-box（多用户、订阅、DNS 服务器），并在此基础上提供统一的 Web 管理界面和更低的内存占用。它可以导入它们的配置文件，也可以导出成它们的格式。

**Q2：配置到底存在哪？Web 会不会改我的文件？**

存在你自己的文件里 —— `~/.li/liproxy/liproxy.toml` 与 `toml.d/` `yaml.d/`。**Web 不会写这些文件**，它只会写 `state/overrides.toml` 里的运行时字段（切节点、临时规则、日志级别）。这样你可以随时把整个配置目录交给 Git 管理，不会出现"界面上改了一下，diff 里多出一堆看不懂的东西"。

节点多了觉得主配置太长，就拆到 `toml.d/nodes.toml`、`toml.d/rules.toml` 里去。TOML 与 YAML 都支持，混着用也行 —— 两种格式最终编译成同一份 IR，行为完全一致。

**Q3：端口 9090 被占用了怎么办？**

改 `liproxy.toml` 里的 `[server] port`，保存即自动热重载（监听地址变化会重建监听器）。如果只是想临时换端口，也可以 `lipd --config /tmp/liproxy.toml` 用一个临时配置文件启动。

**Q4：忘了管理员密码？**

```bash
lip passwd --reset
```

会生成一个新的随机密码并打印到控制台。如果连 CLI 都用不了，删掉 `state/li.db` 即可 —— **你的配置全在 `liproxy.toml` 和 `*.d/` 里，不会丢**，重启后会重新走首次启动流程。

**Q5：把 Web 界面暴露到公网安全吗？**

可以，但必须满足三条：`[auth] enabled = true`、密码足够强、建议在前面加一层 HTTPS 反向代理并把 `[server] listen` 保持为 `127.0.0.1`。LiProxy 在检测到监听非回环地址且关闭认证时会拒绝启动。也可以在 **设置 → API Key** 中生成受限的 API Key 给自动化脚本使用。

注意 Web 本身**不能改业务配置**，所以即使面板被攻破，攻击者也改不了你的节点与规则 —— 这是新架构顺带带来的安全收益。

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

配置和数据都在一个目录里，整目录拷走即可：

```bash
lip stop
tar czf liproxy-backup.tar.gz -C ~/.li liproxy
# 在新机器上解开后 lipd 即可
```

只想带走**配置**（不含流量与审计历史）的话，拷贝这些就够了，体积也小得多：

```bash
cp -r ~/.li/liproxy/liproxy.toml ~/.li/liproxy/toml.d ~/.li/liproxy/yaml.d /path/to/dest/
```

**Q10：我改了配置文件但没生效？**

按顺序查三步：

```bash
lip config check      # 语法或校验错误会直接指出来
lip config sources    # 看这个字段最终是谁提供的 —— 很可能被更高优先级的文件覆盖了
tail -f ~/.li/liproxy/logs/liproxy.log   # 看热重载日志
```

最常见的原因是优先级：`yaml.d/` 覆盖 `toml.d/`，而 `toml.d/` 覆盖主配置。第二种常见原因是配置有错，LiProxy 保留了上一份可用配置继续运行 —— 这时 Web 顶部会有降级告警。

**Q11：配置写错了会不会把服务搞挂？**

不会。任一文件解析失败时，LiProxy 报出该文件与行号，**继续用上一份可用的配置运行**，并在 Web 顶部显示降级告警。你可以从容地在线修好文件，保存后自动恢复。同理，删掉 `state/` 目录也只是丢历史，配置照样能跑。

**Q12：支持订阅链接给其他客户端用吗？**

支持。服务端模式下，Web **用户** 页面为每个用户生成三个订阅链接（Clash / V2Ray / sing-box），可直接粘贴到对应客户端。

---

## 贡献指南

欢迎提交 Issue 和 PR。参与开发前请先阅读 [AGENTS.md](AGENTS.md)（面向开发者与 AI 协作工具的完整指南），其中包含架构说明、代码规范、开发环境搭建与提交流程。

快速上手：

```bash
git clone https://github.com/LiStudioorg/liproxy.git
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

[AGPL-3.0](LICENSE) © LiProxy Contributors

LiProxy 采用 **GNU Affero General Public License v3.0**。你可以自由使用、修改和分发，但**通过网络提供服务时必须向用户提供完整源代码**（AGPL-3.0 第 13 条）。

```
Copyright (C) 2026 LiProxy Contributors

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program.  If not, see <https://www.gnu.org/licenses/>.
```
