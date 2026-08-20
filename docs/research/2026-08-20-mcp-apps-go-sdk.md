# MCP Apps 与官方 Go SDK 支持情况调研

- 调研日期：2026-08-20
- 调研对象：MCP `2026-07-28`、MCP Apps、`github.com/modelcontextprotocol/go-sdk`
- 项目：`mcp-server-mysql`
- 结论状态：可进入原型设计

## 1. 结论摘要

官方 Go SDK **已经具备实现 MCP Apps 服务端所需的底层能力**，本项目可以继续使用 Go 提供 MCP server，不需要把 MySQL 服务端改写为 TypeScript。

但需要区分两种“支持”：

1. **协议与底层构件支持：已支持。** Go SDK 能声明 extension capability，能给 Tool/Resource/Result 写 `_meta`，能注册 `ui://` Resource，也能返回 `structuredContent`。
2. **MCP Apps 专用 Go SDK 支持：尚不完整。** 官方 Go SDK 没有类似 `@modelcontextprotocol/ext-apps/server` 的 Apps 专用包、类型、常量和 helper；View 端官方 SDK 目前仍是 JavaScript/TypeScript 包。

本项目当前锁定 `go-sdk v1.6.1`：

- 足以做 MCP Apps 服务端原型；
- 不支持 MCP 核心协议 `2026-07-28`；
- 若希望同时对齐新核心协议，应升级到 `v1.7.0`。

因此最终判断是：**可实现，但属于“用通用 Go MCP API 手工组装 MCP Apps 协议字段”，不是“Go SDK 已提供一等 Apps 开发体验”。**

## 2. 版本关系澄清

MCP Apps 并不是在 `2026-07-28` 才首次推出。

