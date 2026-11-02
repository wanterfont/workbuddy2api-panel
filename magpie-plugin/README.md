# opencode-workbuddy2apipanel

[magpie](https://usemagpie.ai) 的供应商插件：把自建的 **WorkBuddy2API** 网关接进来。
填自己的网关地址 + api_key，既能用模型，也能在用量页看到账号池余额。

- provider id：`workbuddy2apipanel`（agent 里写 `workbuddy2apipanel/cn:fast-model`）
- 对话接口：OpenAI 兼容（`/v1/chat/completions`），模型表从网关 `/v1/models` 动态拉取
- 余额来源：网关的 `GET /v1/balance`（需网关为带 `internal/magpie` 补丁的构建；没有该端点时自动降级为「不显示余额」，**不影响对话**）

## 安装

插件跟着网关仓库走，直接从本仓库装：

```bash
git clone https://github.com/wanterfont/workbuddy2api-panel
magpie plugin add ./workbuddy2api-panel/magpie-plugin
magpie plugin options workbuddy2apipanel '{"apiBase":"https://你的网关/v1"}'
magpie plugin login workbuddy2apipanel     # 粘贴你自己网关的 api_key
magpie plugin                                # 应显示 workbuddy2apipanel  signed in
```

> **`apiBase` 必须填**。插件不内置默认网关，不填时请求会被拒绝并提示设置命令——
> 这样所有人的流量都不会打到别人的网关。
> 注册的是**路径**，所以 clone 下来的目录别删；换位置要 `magpie plugin rm` 后重新 `add`。

`apiBase` 也接受不带协议、或误填了具体端点的写法，插件会规整：

| 你填的 | 实际使用 |
|---|---|
| `gw.example.com` | `https://gw.example.com/v1` |
| `https://gw.example.com/v1/chat/completions` | `https://gw.example.com/v1` |
| `https://gw.example.com/v1` | 原样 |

## 其它选项（都可选）

```json
{
  "apiBase": "https://你的网关/v1",
  "balanceUrl": "https://你的网关/v1/balance",
  "consoleUrl": "https://你的网关/panel/",
  "usageUrl": "https://你的网关/v1/usage/daily",
  "usageDays": 7,
  "models": { "cn:fast-model": { "name": "Fast", "limit": { "context": 200000, "output": 32000 } } }
}
```

- `balanceUrl`：默认 `${apiBase}/balance`
- `consoleUrl`：供应商卡片的「打开」入口，默认由 `apiBase` 推导为 `https://主机/panel/`
- `usageUrl`：按日用量端点，默认 `${apiBase}/usage/daily`
- `usageDays`：往回取几天，1–90，默认 7（只用于算「今天」的窗口，卡片只显示今天）
- `models`：合并进默认模型表（一般不用填，`/v1/models` 才是权威）

## 每日用量

用量卡里会多一行 **今日用量**，例如：

```
今日用量   12 次 · 340k tokens · 0.42 credits
```

数据来自网关的 `GET /v1/usage/daily`（需网关为带 `internal/magpie` 补丁的构建）。

这一行是**只读展示**：插件上报时 `aside: true` 且不给 `used`/`limit`/`balance`，
magpie 因此把它渲染成纯文本（`text: true`），**不画进度条、不参与路由与额度比较**，
不会干扰现有的余额窗口。

降级行为（都不会影响对话）：

| 情况 | 结果 |
|---|---|
| 网关没有该端点（404/405） | 不显示这一行 |
| 网络失败 / 超时（8s） | 不显示这一行 |
| 今天还没有调用 | 不显示这一行 |

> 「今天」由网关按容器时区切（部署为 `TZ=Asia/Shanghai`），不是 magpie 本机时区。

## 验证

```bash
magpie provider test workbuddy2apipanel      # ✓ chat
magpie quota                                 # 账号池余额
```

## 图标

`assets/icon.svg` 为源文件；`package.json` 的 `magpie.icon` 内嵌同一份 SVG 的 **data URI**
（magpie 只接受 https URL 或 data:image URI，不接受内置图标名）。

设计基于官方 CodeBuddy 图形，加了两处改动：右上角 **API** 角标（网关语义）、
右下胶囊改为**在线绿**（API 已接通）。改动克制以保证 16px 仍可辨识。

## 说明

- 余额为 0 或端点不存在时，用量卡片留空而不报错——**余额面板缺失不应影响账号可用性**。
- 已知：magpie 内置的额度走势图只读 `windows[].used`（百分比），本网关给的是剩余口径，
  因此**走势不启用**（`/v1/balance` 只给 `balance`/`limit`，不给 `used`）。
