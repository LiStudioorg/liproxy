# AGENTS.md

本文件面向所有在本仓库工作的开发者与 AI 协作工具（Cursor / Claude Code / Codex / Copilot 等）。
读完本文件，你应该能够独立完成一次完整的贡献：改代码、加协议、加 API、写迁移、跑测试、提 PR。

> **给 AI 工具的第一条指令**：动手前先读完第 5 节（核心概念）和第 16 节（常见陷阱）。
> 本项目的绝大多数错误提交都源于违反了这两节里的约定。

---

## 1. 项目概述

LiProxy 是一个用 Go 1.22+ 编写的、Web 驱动的全能代理平台。它把"客户端代理"（对齐 Mihomo 的能力面）和"服务端代理"（对齐 sing-box 的能力面）合并进单个二进制，并用内嵌的 Vue 3 Web 界面取代传统的手写 YAML 配置流程。

**关键定位（三句话）：**

1. **配置文件是权威源（Source of Truth）** —— 业务配置写在 `~/.li/liproxy/` 下的主配置与 `toml.d/` `yaml.d/` 片段里，冲突时以合并结果为准，数据库里没有第二份业务配置。
2. **Web 是观察者与运行时调节器** —— Web 只读当前生效的 IR、连接、流量与日志，唯一的写能力是 `state/overrides.toml` 里的运行时字段（切节点、临时规则、日志级别）。Web 永远不写主配置与片段目录。
3. **数据库降级为运行时状态** —— SQLite 只存会话、API Key、审计、流量日志、订阅缓存与运行时快照，且是**可选组件**：纯客户端模式不创建数据库也能跑。
4. **IR 是唯一的内存表示** —— TOML 与 YAML 都只是前端解析器，解析后统一编译为 IR，核心引擎只认 IR，不认 TOML / YAML。

**硬性目标：**

| 指标 | 目标 |
| --- | --- |
| 空闲内存 | < 15 MB |
| 1000 并发连接内存 | < 100 MB |
| 全协议二进制体积 | ≈ 25 MB |
| 精简版二进制体积 | ≈ 8 MB |
| 外部运行时依赖 | 0（纯 Go SQLite，无 CGO） |

**模块路径**：`github.com/LiStudioorg/liproxy`
**仓库地址**：`https://github.com/LiStudioorg/liproxy`
**主二进制**：`lipd`（守护进程）
**CLI**：`lip`
**License**：AGPL-3.0（见第 18 节）

---

## 2. 架构总览

```text
┌────────────────────────────────────────────────────────────────────┐
│                          Browser (Vue 3 SPA)                        │
│   TanStack Query  ──  REST /api/v1/*        WS /api/v1/ws          │
└───────────────────────────────┬────────────────────────────────────┘
                                │  HTTP / WebSocket
┌───────────────────────────────▼────────────────────────────────────┐
│                        internal/api  (chi router)                  │
│   middleware: 认证(JWT/APIKey) · 限流 · 请求日志 · 错误映射 · CORS  │
│   handlers:   nodes · rules · providers · dns · users · traffic ·  │
│               versions · system · auth                             │
└───────────────────────────────┬────────────────────────────────────┘
                                │  调用服务层，不直接碰 DB
┌───────────────────────────────▼────────────────────────────────────┐
│                        服务层 (service layer)                      │
│   internal/auth      认证、会话、API Key                            │
│   internal/config    IR 编译 / 导入 / 导出 / 版本 diff 与回滚        │
│   internal/provider  订阅拉取、解析、格式转换、定时更新              │
│   internal/server    服务端模式：多用户、流量配额、ACME、订阅生成    │
└───────┬────────────────────────────────────────────┬───────────────┘
        │ 读写（唯一入口）                            │ 编译下发
┌───────▼────────────────────┐            ┌──────────▼───────────────┐
│   internal/db (SQLite)     │            │   internal/engine        │
│   迁移 · 仓储 · 事务        │───读取────▶│   运行时：加载 IR、构建    │
│   modernc.org/sqlite       │            │   入站/出站、热重载、指标  │
└────────────────────────────┘            └──────────┬───────────────┘
                                                     │ 组装
        ┌────────────────────────────────────────────┼───────────────────────┐
        │                    │            │          │          │            │
┌───────▼──────┐  ┌──────────▼───┐ ┌──────▼─────┐ ┌──▼──────┐ ┌─▼────────┐ ┌▼──────────┐
│ internal/    │  │ internal/    │ │ internal/  │ │internal/│ │internal/ │ │internal/  │
│ inbound      │  │ outbound     │ │ transport  │ │ router  │ │ dns /    │ │ sniffer / │
│ 入站监听     │  │ 出站拨号     │ │ TCP/WS/gRPC│ │ 规则引擎│ │ dnsserver│ │ tun       │
└───────┬──────┘  └──────────┬───┘ └──────┬─────┘ └────┬────┘ └────┬─────┘ └─────┬─────┘
        └────────────────────┴────────────┴────────────┴───────────┴─────────────┘
                                          │
                              ┌───────────▼────────────┐
                              │   internal/relay       │
                              │   转发核心（零拷贝）    │
                              └────────────────────────┘
                                          │
                              ┌───────────▼────────────┐
                              │   internal/registry    │
                              │   协议工厂注册表        │
                              └────────────────────────┘
```

**数据流（一次文件配置变更 —— 主路径）：**

```text
用户编辑 主配置 / toml.d/ / yaml.d/
   → 文件监听或 SIGHUP 触发
   → config 扫描：include 展开 → 通配符展开 → 循环检测 → 按优先级合并
   → 校验 + 编译为 IR（失败则保留上一份可用 IR，拒绝热重载并报错到日志）
   → engine 热重载 → 旧连接优雅关闭 → 新配置生效
```

**数据流（一次运行时覆盖 —— Web 的唯一写路径）：**

```text
Web 表单 → API handler → 校验字段属于运行时白名单
        → 写入 state/overrides.toml（原子替换：临时文件 + rename）
        → 走同一套加载与编译流程 → 热重载
        → 若校验失败：回滚 overrides.toml，保留旧 IR，向前端返回错误
```

**数据流（一次连接）：**

```text
inbound 接受连接 → transport 解包 → sniffer 嗅探域名
   → router 匹配规则得到 outbound tag → outbound 拨号
   → relay 双向转发（splice/零拷贝） → 流量计量写回 db（异步批量）
```

---

## 3. 目录结构

```text
liproxy/
├── cmd/
│   ├── lipd/                    # 守护进程入口：解析 flag、加载配置、启动 API 与 engine
│   └── lip/                     # CLI 入口：子命令注册、与 lipd 通过本地 API 通信
├── internal/
│   ├── config/                  # 【权威源】文件加载 · include 展开 · 合并 · 校验 · 编译 IR
│   │                            #   不 import db；db 反过来依赖它
│   ├── db/                      # SQLite：运行时状态（会话 · API Key · 审计 · 流量 · 订阅缓存）
│   │                            #   迁移 · 事务 · 仓储（唯一允许写 SQL 的地方）；可选启用
│   ├── registry/                # 协议工厂注册表：按类型名创建 inbound/outbound
│   ├── inbound/                 # 入站协议实现（http、socks、ss、vmess、vless、trojan…）
│   ├── outbound/                # 出站协议实现（与 inbound 同源，独立目录组织拨号逻辑）
│   ├── transport/               # 传输层：tcp/raw、ws、grpc、h2、quic、xhttp
│   ├── router/                  # 规则引擎：Trie / Radix / Aho-Corasick 匹配与出站选择
│   ├── provider/                # 订阅与规则集：拉取、格式识别、转换、定时更新
│   ├── dns/                     # DNS 客户端：Fake-IP、DoH/DoT/DoQ、Bootstrap、分流
│   ├── dnsserver/               # 自建 DNS 服务端：UDP/TCP/DoT/DoH/DoQ 入站与解析管线
│   ├── sniffer/                 # 流量嗅探：TLS SNI、HTTP Host、QUIC 域名提取
│   ├── tun/                     # TUN 设备：gVisor netstack 与 system 协议栈
│   ├── server/                  # 服务端模式：多用户、流量计量与配额、ACME、订阅生成
│   ├── api/                     # REST + WebSocket：路由、handler、DTO、中间件
│   ├── auth/                    # JWT、Refresh Token、API Key、密码哈希
│   ├── engine/                  # 运行时引擎：接收 IR、构建实例、热重载、统计上报
│   └── relay/                   # 转发核心：双向拷贝、splice 零拷贝、超时与统计
├── web/                         # 前端：Vue 3 + TS + Vite（构建产物 web/dist 嵌入二进制，见第 19 节）
│   ├── embed.go                 # 唯一持有 //go:embed all:dist 的 Go 文件
│   ├── dist/                    # 前端构建产物（生成物，不入库；仅保留占位 index.html）
│   ├── src/
│   │   ├── pages/               # 页面级组件，与路由一一对应
│   │   ├── components/          # 复用组件，shadcn-vue 基础组件在 components/ui/
│   │   ├── api/                 # 后端接口封装（统一走封装的 fetch 客户端）
│   │   ├── stores/              # Pinia 全局状态（仅放跨页面状态）
│   │   └── composables/         # 组合式函数（TanStack Query 封装）
│   └── public/                  # 静态资源与 PWA manifest
├── pkg/
│   ├── bufpool/                 # 分级 buffer pool
│   ├── strpool/                 # 字符串驻留
│   ├── log/                     # zerolog 封装与脱敏（只输出到 stdout）
│   └── version/                 # 版本号、commit、构建时间（ldflags 注入）
├── docs/                        # 文档与截图（docs/images/ 供 README 引用）
├── Makefile                     # 构建、测试、lint、生成等目标
├── LICENSE                      # AGPL-3.0 全文
└── go.mod
```

