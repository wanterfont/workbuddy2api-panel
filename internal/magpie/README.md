# internal/magpie —— magpie 额度端点

给 [magpie](https://usemagpie.ai) 的 `auth.usage` 钩子提供数据的**只读**端点，
与对话接口同一把 `api_key` 鉴权：

- `GET /v1/balance` —— 账号池余额快照（`auth.usage` 的数据源）
- `GET /v1/usage/daily?days=N` —— 按日用量聚合（N 为 1..90，默认 7）
  - 数据来自网关既有的 `internal/usage`（`data/usage.json`），本包只做投影，不复制统计逻辑
  - 「日」按容器本地时区切（部署为 `TZ=Asia/Shanghai`）
  - 响应只含数字：requests / tokens / credits，不含 token、key、会话内容

客户端插件在 [`magpie-plugin/`](../../magpie-plugin)。

## 改动面（全部）

| 文件 | 改动 |
|---|---|
| `internal/magpie/balance.go` | 新增 |
| `internal/magpie/balance_test.go` | 新增 |
| `internal/magpie/usage.go` | 新增 |
| `internal/magpie/usage_test.go` | 新增 |
| `internal/magpie/README.md` | 新增（本文件） |
| `magpie-plugin/` | 新增（magpie 客户端插件） |
| `internal/server/handler.go` | **1 行 import + 3 行注释 + 2 个 if 块（6 行）** |

`handler.go` 的插入点在 `GET /v1/capacity` 之后：

```go
	// --- magpie 插件支持（见 internal/magpie/README.md）---
	// 只读额度端点，与 /v1/capacity 同一套 withAuth；纯读池快照，不发上游请求。
	// 删除本段 + internal/magpie/ 即可完全回到上游原状。
	if cfg.Pool != nil {
		h.mux.HandleFunc("GET /v1/balance", h.withAuth(magpie.Balance(cfg.Pool)))
	}
	if cfg.Usage != nil {
		h.mux.HandleFunc("GET /v1/usage/daily", h.withAuth(magpie.DailyHandler(cfg.Usage, nil)))
	}
```

import 区加一行：

```go
	"github.com/misakano7545/workbuddy2api-panel/internal/magpie"
```

## 提交拆分

改动在 `feat/magpie-plugin` 分支上，基于 `main` 的 `38e4f39`，拆成 4 个提交：

| 提交 | 内容 |
|---|---|
| `feat(magpie): 只读额度端点 GET /v1/balance` | `balance.go` + 测试 + handler 注册 |
| `feat(magpie): 按日用量端点 GET /v1/usage/daily` | `usage.go` + 测试 + handler 注册 |
| `feat(magpie-plugin): magpie 客户端插件` | `magpie-plugin/` |
| `docs(magpie): 端点契约与补丁说明` | 本文件 |

所以「本地改动 vs 上游」直接用 `git diff main...feat/magpie-plugin` 看，不会漏掉 handler 里那两段。

## 回退

### 方式一：有 git 历史时（推荐）

```bash
git revert --no-commit <4 个提交> && git commit    # 或直接不合并这个分支
```

### 方式二：部署目录解包自 release、没有 `.git` 时

改前先备份 `cp internal/server/handler.go internal/server/handler.go.bak-magpie-$(date +%Y%m%d-%H%M%S)`，
回退时：

```bash
cd <release-dir>/internal/server
cp -a handler.go.bak-magpie-<时间戳> handler.go    # 逐字节还原
cd ../.. && rm -rf internal/magpie magpie-plugin
```

也可以用反向 patch，但**必须用 plain `diff`**：

```bash
diff handler.go.bak-magpie-<时间戳> handler.go > /tmp/magpie.revert   # 不要 -u
patch -R -p0 < /tmp/magpie.revert                                     # 实测 rc=0
```

> ⚠️ **实测踩坑记录**（2026-11-01）：
> - `patch -R -p0` + **plain diff** ✅ 可用；
> - `patch -R -p1` ❌ 失败 —— plain diff 无文件头，patch 报 `Only garbage was found in the patch input`；
> - `diff -u ... | git apply -R` ❌ 失败 —— `diff -u` 的文件名是 `handler.go.bak-...`，不是 `a/ b/` 形式；
> - `git apply -R` ❌ 失败 —— 该源码目录解包自 release，**没有 `.git`**。
>
> 也不要手工「删 N 行」：插入块是 **1 行 import + 3 行注释 + 6 行代码**，数错会让 `go build` 直接失败。

## 同步上游

改动与上游的唯一接触点是 `handler.go` 那两段，rebase 冲突基本只会出现在这里：

```bash
git fetch upstream          # https://github.com/misakano7545/workbuddy2api-panel
git rebase upstream/main    # 冲突 → 保留 /v1/capacity 之后的两段注册
go build -buildvcs=false ./... && go test -buildvcs=false -count=1 ./internal/magpie/
```

自检：`diff main..HEAD -- internal/server/handler.go | grep -c '^+[^+]'` 应为 **10**（1 import + 3 行注释 + 6 行代码），
`gofmt -l internal/magpie` 应当无输出。

> 已知：全量 `go test ./...` 中 `internal/updater` 会失败（需要预先构建的 `wb2api` 二进制打包成 zip 作 fixture），
> 这是环境限制，**与补丁无关**（已用上游原版做对照实验确认，失败输出完全相同）。

## 端点契约

### `GET /v1/balance`

鉴权与 `/v1/models` 相同（`Authorization: Bearer <api_key>`）。

```json
{
  "object": "balance",
  "unit": "credits",
  "total_credits": 5000,
  "total": 2, "healthy": 1, "paused": 1, "cooling": 0, "disabled": 0,
  "updated_at": "2024-01-01T00:00:00Z",
  "accounts": [
    { "uid": "u1", "nickname": "账号 B", "realm": "cn",
      "credits": 2000, "credits_total": 2000, "paused": true,
      "earliest_expiry": "2030-01-01T00:00:00+08:00", "earliest_remaining": 2000 },
    { "uid": "u2", "nickname": "账号 A", "realm": "cn",
      "credits": 3000, "credits_total": 5000,
      "earliest_expiry": "2030-01-01T00:00:00+08:00", "earliest_remaining": 3000 }
  ],
  "usage": {
    "plan": "WorkBuddy 账号池 (2)",
    "user": "workbuddy-pool",
    "balance": "5000 credits",
    "windows": [
      { "name": "账号 B（暂停）", "balance": 2000, "limit": 2000, "unit": "credits",
        "display": "剩 2000 / 总 2000 credits", "aside": true,
        "runsOutAt": "2030-01-01T00:00:00+08:00", "runsOut": 2000 },
      { "name": "账号 A", "balance": 3000, "limit": 5000, "unit": "credits",
        "display": "剩 3000 / 总 5000 credits",
        "runsOutAt": "2030-01-01T00:00:00+08:00", "runsOut": 3000 }
    ],
    "error": ""
  }
}
```

### `GET /v1/usage/daily?days=N`

`days` 取值 1..90（越界或非数字回落默认 7）。

```json
{
  "generated": "2024-01-01T00:00:00Z",
  "days": 7,
  "today": { "day": "2024-01-01", "requests": 12, "errors": 0,
             "input_tokens": 300000, "output_tokens": 40000,
             "cache_read_tokens": 0, "total_tokens": 340000, "credits": 0.42 },
  "by_day":     [ { "day": "2024-01-01", "requests": 12, "...": "..." } ],
  "by_account": [ { "key": "...", "realm": "cn", "nickname": "账号 A",
                    "requests": 12, "total_tokens": 340000, "credits": 0.42 } ],
  "by_model":   [ { "key": "cn:fast-model", "requests": 12, "...": "..." } ]
}
```

只要 `today` 就够画「今日用量」那一行；`by_*` 是给日后的图表留的。

### 字段口径（重要）

- `total_credits`：**全部账号剩余 credits 之和（含 paused）**。
- `usage.*`：直接就是 magpie `auth.usage` 的返回形状，插件可原样透传。
- **`windows[].balance` = 剩余，`limit` = 总量；故意不给 `amount`。**
  magpie 文档里 `amount/limit` 是「已用 / 总量」，而本网关统计剩余——
  给 `amount` 会让余额页把剩余读成已用。`display` 用中文「剩 / 总」明示。
- `windows[].aside=true`：暂停账号（paused）。magpie 不会据此判定额度耗尽，符合 paused 语义。
- `windows[].runsOutAt / runsOut`：最早到期批次的时间与数量。
- `accounts[]`：`credits` 剩余、`credits_total` 总量；`earliest_expiry` 为零值时**不输出**该字段。

## 设计取舍

- **只读**：只调用 `Pool.List()` 与 `CountsDetailedWithPaused()`——面板 overview 与 `/status` 在用的只读视图；
  不发上游请求、不改池状态、不启动新 goroutine。
- **不依赖鉴权实现**：handler 由 `server.withAuth` 包裹，本包不知道 key 从哪来。
- **不泄漏凭证**：输出只有 uid / 昵称 / 域 / 额度，没有 token、session、cookie。
- **nil 安全**：`cfg.Pool == nil` / `cfg.Usage == nil` 时不注册路由（返回 404），而不是像 `/v1/capacity` 那样潜在 panic。
