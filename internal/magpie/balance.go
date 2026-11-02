// Package magpie 为 magpie 客户端（https://usemagpie.ai）提供只读额度查询。
//
// 解耦原则（便于与上游同步）：
//   - 只依赖 pool.Status —— 面板 overview 也在用的同一份对外脱敏视图，
//     不触碰 auth / upstream / panel / scheduler 的任何内部实现；
//   - 纯只读：不发上游请求、不改池状态、不加锁竞争；
//   - 对外同时给出「通用字段」与「magpie usage 字段」，
//     插件可原样返回 usage 部分，无需映射逻辑。
package magpie

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/misakano7545/workbuddy2api-panel/internal/pool"
)

// Snapshot GET /balance 的完整响应体。
type Snapshot struct {
	Object       string    `json:"object"`
	Unit         string    `json:"unit"`
	TotalCredits int64     `json:"total_credits"`
	Total        int       `json:"total"`
	Healthy      int       `json:"healthy"`
	Paused       int       `json:"paused"`
	Cooling      int       `json:"cooling"`
	Disabled     int       `json:"disabled"`
	UpdatedAt    time.Time `json:"updated_at"`
	Accounts     []Account `json:"accounts"`
	Usage        Usage     `json:"usage"`
}

// Account 单账号额度视图（不含任何凭证）。
type Account struct {
	UID             string     `json:"uid"`
	Nickname        string     `json:"nickname,omitempty"`
	Realm           string     `json:"realm,omitempty"`
	Credits         int64      `json:"credits"`
	CreditsTotal    int64      `json:"credits_total,omitempty"`
	Paused          bool       `json:"paused,omitempty"`
	Disabled        bool       `json:"disabled,omitempty"`
	Cooling         bool       `json:"cooling,omitempty"`
	EarliestExpiry  *time.Time `json:"earliest_expiry,omitempty"`
	EarliestRemain  int64      `json:"earliest_remaining,omitempty"`
	ExpiringCredits int64      `json:"expiring_credits,omitempty"`
}

// Usage 字段名对齐 magpie auth.usage() 的返回值。
type Usage struct {
	Plan    string   `json:"plan"`
	User    string   `json:"user"`
	Balance string   `json:"balance"`
	Windows []Window `json:"windows"`
	Error   string   `json:"error"`
}

// Window 一个额度窗口。
//
// 口径（遵循 magpie 文档）：
//   - magpie 的 amount/limit 表示「已用 / 总量」，而本网关统计的是「剩余」；
//     所以这里不写 amount，避免余额页把剩余读成已用；
//   - balance 表示「剩余多少（按 unit 计）」，是文档里预付钱包的语义；
//   - limit 仍给出，供前端算比例；display 用中文明示「剩 / 总」。
type Window struct {
	Name      string     `json:"name"`
	Balance   int64      `json:"balance"`
	Limit     int64      `json:"limit,omitempty"`
	Unit      string     `json:"unit"`
	Display   string     `json:"display"`
	Aside     bool       `json:"aside,omitempty"` // 暂停号：用完不影响路由
	RunsOutAt *time.Time `json:"runsOutAt,omitempty"`
	RunsOut   int64      `json:"runsOut,omitempty"`
}

// Counts 池计数（与 pool.CountsDetailedWithPaused 返回值一一对应）。
type Counts struct {
	Total, Healthy, Cooling, Disabled, Paused int
}

// Build 由 pool.Status 列表构造快照；纯函数，便于单测。
func Build(list []pool.Status, c Counts) Snapshot {
	accounts := make([]Account, 0, len(list))
	var sum int64
	for _, s := range list {
		sum += s.Credits
		var expPtr *time.Time
		if !s.CreditsEarliestExpiry.IsZero() {
			t := s.CreditsEarliestExpiry
			expPtr = &t
		}
		accounts = append(accounts, Account{
			UID:             s.UID,
			Nickname:        s.Nickname,
			Realm:           s.Realm,
			Credits:         s.Credits,
			CreditsTotal:    s.CreditsTotal,
			Paused:          s.Paused,
			Disabled:        s.Disabled,
			Cooling:         s.Cooling,
			EarliestExpiry:  expPtr,
			EarliestRemain:  s.CreditsEarliestRemaining,
			ExpiringCredits: s.CreditsExpiring,
		})
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].UID < accounts[j].UID })

	windows := make([]Window, 0, len(accounts))
	for _, a := range accounts {
		name := a.Nickname
		if name == "" {
			name = a.UID
		}
		if a.Paused {
			name += "（暂停）"
		}
		limit := a.CreditsTotal
		if limit <= 0 {
			limit = a.Credits
		}
		// 剩余口径：balance 给数值；display 明示「剩/总」；不写 amount（= 已用）。
		display := fmt.Sprintf("剩 %d credits", a.Credits)
		if limit > 0 {
			display = fmt.Sprintf("剩 %d / 总 %d credits", a.Credits, limit)
		}
		w := Window{
			Name:    name,
			Balance: a.Credits,
			Limit:   limit,
			Unit:    "credits",
			Display: display,
			Aside:   a.Paused,
		}
		if a.EarliestExpiry != nil {
			w.RunsOutAt = a.EarliestExpiry
			w.RunsOut = a.EarliestRemain
		}
		windows = append(windows, w)
	}

	return Snapshot{
		Object:       "balance",
		Unit:         "credits",
		TotalCredits: sum,
		Total:        c.Total,
		Healthy:      c.Healthy,
		Paused:       c.Paused,
		Cooling:      c.Cooling,
		Disabled:     c.Disabled,
		UpdatedAt:    time.Now().UTC(),
		Accounts:     accounts,
		Usage: Usage{
			Plan:    fmt.Sprintf("WorkBuddy 账号池 (%d)", c.Total),
			User:    "workbuddy-pool",
			Balance: fmt.Sprintf("%d credits", sum),
			Windows: windows,
			Error:   "",
		},
	}
}

// Handler 只读 GET /balance。鉴权由调用方在注册路由时用 withAuth 包裹，
// 本包因此不依赖 server 的鉴权实现。
func Handler(source func() Snapshot) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusMethodNotAllowed)
			_, _ = w.Write([]byte(`{"error":"method_not_allowed"}`))
			return
		}
		body, err := json.Marshal(source())
		if err != nil {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"internal"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(body)
	}
}

// PoolSource 是所需数据源的最小接口；*pool.Pool 天然满足。
// 单独定义接口而不是直接接收 *pool.Pool，是为了单测能注入假数据。
type PoolSource interface {
	List() []pool.Status
	CountsDetailedWithPaused() (total, healthy, cooling, disabled, paused, inFlightFull int)
}

// Balance 返回 GET /v1/balance 的 handler。鉴权由调用方以 withAuth 包裹。
func Balance(src PoolSource) http.HandlerFunc {
	return Handler(func() Snapshot {
		total, healthy, cooling, disabled, paused, _ := src.CountsDetailedWithPaused()
		return Build(src.List(), Counts{
			Total: total, Healthy: healthy, Cooling: cooling,
			Disabled: disabled, Paused: paused,
		})
	})
}