**用户侧配置目录**（不在仓库里，是运行时的权威源）：

```text
~/.li/liproxy/
├── liproxy.toml          # 主配置（TOML，优先）
├── liproxy.yaml          # 主配置（YAML，仅当 liproxy.toml 不存在时启用）
├── toml.d/               # TOML 片段，按文件名字典序加载
├── yaml.d/               # YAML 片段，按文件名字典序加载
├── state/                # 运行时状态（非权威）
│   ├── li.db             #   SQLite：会话 · API Key · 审计 · 流量 · 订阅缓存
│   ├── overrides.toml    #   Web 写入的运行时覆盖（仅白名单字段）
│   └── fakeip.db         #   Fake-IP 映射持久化
└── backups/              # 自动备份（写 overrides 前先备份）
```

> `state/` 与 `backups/` 里的一切都**不是权威源**：删掉它们，服务能重建；删掉主配置与 `*.d/`，服务就失去了业务配置。这条区分是排查问题的第一把尺子。

**没有日志目录。** LiProxy 只把结构化日志写到 stdout，不落盘、不做轮转 —— 收集与保留交给 systemd journal、Docker logging driver 或你选择的日志系统（见第 4 节与第 8 节）。

**包依赖方向（硬约束）：**

```text
cmd/lipd ──▶ config ──▶ (IR) ──▶ engine ──▶ registry
                │                              ▲
                └──▶ db（仅运行时状态）─────────┘ 协议实现经 registry 自注册
```

- `internal/config` **不 import `internal/db`**：配置来自文件系统，与数据库无关
- `internal/db` 可以 import `internal/config`（例如把 IR 指纹写进运行时快照）
- 反向依赖一旦出现，就说明有人想把数据库变回权威源 —— 这正是本次架构调整要根除的东西

---

## 4. 技术栈与依赖

### 后端

| 类别 | 选型 | 说明 |
| --- | --- | --- |
| 语言 | Go 1.22+ | 使用 `log/slog` 之外自选的 zerolog，泛型、`min`/`max` 内建可用 |
| HTTP 路由 | `github.com/go-chi/chi/v5` | 轻量、标准 `net/http` 兼容 |
| 日志 | `github.com/rs/zerolog` | 零分配 JSON 日志，统一入口 `pkg/log`；**只输出 stdout，不写文件、不做轮转** |
| QUIC | `github.com/quic-go/quic-go` | Hysteria2 / TUIC / DoQ / QUIC 传输 |
| 用户态网络栈 | `gvisor.dev/gvisor/pkg/tcpip` | TUN gVisor 模式 |
| 数据库 | `modernc.org/sqlite` | 纯 Go，无 CGO；**仅存运行时状态，可选启用** |
| TOML | `github.com/BurntSushi/toml` | 主配置与 `toml.d/` 解析 |
| YAML | `gopkg.in/yaml.v3` | `yaml.d/` 解析 + Clash / sing-box 导入导出 |
| 通配符 | 标准库 `path/filepath.Glob` + 自实现 `**` | 见第 21 节；不引入第三方 glob 库 |
| 配置校验 | 自实现结构体校验（首选）或 CUE | 见第 21 节校验小节 |
| 认证 | JWT + Refresh Token + API Key | 自实现，见 `internal/auth` |

### 前端

| 类别 | 选型 | 说明 |
| --- | --- | --- |
| 框架 | Vue 3（Composition API + `<script setup>`） | 禁用 Options API 混写与 class 组件 |
| 语言 | TypeScript（`strict: true`） | 禁止 `any` |
| 构建 | Vite（`base: "./"`，`build.outDir: "dist"`） | 相对 base 是 embed 能工作的前提 |
| UI | shadcn-vue + Tailwind CSS | 基础组件在 `components/ui/`，不手改 |
| 数据获取 | TanStack Query for Vue（`@tanstack/vue-query`） | 服务端数据的唯一缓存层 |
| 客户端状态 | Pinia | 只放跨页面客户端状态 |
| 路由 | Vue Router（history 模式） | 依赖 Go 侧 SPA fallback |
| 图表 | vue-echarts | 已定案，不再更换 |
| PWA | `vite-plugin-pwa` | 关闭运行时外部资源下载 |

**前端硬性约束：**

1. 组件命名 `PascalCase.vue`；页面级放 `web/src/pages/`，可复用组件放 `web/src/components/`
2. 统一 `<script setup lang="ts">`，不写 `export default {}`
3. **禁止 CDN 引用**；字体、图标、图表资源必须本地打包，不得运行时下载
4. `web/dist/` 为生成物，不入库；gzip 后总体积 **< 2 MB**（见第 19 节体积红线）

**依赖准入规则：** 新增任何第三方依赖前必须确认三件事 ——（1）许可证与 AGPL-3.0 兼容（宽松协议 MIT/Apache-2.0/BSD/ISC 可直接用，完整对照表见第 18 节；专有、SSPL、BSL、无许可证的一律禁止）；（2）不引入 CGO；（3）二进制体积增量可接受（普通依赖 < 500 KB，网络栈类需单独讨论）。任何违反的依赖必须在 PR 描述中说明理由。

---

## 5. 核心概念

### IR（Intermediate Representation）

IR 是 LiProxy 配置的唯一内存表示，定义在 `internal/config`。TOML 与 YAML 文件里的每一个结构（节点、规则、DNS、入站、出站、策略组…）都会被编译成 IR 中的一个结构体，引擎只认 IR，不认 TOML、不认 YAML。

- **唯一入口**：`config.Compile(files *FileSet) (*IR, error)`
  - `FileSet` 是"已展开、已排序、已读取"的配置片段集合，由 `Load` 阶段产出
  - `Compile` 不碰文件系统、不碰网络，纯粹把内存中的 `FileSet` 变成 IR
- **不变量**：IR 必须是不可变的（编译完成后不再修改），热重载 = 编译出新 IR 并整体替换
- **零反射**：IR 的序列化/反序列化走手写代码或代码生成，禁止在热路径上用 `encoding/json` + 反射处理 IR
- **双格式同构**：TOML 与 YAML 解析到**同一个中间结构**后再合并，合并逻辑只写一份。不允许"TOML 走一条合并路径、YAML 走另一条"

**两段式设计（重要）：**

```text
Load 阶段（有 IO）                    Compile 阶段（纯函数）
扫描目录 → 展开 include/通配符          FileSet → 合并 → 校验 → IR
→ 读文件 → 记录来源行号                 ｜
→ 产出 FileSet（含 provenance）         └─ 相同 FileSet 必然产出相同 IR
```

把 IO 与合并彻底分开，是为了让合并与校验逻辑可以被完整单测覆盖 —— 测试只需要构造 `FileSet`，不需要伪造文件系统。这条边界一旦被打破（有人在 `Compile` 里读文件），第 16 节的陷阱 9 就会重现。

### Registry（协议工厂注册表）

`internal/registry` 维护"协议类型名 → 工厂函数"的映射：

```go
// internal/registry/registry.go (示意)
type InboundFactory func(ctx context.Context, spec IRS inbound.Inbound, opts Deps) (inbound.Inbound, error)
```

- 协议实现在自己的包内 `func init()` 中调用 `registry.RegisterInbound("vless", New)` 完成自注册
- 核心引擎（`engine`、`relay`、`router`）**永远不 import 具体协议包**，只 import `registry` 与接口定义
- 副作用：新增协议时，唯一不会被修改的核心文件是 `cmd/lipd`（通过 blank import 汇总注册）

### Bootstrap（已废除）

旧设计中 `internal/bootstrap` 是一个只解析"10 个引导字段"的特殊通道，业务配置走数据库。**该包已被删除**，不存在"引导配置"与"业务配置"两条路径：

- 所有配置（监听地址、认证、日志、节点、规则、DNS…）都在同一套文件与同一套合并规则里
- `config` 包是唯一的配置入口，没有例外通道
- 若某个字段需要在数据库可用之前就生效（例如 `database.path` 本身），它仍然是主配置里的普通字段，只是被加载流程更早读取 —— 不因此获得特权地位

### 运行时状态与历史（Database）

数据库不再承载业务配置，它只回答"运行期间发生了什么"：

| 表 | 用途 | 权威性 |
| --- | --- | --- |
| `sessions` | Web 登录会话 | 运行时状态 |
| `api_keys` | API Key（哈希存储） | 运行时状态 |
| `audit_logs` | 审计记录，只增不删 | 运行时状态 |
| `runtime_state` | 当前加载的文件集合指纹、启动时间、选中节点等 | 运行时状态 |
| `traffic_logs` | 流量计量明细（服务端模式） | 运行时状态 |
| `users` | 服务端模式的多用户与配额 | 运行时状态 |

关键点：

- **`config_versions` 表已删除**。配置历史的权威载体是用户自己的文件，不是数据库。想要版本管理，用户应该用 Git 管 `~/.li/liproxy/`，而不是让 LiProxy 造一个平行版本库
- `runtime_state.config_fingerprint` 记录**当前生效 IR 的来源文件指纹**（排序后的路径 + 内容 sha256），用于判断热重载是否真的需要发生，以及排查"我改了文件为什么没生效"
- `database.enabled = false` 时，`internal/db` 完全不被实例化，以上功能按降级语义运行（无会话持久化、无审计落库、流量只累计在内存）
- **数据库不可用绝不能导致配置不可用**：配置来自文件，数据库只是旁路。这是与旧设计最本质的差别

### Provider

`internal/provider` 负责从外部订阅源拉取并转换为本地节点/规则集。

- 支持的输入格式：Clash YAML、V2Ray Base64、sing-box JSON、单节点 URI
- 支持的输出：IR 中的 `Node` 或 `RuleSet`
- 更新策略：定时（可配置间隔）+ 手动触发；更新失败保留上一份成功结果并记录错误
- 拉取必须走可配置的出站（部分订阅源需要代理才能访问），且必须设置超时与响应体大小上限