- MCP Apps 于 **2026-01-26** 作为第一个官方 MCP extension 发布并宣告可用于生产：[MCP Apps 发布公告](https://blog.modelcontextprotocol.io/posts/2026-01-26-mcp-apps/)。
- MCP `2026-07-28` 的相关变化是把 extension framework 正式纳入新版核心协议，并在无初始化握手的模式下规定 extension capability 的发现与逐请求携带方式：[2026-07-28 规范发布公告](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/main/blog/content/posts/2026-07-28-spec-ga/index.md)。
- MCP Apps 作为 extension 独立演进，当前稳定规范由 `ext-apps` 仓库维护：[MCP Apps 2026-01-26 规范](https://github.com/modelcontextprotocol/ext-apps/blob/main/specification/2026-01-26/apps.mdx)。

这意味着：实现 MCP Apps 不必以 `2026-07-28` 为绝对前提；但若项目明确要支持新版核心协议，就必须使用支持该协议的 Go SDK 版本。

## 3. 官方 Go SDK 的支持边界

| 能力层 | 当前状态 | 判断依据 |
|---|---|---|
| MCP 核心协议 `2026-07-28` | `go-sdk v1.7.0+` 支持 | [Go SDK 兼容性表](https://github.com/modelcontextprotocol/go-sdk#version-compatibility)、[v1.7.0 release](https://github.com/modelcontextprotocol/go-sdk/releases/tag/v1.7.0) |
| Extension capability 协商 | 支持 | `ClientCapabilities`、`ServerCapabilities` 有 `Extensions` 与 `AddExtension`；见 [PR #794](https://github.com/modelcontextprotocol/go-sdk/pull/794) |
| Tool `_meta.ui.resourceUri` | 可表达 | `mcp.Tool` 嵌入通用 `mcp.Meta`；没有 Apps 专用强类型 |
| `ui://` Resource | 可注册、可读取 | `Server.AddResource`、`mcp.Resource`、`mcp.ResourceContents` 支持绝对 URI、MIME、HTML 文本与 `_meta`；见 [Go SDK server docs](https://github.com/modelcontextprotocol/go-sdk/blob/main/docs/server.md#resources) |
| Tool 结构化结果 | 支持 | `mcp.CallToolResult.StructuredContent` 可承载查询列、行和截断状态；见 [Go package docs](https://pkg.go.dev/github.com/modelcontextprotocol/go-sdk/mcp#CallToolResult) |
| Apps capability 的逐请求读取 | `v1.7.0` 支持得更完整 | 新协议把 client capability 放在每个请求的 `_meta`；`ServerRequest.ClientCapabilities()` 可读取 |
| Apps 专用 Go 类型、常量与 helper | 未提供 | Go SDK 当前公开包和源码中没有 `ext-apps`/Apps package，也没有 `getUiCapability` 等对应封装 |
| 官方 Go Apps 完整示例 | 尚未合入 | [Go Apps example PR #847](https://github.com/modelcontextprotocol/go-sdk/pull/847) 截至调研日仍为 open |
| View/Host 端 Apps SDK | 官方实现为 JS/TS | [ext-apps 仓库](https://github.com/modelcontextprotocol/ext-apps) 发布 `@modelcontextprotocol/ext-apps`、React 与 App Bridge 子包 |

### 3.1 Go SDK 已经具备的关键 API

项目当前使用的 `v1.6.1` 源码中已经存在：

- `ClientCapabilities.Extensions` / `ClientCapabilities.AddExtension`
- `ServerCapabilities.Extensions` / `ServerCapabilities.AddExtension`
- `mcp.Tool.Meta`
- `mcp.Resource.Meta`、`MIMEType`、`URI`
- `mcp.ResourceContents.Meta`、`MIMEType`、`Text`
- `mcp.Server.AddResource`
- `mcp.CallToolResult.Meta`
- `mcp.CallToolResult.StructuredContent`

这些正好覆盖 MCP Apps 服务端必须表达的内容：

1. 声明 `io.modelcontextprotocol/ui` extension；
2. 通过 `_meta.ui.resourceUri` 把 Tool 关联到 UI；
3. 通过 `ui://` Resource 返回 `text/html;profile=mcp-app`；
4. 通过 `structuredContent` 把查询结果交给 View；
5. 同时保留普通 `content`，供不支持 Apps 的 Host 使用。

### 3.2 Go SDK 尚未提供的内容

官方 Go SDK 没有提供如下 Apps 专用抽象：

- `McpUiToolMeta`、`UIResourceMeta`、CSP、permissions、visibility 等强类型 Go struct；
- `RESOURCE_MIME_TYPE`、extension identifier 等常量；
- `registerAppTool`、`registerAppResource`、`getUiCapability` 等 helper；
- View 与 Host 之间 `ui/*` 消息的 Go 端 App/View SDK；
- 已合入并持续测试的完整 Go MCP Apps 示例。

Go SDK 维护者已经把 extension capability 字段视为可以启用 MCP Apps 的基础；原始需求也因此关闭，但 Apps 示例 PR 仍未合入：[Issue #815](https://github.com/modelcontextprotocol/go-sdk/issues/815)、[PR #847](https://github.com/modelcontextprotocol/go-sdk/pull/847)。

## 4. MCP Apps 服务端需要做什么

稳定规范定义的核心路径是：

1. Tool 在 `_meta.ui.resourceUri` 中指向一个 `ui://` Resource；
2. Host 通过 `resources/read` 获取 HTML；
3. Host 在 sandboxed iframe 中渲染 View；
4. Host 把 tool input/result 通过 JSON-RPC over `postMessage` 交给 View；
5. View 可以经 Host 再调用同一个 MCP server 的 Tool。

参考：[MCP Apps 概览](https://modelcontextprotocol.io/extensions/apps/overview)、[MCP Apps 规范](https://github.com/modelcontextprotocol/ext-apps/blob/main/specification/2026-01-26/apps.mdx)。

服务端最低要求如下：

- extension identifier：`io.modelcontextprotocol/ui`
- UI URI scheme：`ui://`
- HTML MIME：`text/html;profile=mcp-app`
- Tool metadata：`_meta.ui.resourceUri`
- 查询数据：优先放在 `structuredContent`
- 降级结果：必须继续提供有意义的普通 `content`

View 端不要求使用某个框架；官方文档明确说明 `App` class 是便利封装而非协议要求。但在本项目中使用 `@modelcontextprotocol/ext-apps` 会显著降低手写 `postMessage` JSON-RPC、生命周期和 Host capability 处理的风险：[Framework support](https://modelcontextprotocol.io/extensions/apps/overview#framework-support)。

## 5. 对当前 mysql-mcp 项目的影响

### 5.1 当前状态

- [go.mod](../../go.mod) 锁定 `github.com/modelcontextprotocol/go-sdk v1.6.1`。
- [internal/server/server.go](../../internal/server/server.go) 的 `Build()` 创建 Server 并注册 `mysql_query` 等 Tool。
- 查询结果由 `Executor.Query()` 产生 `Columns`、`Rows`、`Truncated`，随后经 `formatResult()` 变成纯文本。
- 当前没有 Resource 注册、`_meta.ui` 或 `StructuredContent`。

现有查询结果模型已经适合直接转成 UI 数据，不需要重写 executor。

### 5.2 建议的最小实现形态

第一版只给 `mysql_query` 增加 UI：

1. 在 Server capability 中声明 `io.modelcontextprotocol/ui`。
2. 注册固定 Resource，例如 `ui://mcp-server-mysql/query-results`。
3. Resource 返回内联构建后的 HTML/JS/CSS，MIME 使用 `text/html;profile=mcp-app`。
4. 在 `mysql_query` Tool 的 `_meta.ui.resourceUri` 中引用该 Resource。
5. 查询成功时返回：
   - 原有 `TextContent`，作为模型与非 Apps Host 的 fallback；
   - `StructuredContent`，形如 `{columns, rows, truncated}`，供 View 渲染表格。
6. View 使用官方 `@modelcontextprotocol/ext-apps` 接收初始 tool result，并允许排序、筛选、分页或重新查询。

最小后端改动预计集中在：

- [internal/server/server.go](../../internal/server/server.go)
- 一个新的 UI Resource/静态资源模块
- [internal/server/server_test.go](../../internal/server/server_test.go)

`internal/executor` 的查询结果结构可直接复用。

## 6. “实时查看查询数据”的实现含义

MCP Apps 能维持一个交互式 View，View 也能通过 Host 调用 server Tool，因此可以实现：

- 初次查询后立即显示表格；
- 用户点击刷新；
- View 定时调用只读查询或专用 refresh Tool；
- 根据 UI 过滤条件重新发起参数化查询；
- Host 支持时保持 View 状态并持续更新。

但官方 Go SDK **没有一个 Apps 专用的“实时数据推送 API”**。对于本项目，第一版应把“实时”定义为受控轮询或用户触发刷新。若后续要求数据库变更即主动推送，需要另行设计：

- 资源变更订阅或新版 `subscriptions/listen`；
- 数据库 CDC/binlog 或服务端事件源；
- Host 对持续 View 与相关通知的实际支持；
- 查询频率、最大行数、超时、审计和并发限制。

因此，MCP Apps 解决的是“在 Host 中承载交互 UI 并安全地把 Tool 数据交给 UI”，并不会自动提供数据库实时订阅能力。

## 7. 版本建议

### 原型阶段

可以直接基于当前 `v1.6.1` 验证：

- Tool metadata；
- `ui://` Resource；
- HTML View；
- `StructuredContent`；
- 文本降级。

这能快速证明 UI 是否能在目标 Host 中渲染和交互。

### 正式实现

建议先把 Go SDK 升级到 `v1.7.0`：

- 它是当前官方标记支持 MCP `2026-07-28` 的版本；
- 能处理新版无 session 初始化、`server/discover`、逐请求 client capabilities 和新版订阅语义；
- 保留对 `2025-11-25` 及更早版本的兼容。

如果继续使用 stdio，升级仍能获得新协议兼容；如果未来改为 Streamable HTTP 并希望协商 `2026-07-28`，官方 release note 要求 server 使用 stateless 模式。

## 8. 风险与验证点

1. **Host 才是最终开关。** 不支持 MCP Apps 的客户端只会走文本 fallback；Host 支持列表会变化，应在选定目标客户端上实测：[当前客户端支持说明](https://modelcontextprotocol.io/extensions/apps/overview#client-support)。
2. **Capability negotiation 需要兼容新旧协议。** `2026-07-28` 每个请求携带 client capabilities，旧协议则在初始化握手中协商。
3. **必须保留文本结果。** Apps 规范把 UI 视为 progressive enhancement，而不是唯一结果格式。
4. **HTML 受 CSP 与 iframe sandbox 约束。** 推荐把前端 bundle 内联进 Resource，尽量不依赖外部域名。
5. **UI 触发的 Tool 仍必须走现有 guard、row cap、timeout 和 audit。** 不能因调用来源是 View 而绕过安全边界。
6. **大型结果集不能无限放入 `structuredContent`。** 应沿用当前行数上限，并在 UI 中明确展示 `truncated`。

## 9. 最终判断

| 问题 | 回答 |
|---|---|
| 官方 Go SDK 是否支持 MCP `2026-07-28`？ | 支持，从 `v1.7.0` 开始。 |
| 本项目当前 `v1.6.1` 是否支持该核心版本？ | 不支持，最高为 `2025-11-25`。 |
| 当前 `v1.6.1` 能否实现 MCP Apps 服务端？ | 能，所需通用字段与 Resource/Result API 已存在。 |
| 官方 Go SDK 是否有一等 MCP Apps 包？ | 没有；缺少 Apps 专用强类型、helper 和已合入示例。 |
| mysql-mcp 是否值得继续推进？ | 值得；现有查询结果结构非常适合 `StructuredContent + interactive table`。 |
| 推荐路线 | Go 后端继续保留，升级到 `v1.7.0`，View 使用官方 JS/TS `ext-apps` SDK。 |

## 10. 一手来源

- [MCP 2026-07-28 规范发布公告](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/main/blog/content/posts/2026-07-28-spec-ga/index.md)
- [MCP Extensions Overview](https://modelcontextprotocol.io/extensions/overview)
- [MCP Apps 官方概览](https://modelcontextprotocol.io/extensions/apps/overview)
- [MCP Apps 2026-01-26 发布公告](https://blog.modelcontextprotocol.io/posts/2026-01-26-mcp-apps/)
- [MCP Apps 稳定规范](https://github.com/modelcontextprotocol/ext-apps/blob/main/specification/2026-01-26/apps.mdx)
- [MCP Apps 官方 SDK/示例仓库](https://github.com/modelcontextprotocol/ext-apps)
- [官方 Go SDK](https://github.com/modelcontextprotocol/go-sdk)
- [Go SDK v1.7.0 release](https://github.com/modelcontextprotocol/go-sdk/releases/tag/v1.7.0)
- [Go SDK extension capability PR #794](https://github.com/modelcontextprotocol/go-sdk/pull/794)
- [Go SDK MCP Apps issue #815](https://github.com/modelcontextprotocol/go-sdk/issues/815)
- [Go SDK MCP Apps example PR #847](https://github.com/modelcontextprotocol/go-sdk/pull/847)