### Sniffer

`internal/sniffer` 从连接的首个（或前几个）数据包中提取域名信息：

- TLS → SNI（ClientHello 解析）
- HTTP → Host 头
- QUIC → Initial 包中的 SNI
- 嗅探结果回填到连接元数据，使**基于域名的规则**在目标为纯 IP 的连接上依然生效
- 嗅探必须**不阻塞转发**：解析失败立即放行，嗅探有独立超时（默认 300ms）

### Reality

Reality 是一种无需自建域名与证书的 TLS 伪装方案：

- **服务端**：借用真实站点的证书链完成握手，只有持有私钥的客户端能通过
- **客户端**：校验服务端返回的证书是否来自预设的 `serverName`，并保持 uTLS 指纹一致
- 关键字段：`dest`（回落到哪个真实站点）、`serverNames`、`privateKey` / `publicKey`、`shortId`、`fingerprint`
- 相关实现位于 `internal/transport/tls/reality`，与 uTLS 指纹配置强耦合，修改时请同时验证指纹一致性

---

## 6. 代码规范

### Go

**命名**

- 包名小写单词，不用下划线，不用复数（`provider` 而非 `providers`）
- 导出标识符必须有注释，注释以标识符名开头
- 接口名以行为命名（`Dialer`、`Sniffer`、`Matcher`），不要 `I` 前缀
- 缩写词保持大小写一致：`URL`、`ID`、`IP`、`TLS`、`DNS`，不要 `Url`、`Id`

**错误处理**

- 一律显式检查，禁止 `_ = err`
- 包装用 `fmt.Errorf("...: %w", err)`，保留错误链
- 领域错误定义在各自包的 `errors.go` 中，用 `errors.Is` / `errors.As` 判断，禁止字符串比较错误内容
- API 层统一把领域错误映射为 HTTP 状态码（见 `internal/api/errors.go`），handler 里不要手写状态码分支
- 禁止 `panic`：除程序初始化阶段的不可恢复错误外，一律返回 error

**日志**

- 统一使用 `pkg/log` 暴露的 logger，禁止直接 `fmt.Println` 或自建 `zerolog.Logger`
- **只写 stdout**：禁止在代码里打开日志文件、建目录、做轮转。日志收集是部署层的事，交给 systemd journal / Docker logging driver / 日志采集器
- 结构化字段优先：`log.Info().Str("protocol", "vless").Int("port", 443).Msg("inbound started")`
- 热路径（每连接、每包）日志必须为 `Debug` 级别，且使用 `log.Debug()` 的惰性求值形式
- **禁止**在日志中输出：密码、私钥、UUID、JWT、API Key、订阅原文（见第 14 节）

**注释**

- 注释解释**为什么**，不解释**是什么**
- 每个 `internal/` 包的 `doc.go` 用一段话说明该包职责与不变量
- 涉及并发、内存池、生命周期的地方必须写明所有权（谁分配、谁释放、是否可跨 goroutine）

**并发**

- 每个 goroutine 必须有明确的退出路径，接受 `context.Context` 并响应 `ctx.Done()`
- 共享状态用 channel 或 `sync.Mutex` 保护，禁止裸 map 并发读写
- 热重载期间旧实例的关闭必须优雅：先停止接受新连接，再等待存量连接（有超时上限）

### 前端（Vue 3）

**组件**

- 一律单文件组件（SFC）+ Composition API，统一 `<script setup lang="ts">`；禁止 Options API 混写、禁止 class 组件
- 一个文件一个主组件，文件名 `PascalCase.vue`
- 页面级组件放 `src/pages/`，可复用组件放 `src/components/`
- shadcn-vue 组件放在 `src/components/ui/`，**不要手改**，需要定制时在业务层包装
- 可复用的有状态逻辑放 `src/composables/useXxx.ts`，命名以 `use` 开头

**状态**

- 服务端数据一律用 TanStack Query（`@tanstack/vue-query`），**禁止用 Pinia 缓存接口数据**
- Pinia 只放跨页面的客户端状态（主题、侧边栏折叠、当前选中节点）
- Query key 统一从 `src/api/keys.ts` 取，禁止在组件里手写字符串数组

**样式**

- 只用 Tailwind 原子类，禁止内联 `style`（动画等极端情况除外并注明原因）
- 颜色/间距一律使用 Tailwind 主题变量，禁止硬编码十六进制色值
- 响应式从移动端向上写（`sm:` → `md:` → `lg:`）

**类型**

- 开启 `strict`，禁止 `any`（必要时用 `unknown` + 类型收窄）
- 后端 DTO 类型在 `src/api/types.ts` 手动维护，字段与后端 `internal/api/dto.go` 一一对应

---

## 7. 开发环境搭建

**前置要求**

| 工具 | 版本 | 说明 |
| --- | --- | --- |
| Go | ≥ 1.22 | 建议使用最新稳定版 |
| Node.js | ≥ 20 | 前端构建 |
| pnpm | ≥ 9 | 前端包管理，不使用 npm/yarn |
| Make | 任意 | 统一入口，所有命令都从 Makefile 走 |
| SQLite CLI | ≥ 3.40 | 仅调试用，`sqlite3 state/li.db`（数据库可选，纯客户端模式不创建） |

**初始化**

```bash
git clone https://github.com/LiStudioorg/liproxy.git
cd liproxy

# 后端依赖
go mod download

# 前端依赖
cd web && pnpm install && cd ..
# 前端源码约定：pages/ · components/(ui/ 放 shadcn-vue) · api/ · stores/(Pinia) · composables/

# 安装开发工具（golangci-lint、air、mockgen 等）
make tools

# 启动开发环境（后端 air 热重载 + 前端 Vite dev server）
make dev
```

`make dev` 启动后：前端 `http://127.0.0.1:5173`（带 HMR，API 请求代理到 9090），后端 `http://127.0.0.1:9090`。

**常用 Makefile 目标**

| 目标 | 说明 |
| --- | --- |
| `make dev` | 并行启动后端热重载与前端 dev server |
| `make build` | 构建前端并打包全协议二进制到 `bin/lipd`、`bin/lip` |
| `make build-minimal` | 构建不含重型协议的精简版（≈ 8 MB） |
| `make build-frontend` | 只构建前端产物（不跑 Go 构建） |
| `make test` | 运行全部单元测试与集成测试 |
| `make test-unit` | 只跑单元测试（`-short`） |
| `make test-race` | 带 `-race` 运行测试，提交 PR 前必跑 |
| `make lint` | `golangci-lint run` + `pnpm lint` + `pnpm typecheck` |
| `make fmt` | `gofumpt -w .` + `pnpm format` |
| `make gen` | 代码生成（迁移校验、DTO 同步、mock） |
| `make bench` | 运行基准测试并输出内存分配报告 |
| `make clean` | 清理 `bin/`、`web/dist/` 与测试缓存 |

**配置与状态调试**

```bash
# 配置：查看合并后的最终结果（不启动服务）
lip config show                  # 打印当前生效的 IR（等价于 TOML/YAML）
lip config show --format yaml    # 以 YAML 形式导出
lip config sources               # 打印每个字段来自哪个文件、第几行
lip config check                 # 校验但不启动

# 运行时状态（数据库只是旁路）
sqlite3 ~/.li/liproxy/state/li.db ".tables"
sqlite3 ~/.li/liproxy/state/li.db "SELECT * FROM runtime_state;"
```

`lip config sources` 是排查配置问题时最常用的命令：它回答"这个值到底是谁写的"，包括 include 链上每一层的贡献。

---

## 8. 构建与测试命令

```bash
# 完整构建（前端嵌入 + ldflags 注入版本信息）
make build

# 等价于
cd web && pnpm install --frozen-lockfile && pnpm build && cd ..
go build -trimpath -ldflags "-s -w \
  -X github.com/LiStudioorg/liproxy/pkg/version.Version=$(git describe --tags --always) \
  -X github.com/LiStudioorg/liproxy/pkg/version.Commit=$(git rev-parse --short HEAD) \
  -X github.com/LiStudioorg/liproxy/pkg/version.BuildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -o bin/lipd ./cmd/lipd
```

**测试**

```bash
make test                       # 全部测试
make test-race                  # 竞态检测（必跑）
go test ./internal/router/...   # 单包测试
go test -run TestMatch -v ./internal/router/...
go test -bench=. -benchmem ./internal/relay/...
```

**测试约定**

- 测试文件与被测文件同目录，命名 `xxx_test.go`
- 优先使用表驱动测试
- 涉及网络与文件的测试使用 `t.TempDir()` 与本地 `127.0.0.1` 随机端口（`:0`），禁止访问真实外网
- 协议实现必须有**回环测试**：同进程内启动 inbound + outbound，完成一次真实数据往返
- 修复 bug 必须附带能复现该 bug 的测试用例
- 新增协议或路由逻辑的 PR，覆盖率不得低于该包原有水平

**构建产物验证**

```bash
./bin/lip version               # 确认版本信息已注入
./bin/lipd --check              # 校验配置与数据库
ls -lh bin/lipd                 # 确认体积符合预期
```

---

## 9. 新增一个协议的标准流程

以新增一个名为 `Foo` 的协议为例（它同时是入站和出站）。

**第 1 步：确定接口契约**

阅读 `internal/inbound/inbound.go` 与 `internal/outbound/outbound.go`，确认要实现的方法（通常包括 `Start(ctx)`、`Close()`、`Address()` 或 `Dial(ctx, metadata)`）。

**第 2 步：创建协议包**

```text
internal/inbound/foo/
├── doc.go        # 包职责说明与协议要点
├── inbound.go    # 入站实现
├── inbound_test.go
├── outbound.go   # 出站实现（若与入站同源，可共享编解码）
└── codec.go      # 协议编解码，独立可测
```

**第 3 步：实现 IR 配置结构**

在 `internal/config/` 下新增该协议的 IR 结构，并在 `Compile` 中补上"中间结构 → IR"的转换分支。协议字段同时要在 TOML 与 YAML 两侧都能解析（见第 21 节的双格式同构要求）。**注意兼容性规则见第 12 节。**

**第 4 步：自注册**

```go
// internal/inbound/foo/inbound.go
func init() {
    registry.RegisterInbound("foo", New)
    registry.RegisterOutbound("foo", newOutbound)
}
```

**第 5 步：在聚合处 blank import**

在 `internal/registry/all/all.go`（聚合注册入口）中添加 `_ "github.com/LiStudioorg/liproxy/internal/inbound/foo"`。
**不要**在 `engine`、`relay`、`router` 中 import 该包。

**第 6 步：配置与前端**

- 协议类型名登记在 `internal/config/protocol.go` 的枚举里，TOML/YAML 解析与校验共用这一处
- 若协议有专属字段，在 `internal/config` 的结构体与校验规则中补齐，**不需要数据库迁移**（协议配置不进库）
- 在 `docs/protocols/foo.md` 补字段表，并在 README 的协议列表中登记
- 前端在 Web 的节点只读视图与「配置片段编辑提示」中登记该协议名

**第 7 步：测试**

必须包含：

1. `codec_test.go` —— 协议编解码的表驱动测试
2. `inbound_test.go` —— 在同进程内 `127.0.0.1:0` 起监听，用真实客户端完成一次握手
3. 回环测试 —— 用本协议的出站连接本协议的入站，传输 1 MB 随机数据并校验
4. 错误路径 —— 错误密码/错误 UUID/被中断连接必须返回可判定的错误而不是 panic

**第 8 步：文档**

- 在 README 的协议列表中登记
- 在 `docs/protocols/foo.md` 中补齐字段说明与示例

**第 9 步：验证**

```bash
make lint
make test-race
make build && ./bin/lip status
```

**检查清单：** 核心引擎零 import ✅ ／ 数据库迁移已写 ✅ ／ 回环测试通过 ✅ ／ 前端表单可用 ✅ ／ 体积增量已测量 ✅

---

## 10. 新增一个 API 的标准流程

**第 1 步：确定资源与路径**

遵循 REST 风格：`/api/v1/<resource>`。集合用复数，动作型接口用子路径（`/api/v1/providers/{id}/update`）。

**第 2 步：定义 DTO**

在 `internal/api/dto/` 下新增结构体，字段带 `json` tag。DTO **必须**与数据库模型解耦 —— 不要把 `db.Node` 直接序列化出去（会泄露内部字段、且迁移时会破坏 API 兼容）。

**第 3 步：写 service 方法**

业务逻辑放服务层，handler 只做：解析参数 → 调用 service → 映射错误 → 返回响应。**handler 中禁止出现 SQL。**

**第 4 步：写 handler 并注册路由**

```go
// internal/api/router.go
r.Route("/api/v1", func(r chi.Router) {
    r.Use(mw.Auth)                     // 认证中间件
    r.Get("/nodes", h.ListNodes)       // 需要认证
    r.Group(func(r chi.Router) {
        r.Use(mw.RequireScope("nodes:write"))   // 写操作额外要求 scope
        r.Post("/nodes", h.CreateNode)
    })
})
```

**第 5 步：鉴权与 scope**

每个写操作必须声明所需 scope（`nodes:write`、`dns:write`、`users:admin` 等）。新增 scope 需同步更新 `internal/auth/scope.go` 与 Web 的 API Key 管理界面。

**第 6 步：区分只读与可写接口**

这是本次架构调整后最容易写错的地方，写接口必须先过这道判断：

| 接口类型 | 例子 | 实现方式 |
| --- | --- | --- |
| **只读配置** | `GET /api/v1/config`、`GET /api/v1/nodes`、`GET /api/v1/rules` | 只读当前 IR，**不碰文件** |
| **写运行时覆盖** | `POST /api/v1/overrides`、`PATCH /api/v1/runtime/active-node` | 只写 `state/overrides.toml`，字段必须在白名单内 |
| **写运行时状态** | `POST /api/v1/auth/login`、`POST /api/v1/api-keys` | 写数据库（或内存，当数据库禁用时） |
| **导出/导入** | `POST /api/v1/config/export`、`POST /api/v1/config/import` | 导出返回文本；导入**只返回合并预览**，落盘由用户自己做 |

**禁止**出现任何直接写主配置、`toml.d/`、`yaml.d/` 的 API。发现了就是架构违规，不是"功能增强"。

覆盖白名单是硬编码的字段集合（如 `log.level`、`runtime.active_node`、`runtime.temp_rules`），实现上是一个显式的 map，新增字段必须同时改这里与文档。

**第 7 步：错误映射**

用领域错误，让统一中间件映射状态码：`ErrNotFound` → 404，`ErrConflict` → 409，`ErrInvalid` → 400，`ErrUnauthorized` → 401，`ErrForbidden` → 403，其余 → 500 且**不外泄内部错误文本**。

**第 8 步：测试**

- handler 层测试用 `httptest.NewServer` + 真实内存数据库
- 必须覆盖：未认证 401、越权 403、参数非法 400、正常路径 200/201
- 涉及并发的写接口加一个并发测试（验证 version 不会串号）

**第 9 步：前端对接**

在 `web/src/api/` 添加调用封装，在 `keys.ts` 添加 query key，在页面中使用 TanStack Query 的 `useQuery` / `useMutation`，写操作成功后 `invalidateQueries`。

---

## 11. 数据库迁移规范

> 适用范围：**仅运行时状态表**（见第 5 节）。业务配置不进数据库，因此新增协议、新增节点字段、新增规则类型**都不需要迁移** —— 只需要改 `internal/config`。这条边界务必记牢，它是本次架构调整的核心收益。

**文件组织**

```text
internal/db/migrations/
├── 0001_init.up.sql
├── 0001_init.down.sql
├── 0002_add_provider_interval.up.sql
├── 0002_add_provider_interval.down.sql
└── ...
```

**命名规则**

- 四位递增序号 + 下划线 + 小写下划线描述：`0012_add_user_quota`
- 序号**永不重用**，即使某个迁移被回滚删除
- 每个迁移必须同时提供 `.up.sql` 与 `.down.sql`

**编写要求**

1. **单向前进**：只允许新增表、新增列、新增索引。删除或重命名列必须先经历"标记废弃 → 代码不再读写 → 下个版本删除"三个阶段
2. **禁止破坏性变更**：不允许在迁移中 `DROP COLUMN` / `DROP TABLE`，除非该列已废弃至少一个发布周期并在 PR 中明确说明
3. **新增列必须给默认值**或允许 NULL，否则旧数据行会导致迁移失败
4. **索引命名**：`idx_<table>_<columns>`；唯一索引 `uniq_<table>_<columns>`
5. **迁移必须可在事务中执行**（SQLite 的 DDL 支持事务）
6. **不要写数据迁移与结构迁移混在一起**的大文件，拆成两个迁移更易回滚
7. **迁移中禁止引用 Go 代码或外部服务**，纯 SQL

**测试要求**

每个迁移必须配套一个测试，验证三件事：

```go
func TestMigration0012(t *testing.T) {
    // 1. 从空库迁移到当前版本，无错误
    // 2. 从上一版本迁移到当前版本，且旧数据保留
    // 3. down 迁移可执行（若声明支持），且数据不损坏
}
```

**版本表**

SQLite 使用 `schema_migrations(version INTEGER PRIMARY KEY, applied_at TIMESTAMP)` 记录已应用版本。启动时自动迁移。

但注意语义变化：数据库是可选的，所以"迁移失败拒绝启动"只在 `database.enabled = true` 时成立。数据库启用但迁移失败时，**拒绝启动**并打印明确错误，不要静默继续，也不要"降级为无数据库运行" —— 后者会让用户以为审计与配额正常工作，实际在静默丢数据。

---

## 12. IR 变更规范

IR 是引擎与配置之间的契约，也是导入导出的中间格式。变更 IR 的风险高于普通代码改动。

**新增字段（安全）**

1. 新字段必须是**可选**的，零值必须有明确且向后兼容的语义
2. 在结构体注释中标注引入版本：`// Since v0.4`
3. 反序列化时，旧快照缺失该字段 → 取零值 → 使用默认行为，不得报错
4. 同步更新：TOML 解析、YAML 解析、`Compile` 转换、导出器、Web 只读视图、API DTO（**不再涉及 DB 迁移**）

**修改字段语义（危险）**

- 不允许原地改变字段含义。正确做法：新增字段 `foo_v2`，保留 `foo` 用于读取旧数据，在下个主版本移除 `foo`
- 若语义变更会影响到已导出的配置文件，必须在 CHANGELOG 中标注为 **BREAKING**

**删除字段**

- 必须经历：标记 `Deprecated` → 至少一个次版本仍然解析该字段 → 主版本删除
- 删除时同时清理：迁移、Compile、导入导出、前端表单

**兼容性检查清单**

- [ ] 旧版本导出的 TOML / YAML 能被新版本正确导入
- [ ] 用户手写的旧格式配置文件仍然能被解析（这是最重要的兼容性来源）
- [ ] 新版本导出的配置，在旧版本中导入的行为是"忽略未知字段"而非崩溃
- [ ] 往返测试：`load → IR → export → load` 结果一致（必须有 round-trip 测试）
- [ ] 同一份配置用 TOML 写与用 YAML 写，编译出的 IR **完全相等**（双格式同构测试）

**编译期不变量**

- `Compile` 必须是纯函数：相同 `FileSet` 产生相同 IR，不读文件系统、不读全局状态、不发网络请求
- `Compile` 失败必须返回带上下文的错误，指出**是哪个文件、第几行、哪个字段**有问题 —— 用户看不到数据库，只能看文件和日志，错误信息必须精确定位到文件
- 校验失败时**保留上一份可用 IR**，服务继续以旧配置运行，而不是崩溃或清空配置

---

## 13. 内存优化注意事项

内存是本项目的核心卖点，任何 PR 如果让空闲内存或千并发内存显著上升，都应在描述中说明理由。

**禁止分配的地方（热路径）**

- **每连接/每包路径**禁止 `make`、`new`、闭包逃逸、`fmt.Sprintf`、字符串拼接
- 禁止在热路径使用 `encoding/json`（反射 + 大量分配）。IR 相关序列化走手写或生成代码
- 禁止在热路径把 `[]byte` 转 `string`（会复制）；需要只读视图时用 `unsafe.String` 并有明确的所有权约束
- 禁止 `time.After` 在循环中创建（每次迭代泄漏一个 timer），改用 `time.NewTimer` + `Reset`
- 禁止在热路径用 `sync.Map`，其内部装箱会产生额外分配

**必须池化或复用的地方**

- **缓冲区**：所有网络拷贝使用 `pkg/bufpool` 的分级池（小包 / 1 KB / 4 KB / 16 KB / 64 KB），用 `defer bufpool.Put(buf)` 归还
- **字符串**：高频重复的目标域名、出站 tag、规则名走 `pkg/strpool` 驻留，减少重复字符串的堆占用
- **连接元数据**：`Metadata` 结构体随连接创建，尽量预分配到池中复用
- **规则匹配结构**：域名规则用 Trie/Radix，关键词规则用 Aho-Corasick，在编译期构建一次，运行期只读

**零拷贝**

- 转发路径优先使用 `splice(2)`（Linux）进行内核态零拷贝，回退到 `io.CopyBuffer` + 复用缓冲区
- 禁止在转发路径上做"先读全部再写"的实现，必须流式转发

**GC 调优**

- 可调 `GOGC` / `GOMEMLIMIT`，默认策略见 `cmd/lipd/main.go`
- 避免大对象长期驻留；大量小对象优先用值类型与 slice 复用

**验证方式**

```bash
make bench                                  # 基准测试与分配报告
go test -bench=. -benchmem -run=^$ ./internal/relay/
go build -gcflags="-m" ./internal/relay/    # 检查逃逸分析
```

新增或修改热路径代码，必须在 PR 中附上修改前后的 `-benchmem` 对比（allocs/op 与 B/op）。

---

## 14. 安全注意事项

**敏感字段清单**（一经出现在日志/错误响应中即为安全缺陷）

- 密码、密码哈希、JWT、Refresh Token、API Key
- 节点凭据：UUID、密码、私钥、预共享密钥、Reality `privateKey`
- 订阅链接（含 token 的 URL）、Provider 认证信息
- 用户流量明细中的可关联身份信息

**日志脱敏**

- `pkg/log` 提供 `log.Redact(s string) string`，输出敏感串时使用它
- 结构体整体打印时必须实现 `LogValue()` 或显式构造安全字段列表
- 排查问题时优先输出**指纹**（如 `sha256` 前 8 位）而不是原文
- 错误响应给客户端时只返回通用消息 + 错误码，详细原因只进服务端 stdout（由部署层收集）

**存储**

- 密码使用 bcrypt（cost 由配置决定，默认 12），禁止 MD5/SHA1/明文
- JWT 密钥、API Key 在数据库中以哈希形式保存，仅在创建时明文展示一次
- 数据库文件权限设为 `0600`，目录 `0700`
- 备份文件中包含全部凭据，提醒用户妥善保管

**网络与接口**

- 监听非回环地址且 `auth.enabled = false` 时必须拒绝启动
- 写操作要求对应 scope，遵循最小权限；API Key 支持设置过期时间
- 所有外部输入（订阅内容、导入的配置、API 请求体）必须校验大小上限与字段合法性，禁止直接透传给引擎
- 导入的配置不得包含本地文件读取类指令（如把 `cert` 指向任意路径），导入器需白名单化字段

**审计**

- 认证成功/失败、配置变更、版本回滚、用户配额变更、API Key 创建与吊销必须写审计记录
- 审计记录包含：时间、操作者、来源 IP、动作、目标资源、结果；**不包含**任何敏感原文
- 审计日志与运行日志分离存储，审计只增不删

**依赖安全**

```bash
govulncheck ./...
pnpm audit
```

CI 会在每个 PR 上运行这两条命令，高危漏洞直接阻断合并。

---

## 15. 提交规范

**Commit message**

遵循 [Conventional Commits](https://www.conventionalcommits.org/)：

```text
<type>(<scope>): <subject>

<body>

<footer>
```

- `type`：`feat` / `fix` / `perf` / `refactor` / `docs` / `test` / `build` / `ci` / `chore` / `style`
- `scope`：模块名，如 `inbound/vless`、`router`、`api`、`db`、`web`、`dns`
- `subject`：祈使句、小写开头、不超过 72 字符、结尾不加句号
- `body`：说明**为什么**改，而不是改了什么（diff 已经说明了 what）
- `footer`：`Closes #123` / `Fixes #456` / `BREAKING CHANGE: ...`

示例：

```text
perf(relay): use splice for linux tcp forwarding

io.CopyBuffer allocated a 32KB buffer per connection direction.
Switching to splice removes both the buffer and the user-space copy.

Reduces 1000-conn memory by ~38MB in benchmarks.

Closes #210
```

**分支命名**

- `feat/<short-desc>`、`fix/<short-desc>`、`perf/<short-desc>`、`docs/<short-desc>`
- 禁止直接在 `main` 上开发

**PR 流程**

1. 从最新 `main` 创建分支
2. 编写代码 + 测试（新功能必须带测试，bugfix 必须带回归测试）
3. 本地跑通：`make fmt && make lint && make test-race`
4. 提交 PR，描述中必须包含：**改了什么 / 为什么 / 怎么验证的 / 性能影响 / 兼容性影响**
5. 涉及 IR、迁移、协议、安全相关文件的 PR 需要至少一名 maintainer review
6. CI 全绿后由 maintainer squash merge，合并后删除分支

**PR 自查清单**

- [ ] 没有 `TODO`、调试日志、被注释掉的代码残留
- [ ] 没有新增 CGO 依赖，没有未说明的第三方库
- [ ] 热路径改动附带了 benchmark 对比
- [ ] 日志中没有任何敏感字段
- [ ] 文档（README / AGENTS.md / docs/）已同步更新
- [ ] 前端改动已通过 `pnpm typecheck`

---

## 16. 常见陷阱

**1. 不要绕过 IR 直接改文件或内存结构。**
配置类数据的唯一正确路径是：文件 → `Load`（展开 + 读取）→ `FileSet` → `Compile`（合并 + 校验）→ IR → 引擎热重载。绕过 `FileSet` 直接拼 IR，或在引擎里临时改写配置，都会让"配置文件是权威源"这条承诺失效，也让热重载与指纹比对失灵。

**2. 不要在 `engine` / `router` / `relay` 中 import 具体协议包。**
协议只能通过 `registry` 创建。一旦核心引擎依赖具体协议，精简版构建（`build-minimal`）就会失效，且会出现循环依赖。

**3. 不要让 Web 写配置文件。**
Web 的唯一写路径是 `state/overrides.toml` 的运行时白名单字段。任何"顺手帮用户把改动写回 `liproxy.toml`"的实现，都会让 LiProxy 变成配置文件的第二个写入者，用户用 Git 管理配置时必然冲突。导出与导入预览是允许的，落盘必须由用户自己做。

**4. 不要在热路径分配内存。**
`relay` 的转发循环、`router` 的匹配函数、`sniffer` 的解析函数都会被调用百万次。任何 `fmt.Sprintf`、`json.Marshal`、临时切片都会直接反映到内存指标上。写之前先问：这段代码每连接执行几次？

**5. 不要把 `[]byte` 随意转成 `string`。**
`string(b)` 会复制，`map[string(...)]` 查询会产生堆分配。规则匹配请用预先驻留的字符串或 `unsafe.String`（并遵守第 13 节的所有权约束）。

**6. 不要修改已发布的迁移文件。**
迁移一旦合入 `main` 就已可能在用户机器上执行过。需要修正时新增一个迁移，而不是编辑旧的。

**7. 不要在迁移中删除或重命名列。**
SQLite 的 `DROP COLUMN` 在旧版本上行为不一致，且会让旧版本程序崩溃。走"废弃 → 停用 → 新版本删除"三步。

**8. 不要让 IR 字段的零值含义被改变。**
给已有字段补默认值、改类型、改语义，都会让用户的历史版本快照编译出错或行为突变。新增字段永远比修改字段安全。

**9. 不要在 `Compile` 中做 IO。**
`Compile` 必须是纯函数，只接受已读取完毕的 `FileSet`。读文件、展开 include、拉取远程片段都必须发生在 `Load` 阶段。否则"相同输入相同输出"不再成立，热重载会在网络抖动或磁盘权限变化时表现不可预测。

**10. 不要在没有 context 的情况下启动 goroutine。**
每个 goroutine 都要能退出，否则热重载会泄漏 goroutine，长时间运行后内存只增不减。

**11. 不要在日志中打印配置结构体。**
`log.Info().Interface("node", node)` 会把 UUID 和密码写进 stdout，进而被 journal / Docker 日志系统长期留存。永远显式列出要输出的字段。

**12. 不要忽略 `bufpool.Put`。**
缓冲池的收益完全依赖归还。忘记归还比不用池更糟（每次分配新缓冲）。用 `defer` 保证归还。

**13. 不要在 handler 中写业务逻辑。**
handler 只做参数解析、调用 service、错误映射。业务逻辑写在服务层，否则无法被 CLI 与测试复用。

**14. 不要手工构造 HTTP 状态码。**
返回领域错误，让统一中间件映射。手工分支会导致同一个错误在不同接口返回不同状态码。

**15. 不要绕过 `lipd --check` 直接线上试配置。**
改名、改端口、改规则前先 `lipd --check` 校验，再 `lip restart`。少一次事故。

**16. 不要在前端用 Pinia 缓存接口数据。**
TanStack Query 负责缓存与失效。两套缓存并存必然出现"数据不一致"类 bug。Pinia 只放纯客户端的 UI 状态。

**17. 不要在前端引入任何外部 CDN 资源。**
字体、图标、图表底图必须本地打包并随二进制嵌入。运行时从外网拉取资源会导致离线环境白屏，也让"单二进制零外部依赖"这条承诺失效。

**18. 不要在 `/api/` 路径上做 SPA fallback。**
`/api/v1/*` 路由必须注册在静态资源与 fallback 之前，未命中的 API 路径要返回 JSON 错误体而不是 `index.html`。否则前端会把 HTML 当成 JSON 解析，报出难以定位的错。详见第 19 节。

**19. 不要用未解析符号链接的路径做循环检测。**
include 环检测必须 `EvalSymlinks` 之后再比较规范化绝对路径，否则 `a.toml → link/b.toml` 与 `link/b.toml → a.toml` 这类环检测不出来，会无限递归。同理，include 深度上限（32 层）与通配符展开上限（1000 文件）都必须真实生效，超限时报明确错误，不能静默截断。

**20. 不要让单个片段文件解析失败拖垮整份配置。**
`toml.d/` 里某个文件语法写错时，正确行为是：报出**该文件与行号**，保留上一份可用 IR 继续运行，并在 Web 顶部显示降级告警。直接 panic、退出、或静默跳过该文件都是错的 —— 静默跳过会让用户以为配置已经生效。

**21. 不要把 overrides 当成第二个配置源。**
`state/overrides.toml` 只允许承载运行时白名单字段。一旦有人往里面塞节点、规则或 DNS 配置，"配置文件是唯一权威源"就不成立了，用户会陷入"我在 liproxy.toml 里改了但没生效"的困境。白名单是显式 map，加字段必须同时改代码与文档。

---

## 17. AI 协作提示

本项目明确欢迎 AI 工具参与开发。以下约定用于让 AI 的产出**可直接合并**，而不是制造 review 负担。

**适合交给 AI 的任务**

- 新增协议/出站/入站的**样板代码**（在已有协议基础上复制并改写）
- 表驱动测试用例补齐、错误路径测试
- 数据库迁移文件（**仅限运行时状态表**：会话、审计、流量、订阅缓存）
- DTO 定义与 API handler 骨架
- 前端表单、列表、设置页面（有明确的接口契约时）
- 文档、注释、CHANGELOG 更新
- 依赖升级与 lint 修复

**必须人工 review（AI 可以写，但不能自审通过）**

- 任何 `internal/relay`、`internal/router`、`pkg/bufpool` 的改动（内存与并发正确性）
- 协议编解码与加密相关实现（安全性）
- IR 结构变更与导入导出兼容性
- 数据库迁移（一旦发布不可修改）
- 认证、授权、密钥管理相关代码
- 涉及 `unsafe`、`splice`、goroutine 生命周期、TUN 的代码

**AI 代码生成后必须做的事（缺一不可）**

1. **编译**：`go build ./...` 与 `pnpm typecheck` 必须通过
2. **测试**：`make test-race` 必须通过；新功能必须自带上文提到的测试
3. **lint**：`make lint` 必须通过
4. **一致性检查**：确认没有违反第 16 节任一陷阱
5. **契约同步**：改了 API 就更新前端类型；改了 IR 就更新 TOML/YAML 双格式解析与测试；改了配置字段就更新 README 的完整字段表
6. **自我声明**：在 PR 描述中说明哪些部分是 AI 生成的、哪些部分人工验证过

**AI 工具使用禁令**

- 禁止在未阅读目标包 `doc.go` 与相邻实现的情况下生成新文件
- 禁止虚构不存在的依赖、函数或配置字段 —— 不确定时先读代码，不要猜
- **禁止把配置写回数据库**。历史遗留的 `db.Node` / `config_versions` / `config.Service.Commit` 这类写法已经废除，见到就说明你参考了过时的上下文
- 禁止生成占位实现（`// TODO: implement`、返回零值的假实现）并声称功能完成
- 禁止跳过测试直接声称"已修复"
- 禁止为了让测试通过而修改测试的期望值（除非明确说明并给出理由）

**给 AI 的最短路径建议**

如果你要完成一个具体的开发任务，推荐流程：

```text
1. 读 AGENTS.md 第 5、16 节
2. 读目标包的 doc.go 与相邻的一个完整实现（照抄结构）
3. 按第 9 或第 10 节的步骤清单逐条执行
4. 跑 make fmt && make lint && make test-race
5. 在第 15 节的 PR 模板中填写变更说明
```

## 18. License 规范

**LiProxy 采用 [GNU Affero General Public License v3.0](../LICENSE)（AGPL-3.0）。**

AGPL-3.0 是强 copyleft 协议：任何人修改并通过网络提供服务，都必须向使用者提供完整源代码（第 13 条）。这直接约束了我们的开发方式 —— 引入任何新依赖前，必须先确认它的许可证与 AGPL-3.0 兼容。

**依赖许可证准入**

| 许可证类型 | 是否可用 | 说明 |
| --- | --- | --- |
| MIT / Apache-2.0 / BSD-2 / BSD-3 / ISC | ✅ | 宽松协议，与 AGPL-3.0 兼容 |
| MPL-2.0 / LGPL-2.1 / LGPL-3.0 | ⚠️ | 可用，但需在 PR 中说明动态链接方式与合规理由 |
| GPL-2.0 / GPL-3.0 | ✅ | 与 AGPL-3.0 兼容 |
| AGPL-3.0 | ✅ | 同协议 |
| 专有 / 商业 / SSPL / BSL / Commons Clause | ❌ | **禁止引入**，会与 AGPL-3.0 冲突 |
| 许可证缺失（无 LICENSE 文件） | ❌ | 默认保留全部权利，禁止引入 |

新依赖必须在 PR 描述中列出名称、版本、许可证，并附上 `go-licenses` / `pnpm licenses list` 的核对结果。

**每个源代码文件头部必须带 license header。** 没有例外：新文件、生成的代码模板、示例脚本都要带。CI 会校验（见下文）。

### Go 文件 header

```go
// LiProxy - a web-driven universal proxy platform.
// Copyright (C) 2026 LiProxy Contributors
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package foo
```

> 注意：header 必须放在 `//go:build` 约束**之后**、`package` 声明**之前**。文件顶部的 `//go:generate` 指令可以留在 header 前面。

### TypeScript / Vue 文件 header

```ts
/**
 * LiProxy - a web-driven universal proxy platform.
 * Copyright (C) 2026 LiProxy Contributors
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public License
 * along with this program.  If not, see <https://www.gnu.org/licenses/>.
 */
```

`.vue` 文件把这段注释放在文件**第一行**（`<script setup>` 块之外），这样构建产物里也能看到版权声明。

### SQL 文件 header（含迁移）

```sql
-- LiProxy - a web-driven universal proxy platform.
-- Copyright (C) 2026 LiProxy Contributors
--
-- This program is free software: you can redistribute it and/or modify
-- it under the terms of the GNU Affero General Public License as published by
-- the Free Software Foundation, either version 3 of the License, or
-- (at your option) any later version.
--
-- This program is distributed in the hope that it will be useful,
-- but WITHOUT ANY WARRANTY; without even the implied warranty of
-- MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
-- GNU Affero General Public License for more details.
--
-- You should have received a copy of the GNU Affero General Public License
-- along with this program.  If not, see <https://www.gnu.org/licenses/>.
```

> 迁移文件的 header 属于**已发布内容的一部分**：一旦迁移合入 `main`，header 就不可再修改（见第 11 节与第 16 节陷阱 6）。

### Shell 脚本 header

```sh
#!/usr/bin/env bash
# LiProxy - a web-driven universal proxy platform.
# Copyright (C) 2026 LiProxy Contributors
#
# This program is free software: you can redistribute it and/or modify
# it under the terms of the GNU Affero General Public License as published by
# the Free Software Foundation, either version 3 of the License, or
# (at your option) any later version.
#
# This program is distributed in the hope that it will be useful,
# but WITHOUT ANY WARRANTY; without even the implied warranty of
# MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
# GNU Affero General Public License for more details.
#
# You should have received a copy of the GNU Affero General Public License
# along with this program.  If not, see <https://www.gnu.org/licenses/>.
set -euo pipefail
```

### 其他文件类型

| 类型 | header 形式 |
| --- | --- |
| YAML / TOML | `#` 注释，同 Shell 格式 |
| Makefile | `#` 注释，紧随 `.DEFAULT_GOAL` 之前 |
| Markdown 文档 | 不需要 header；许可证信息集中在 README 的 License 章节 |
| 生成物（`web/dist/`、`bin/`） | 不需要 header |

### CI 检查

CI 在 lint 阶段运行 header 校验，缺失即阻断合并：

```bash
# Go 文件（在 golangci-lint 之外单独校验，因为它按行匹配而不是按 AST）
find . -name '*.go' -not -path './web/dist/*' -not -path './vendor/*' \
  | xargs grep -L 'GNU Affero General Public License as published by' \
  | tee /dev/stderr | (! read)

# 前端与脚本
pnpm exec license-header-check --template scripts/license-header.txt 'web/src/**/*.{ts,vue}'
```

**检查清单（每个 PR）：**

- [ ] 新增的每个源文件都带 AGPL-3.0 header
- [ ] 没有引入专有 / SSPL / BSL / 无许可证的依赖
- [ ] 新增依赖已在 PR 描述中列出许可证并说明合规性
- [ ] `LICENSE` 文件未被修改（AGPL-3.0 全文必须逐字保留）

---

## 19. 单二进制 embed 规范

**产品承诺：用户拿到 `lipd` 一个文件就能跑，不需要额外部署前端静态文件。** 这一节是所有前端与 HTTP 路由相关改动必须遵守的契约。

### 19.1 embed 的唯一归属

`//go:embed` 只能出现在**一个**包里：`web/embed.go`。

```go
// web/embed.go（示意）
package web

//go:embed all:dist
var webFS embed.FS
```

规则：

1. 全仓库**只有 `web/embed.go` 允许出现 `//go:embed`**，其它包一律通过 `web.FS()` 或 `web.Handler()` 获取，禁止各自 embed
2. 必须用 `all:` 前缀 —— 否则 `_` 与 `.` 开头的文件（Vite 产物里常见）会被静默忽略
3. `web` 包对外只暴露三个符号：`FS() fs.FS`、`Handler() http.Handler`、`Available() bool`；内部结构体（如 `distFS`）不导出
4. `web/dist/` 是**生成物**，写进 `.gitignore`，不入库；仓库里只保留 `web/dist/.gitkeep`
5. **`web/dist/.gitkeep` 必须存在**：`go:embed all:dist` 在目录不存在时是编译期错误，会让 `go build ./...`、`go vet`、CI 的纯后端任务全部失败。占位目录保证"只改后端的人也能编译"
6. **禁止**在 `CMD`/`Dockerfile`/`Makefile` 里把 dist 复制到二进制旁边作为 fallback —— 单文件承诺不允许有"运行时找文件"的分支

> 开发环境（`LIPROXY_DEV=1` 或 `make dev`）下不经过 embed：Vite dev server 提供前端（5173），Go 只提供 API（9090），由 Vite 的 `server.proxy` 转发 `/api`。两条路互不干扰，生产构建永远走 embed。

### 19.2 路由注册顺序（硬约束）

chi 的中间件与路由按注册顺序生效，顺序错了会出现"API 返回 HTML""WebSocket 被 fallback 吃掉"这类难查的 bug。

```text
1. 全局中间件         → Recoverer · RequestID · 日志 · CORS
2. /api/v1/ws         → WebSocket 升级（最先，不被任何 body 改写中间件包裹）
3. /api/v1/*          → REST API（含认证、限流、统一错误映射）
4. /healthz /metrics  → 探针，不需要认证
5. /*                 → 静态资源 + SPA fallback（最后，兜底一切）
```

**必须在代码里有测试固化这个顺序**（见 19.5）。

### 19.3 SPA fallback 规则

静态资源处理器按以下顺序决策：

| 请求 | 行为 |
| --- | --- |
| 命中 `dist/` 下的**文件** | `http.ServeContent` 原样返回（保留 ETag / Last-Modified / 304） |
| 命中 `dist/` 下的**目录** | 找该目录的 `index.html`；没有再回落根 `index.html` |
| 未命中，路径以 `/api/` 开头 | **返回 JSON 404**（`{"error":{"code":"not_found"}}`），**绝不返回 HTML** |
| 未命中，其他路径 | 返回根 `index.html` + 200，交给 Vue Router（history 模式）接管 |
| 方法非 GET / HEAD | 返回 405，不做 fallback |
| 路径包含 `..` 或未清理的转义 | 返回 400，防止目录穿越 |

**缓存策略**（写死，不做配置）：

| 路径 | Cache-Control | 理由 |
| --- | --- | --- |
| `/assets/*` | `public, max-age=31536000, immutable` | Vite 文件名带内容 hash，内容变了文件名就变 |
| `/index.html` | `no-cache` | 否则用户永远拿不到新版本 |
| `/sw.js`、`/manifest.webmanifest` | `no-cache` | Service Worker 更新必须立即可见 |
| 其它根级文件（favicon 等） | `public, max-age=3600` | 折中值 |

**预压缩**：构建期同时产出 `.gz`（必要时 `.br`），命中 `Accept-Encoding` 时直接吐预压缩文件 —— 运行时 gzip 会同时吃掉 CPU 和内存预算，与内存目标冲突。

**安全**：静态资源处理器必须在 `path.Clean` 之后做前缀校验，且**只读** embed FS，不允许任何写路径。API 404 的错误体由统一错误中间件生成，不在 fallback 里手写状态码（见第 16 节陷阱 14）。

### 19.4 Makefile 契约

`make build` 必须串起前端构建与 Go 构建，顺序不可颠倒：

```make
build: build-frontend
	go build -trimpath -ldflags "..." -o bin/lipd ./cmd/lipd
	go build -trimpath -ldflags "..." -o bin/lip  ./cmd/lip

build-frontend:
	cd web && pnpm install --frozen-lockfile && pnpm build
```

- `build` 依赖 `build-frontend`，不能并行（Go 侧要读到产出的 dist）
- `build-frontend` 独立可调用，方便只改前端时复用
- `clean` 必须清理 `bin/` 与 `web/dist/`（但保留 `web/dist/.gitkeep`）
- `make build` 完成后**必须**打印 `bin/lipd` 的体积，让体积回归立刻可见

### 19.5 体积红线与验收

| 指标 | 红线 | 检查方式 |
| --- | --- | --- |
| `web/dist/` gzip 后总量 | **< 2 MB** | `tar czf - -C web/dist . \| wc -c` |
| 全协议 `bin/lipd` | ≈ 25 MB | `ls -lh bin/lipd` |
| 精简版 `bin/lipd` | ≈ 8 MB | `make build-minimal && ls -lh bin/lipd` |
| 空闲内存 | < 15 MB | `./bin/lipd` 后读 RSS |

超过红线的 PR 必须在描述中给出理由与瘦身计划。

**每次改动 embed / 路由 / 前端构建配置后，必须完成"干净目录冒烟测试"：**

```bash
make build
rm -rf /tmp/lip-smoke && mkdir -p /tmp/lip-smoke
cp bin/lipd /tmp/lip-smoke/ && cd /tmp/lip-smoke && ./lipd
# 浏览器打开 http://127.0.0.1:9090，必须看到 Vue Web 界面（不是目录列表、不是 404）
```

外加三条自动化断言（必须有测试固化，不能只靠人工点）：

1. `GET /` 返回 200 且 `Content-Type: text/html`，body 包含 `<div id="app">`
2. `GET /api/v1/nonexistent` 返回 404 且 `Content-Type: application/json`，**body 不是 HTML**
3. `GET /nodes`（任意前端深链）返回 200 且是 `index.html`，而 `GET /assets/<真实文件名>` 返回 200 且带 `immutable` 缓存头

---

## 20. 路线图（Roadmap）

Phase 只定义**边界与出口标准**，不规定实现细节。每个 Phase 内部再拆成最小可交付单元，串行推进，每单元一次 review。

> 本次架构调整把"配置内核"提前了：先有可用的配置加载与 IR，再有 Web 与引擎。顺序反过来会导致大量返工。

### Phase 1 — 配置内核（Config Core）★ 当前阶段

**目标**：`lip config show` 能把 `~/.li/liproxy/` 下的多来源配置合并成一份 IR 并打印出来，不含任何引擎与网络能力。

| 出口标准 | 验证方式 |
| --- | --- |
| 5 层优先级正确，`lip config sources` 能指出每个字段的来源文件与行号 | 优先级表驱动测试 |
| 主配置 TOML/YAML 二选一，同时存在时忽略 YAML 并 warn | 单测 |
| include 支持单文件 / 通配符 / `exclude` / `optional` / 跨格式 | 表驱动测试 |
| 符号链接环与普通环均被检出，错误含完整引用链 | 环检测测试（含 `EvalSymlinks` 用例） |
| 深度 32 / 通配符 1000 上限真实生效 | 边界测试 |
| 数组追加、map 深合并、同名 outbound 报错并含两组行号 | 合并测试 |
| 同一配置的 TOML 与 YAML 表达编译出**深度相等**的 IR | 双格式同构测试 |
| 校验失败保留上一份可用 IR | 单测 |

**不含**：Web 界面、数据库、任何协议、引擎、转发。

### Phase 2 — 骨架与可观测（Skeleton & Observability）

**目标**：`bin/lipd` 拷到干净目录能独立启动，浏览器能看到 Vue 界面并看到当前 IR。

| 出口标准 | 验证方式 |
| --- | --- |
| 单二进制可独立运行 | 19.5 的干净目录冒烟测试 |
| 配置加载失败时服务仍可用，Web 顶部显示降级告警 | 端到端冒烟 |
| 登录 → 只读配置视图可访问 | 端到端冒烟 |
| `/api/v1/*` 返回 JSON，前端深链返回 index.html | 19.5 的三条断言 |
| 数据库可关闭：`database.enabled = false` 时不创建库也能启动 | 启动测试 |

**不含**：任何真实代理协议、规则匹配、Provider 拉取、DNS。

### Phase 3 — 引擎与首个协议（Engine + First Protocol）

**目标**：IR 能驱动真实转发，热重载不泄漏。

| 出口标准 | 验证方式 |
| --- | --- |
| registry 自注册生效，核心引擎零协议 import | `go list -deps` 断言 |
| 至少一个协议完成回环测试（1 MB 随机数据） | 回环测试 |
| 改配置文件后热重载生效，旧连接优雅关闭，goroutine 不泄漏 | 连续热重载 + goroutine 计数断言 |
| 指纹未变时不触发重建 | 单测 |
| relay 转发有 benchmark 基线 | `make bench` 报告入 PR |

**风险**：这一 Phase 触及 `relay` / `router` / `bufpool`，属于"必须人工 review"范围。

### Phase 4 — 协议与传输铺开（Protocol Breadth）

**目标**：协议覆盖面对齐 Mihomo 客户端能力。

按第 9 节的九步流程逐个协议推进，每个协议一个 PR。**每个协议的体积增量都要单独测量并记录。**

**不含**：服务端多用户、订阅分发（Phase 5）。

### Phase 5 — 服务端与分发（Server Mode）

**目标**：多用户、流量配额、ACME、订阅生成，对齐 sing-box 服务端能力。

| 出口标准 | 验证方式 |
| --- | --- |
| 多用户独立凭据与配额，超额自动停用 | 配额边界测试 |
| 流量计量异步批量写回，不影响转发路径 | 转发路径 benchmark 无分配回归 |
| 订阅生成三种格式（clash / v2ray / sing-box） | 格式快照测试 |
| ACME 自动申请与续期 | 使用测试目录（禁止真实外网） |
| 数据库不可用时服务端模式明确拒绝启动（而非静默降级） | 启动测试 |

### 贯穿所有 Phase 的常驻要求

- 每个 Phase 结束时更新 README 与 AGENTS.md，文档与代码不允许漂移
- 内存指标在 Phase 3 之后每个 PR 都要有数据，不允许"先涨上去以后再优化"
- 任何新增依赖都走第 18 节的许可证准入
- 任何"让 Web 写配置文件"的提案都需要先修订本文件，而不是直接实现

---

## 21. 配置加载规范

这是本项目最核心的一节。配置是权威源，所以加载顺序、合并语义、错误处理都必须精确到可测试。

### 21.1 文件布局与加载优先级

```text
优先级从低到高（后者覆盖前者）

1. 内置默认值（编译进二进制，保证空配置也能启动）
2. 主配置：liproxy.toml
3. toml.d/*.toml   按文件名字典序（byte-wise，等价于 sort）
4. yaml.d/*.yaml   按文件名字典序
5. state/overrides.toml   仅运行时白名单字段（见 21.6）
```

两条硬规则：

- **主配置只有 TOML 一种**：`liproxy.toml` 是唯一的主配置入口。YAML 只用于 `yaml.d/` 片段 —— 这样"主配置在哪里"永远只有一个答案，不需要处理"两个主配置并存时听谁的"。
- **字典序是 byte-wise 排序**，不是本地化排序。这样 `10-a.toml` 排在 `9-a.toml` 前面，行为在所有机器上一致。想控制顺序就**用零填充前缀**（`001-`、`002-`），并在文档里明确写出这个约定。

### 21.2 include

主配置与所有片段都可以含 `include`。include 在**解析阶段递归展开**，深度优先。

```toml
include = [
  "extra/nodes.toml",              # 单文件，相对当前文件所在目录
  "rules/*.toml",                  # 单层通配符
  "providers/**/*.yaml",           # 递归通配符
  "conf/{dns,log}.toml",           # 花括号展开
  "local.toml?",                   # ? = 单个字符
]

# 排除（在展开结果上执行）
exclude = ["providers/**/disabled-*.yaml"]

# 结构化 include：需要跨格式或可选时用这种写法
[[include]]
path     = "extra/rules.yaml"
format   = "yaml"                  # 显式指定格式，覆盖扩展名推断
optional = true                    # 不存在则跳过，不报错
```

**通配符支持**：`*`（单层）、`**`（递归）、`?`（单字符）、`{a,b}`（枚举）。`**` 标准库不支持，需自己实现 —— 实现方式是把 pattern 按 `**` 切段，逐段用 `filepath.Glob` 展开。**不要**为此引入第三方 glob 库（见第 4 节依赖准入）。

**格式判定顺序**：
1. 显式 `format = "yaml" | "toml"`
2. 扩展名 `.toml` / `.yaml` / `.yml`
3. 都判定不出来 → 报错，不猜

**include 只支持本地路径。** 远程 URL 不在范围内：既没有实现计划，也不在文档里承诺。配置必须能在没有网络的环境下完整加载。

### 21.3 三道防线（必须有测试）

| 防线 | 上限 | 超限行为 |
| --- | --- | --- |
| 循环引用 | 不允许 | 报错并打印**完整引用链**（`a.toml → b.toml → a.toml`） |
| include 深度 | 32 层 | 报错并指出达到上限的那条链 |
| 通配符展开文件数 | 1000 个 | 报错并建议收窄 pattern |

**循环检测实现要点**：键必须是 `EvalSymlinks` 之后的规范化绝对路径。直接用 `filepath.Clean` 不够 —— `a.toml → link/b.toml → a.toml` 这种经符号链接的环会被漏掉，表现为无限递归直到栈溢出。同时维护一个"当前递归栈"（用于报环）与一个"已访问集合"（用于去重），两者语义不同，不要合并。

### 21.4 合并策略

合并**只在 IR 层执行一次**，TOML 与 YAML 不走各自的合并逻辑。

| 类型 | 策略 |
| --- | --- |
| Map / 结构体 | 深度合并；同名标量后者覆盖前者 |
| List / 数组 | **默认追加**（不是替换！） |
| 标量 | 后者覆盖前者 |
| `rules` | 追加，但可用 `position = "prepend" \| "append"` 控制插入端 |
| 同名 outbound / provider / inbound（按 tag 或 name） | **报错**，并指出两个来源文件的路径与行号 |

**为什么数组默认追加**：配置文件天然是"多来源拼装"的，`toml.d/` 里每个文件通常只贡献几条规则或节点。默认替换会让后加载的文件把前面的整个列表抹掉，这是最容易踩的坑。需要替换语义时用显式标记，不要靠默认行为。

**同名冲突为什么报错而不是覆盖**：节点 tag 是规则引用出站的唯一凭据。静默覆盖会让"规则指向了另一个节点"这种 bug 极难排查。报错时必须给出**两份来源**（文件 + 行号），而不是只报一个。

### 21.5 校验与错误信息

校验规则只有一份，TOML 与 YAML 共用。校验在合并**之后**执行，因为只有合并后才知道最终值。

错误信息的最低要求（这是用户唯一能看到的定位手段）：

```text
config: invalid value for "inbounds[0].listen": port 0 is not allowed
  --> /home/u/.li/liproxy/toml.d/010-inbound.toml:12
  --> also set in /home/u/.li/liproxy/liproxy.toml:8 (overridden)
```

- 必须给出**文件路径 + 行号**，行号在解析阶段就要记录下来（provenance）
- 被覆盖的值要一并列出，否则用户不知道自己在改哪个文件
- 校验失败**不清空配置**：保留上一份可用 IR 继续运行（见第 12 节）

### 21.6 overrides 白名单

`state/overrides.toml` 由 Web 写入，只允许**运行时字段**：

| 允许 | 不允许 |
| --- | --- |
| `log.level` | 任何 `inbounds` / `outbounds` / `nodes` |
| `runtime.active_node`（当前选中的出站 tag） | 任何 `rules` / `rule_sets` |
| `runtime.temp_rules`（内存态临时规则） | 任何 `dns` / `providers` / `tls` 配置 |
| `runtime.mode`（rule / global / direct） | 任何监听地址与端口 |

实现要求：

- 白名单是代码里的**显式 map**，不是"反射后排除业务字段"
- 写入流程：校验白名单 → 备份到 `backups/` → 临时文件 + `rename` 原子替换 → 重新编译 → 失败则回滚
- 文件头必须带注释说明"此文件由 Web 生成，业务配置请改主配置或 *.d/"，避免用户误以为这是主配置

### 21.7 热重载

| 触发方式 | 用途 |
| --- | --- |
| 文件监听（fsnotify 语义，自实现或标准库 + 轮询） | 编辑配置后自动生效 |
| `SIGHUP` | 容器内、或监听不可靠时的手动触发 |
| `lip reload` | 通过本地 API 触发 |
| Web 写 overrides 后 | 同上，走同一套流程 |

流程与约束：

1. 重新执行 `Load` → 计算**文件指纹**（排序后的 `路径 + sha256`）
2. 指纹未变 → 直接返回，不做任何重建（避免无意义的热重载抖动）
3. 指纹变了 → `Compile`；失败则**保留旧 IR**，记 error 日志，Web 顶部挂降级告警
4. 成功 → 原子替换 IR → 优雅关闭旧实例（先停止接受新连接，再等存量连接，带超时上限）
5. 热重载期间**不接受新的热重载请求**（串行化），避免并发重建导致 goroutine 泄漏

### 21.8 测试要求

配置加载是本项目测试最密集的地方，必须覆盖：

- **优先级**：同样字段在 5 个层级各写一次，断言胜出者与 `lip config sources` 输出一致
- **主配置唯一**：`liproxy.yaml` 不作为主配置被读取（它是普通片段目录里的格式，不是主配置入口）
- **字典序**：`10-` 排在 `9-` 之前（固化 byte-wise 语义）
- **include 环**：`a → b → a` 必须报错且错误信息含完整链
- **符号链接环**：`a → link/b → a` 同样必须被检出（这是最容易漏的用例）
- **深度上限**：33 层报错，32 层通过
- **通配符上限**：1001 个文件报错
- **合并语义**：数组追加、map 深合并、同名 outbound 报错并含两个来源行号
- **双格式同构**：同一份配置分别用 TOML 与 YAML 表达，编译出的 IR 必须**深度相等**
- **往返**：`load → IR → export → load` 结果一致
- **overrides 白名单**：写业务字段必须被拒绝
- **失败保留旧 IR**：坏配置文件不会导致服务不可用
- **纯函数性**：相同 `FileSet` 编译两次，结果深度相等

所有涉及文件的用例一律使用 `t.TempDir()`，禁止读取真实 `~/.li/`。

---
