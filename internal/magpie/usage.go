package magpie

import (
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/misakano7545/workbuddy2api-panel/internal/usage"
)

// 本文件为 magpie 提供按日用量聚合（GET /v1/usage/daily）。
//
// 解耦原则（与 balance.go 一致）：
//   - 只读 cfg.Usage 这一份既有的 Recorder，不参与写入、不发上游请求；
//   - 复用 internal/usage 的桶聚合，本包不复制任何统计逻辑；
//   - 删除本文件 + handler.go 里那 1 行即可完全回到上游原状。
//
// 「日」按容器本地时区切分，与 internal/usage 的 dayLayout 口径一致
// （部署环境 TZ=Asia/Shanghai）。

const (
	// dayMost 是 /v1/usage/daily 允许回溯的最大天数。小时桶只保留 90 天，
	// 更早已被折叠成日桶，口径不再逐小时可比，故上限取 90。
	dayMost = 90
)

// DayTotals 是一天（或一段窗口）的用量合计。
type DayTotals struct {
	Requests int64   `json:"requests"`
	Errors   int64   `json:"errors"`
	Input    int64   `json:"input_tokens"`
	Output   int64   `json:"output_tokens"`
	Cache    int64   `json:"cache_read_tokens"`
	Tokens   int64   `json:"total_tokens"`
	Credits  float64 `json:"credits"`
}

// DayPoint 是序列里的一天。
type DayPoint struct {
	Day string `json:"day"` // YYYY-MM-DD，本地时区
	DayTotals
}

// DayRow 是按账号或模型拆开的一行。
type DayRow struct {
	Key      string  `json:"key"`
	Realm    string  `json:"realm,omitempty"`
	Nickname string  `json:"nickname,omitempty"`
	Requests int64   `json:"requests"`
	Tokens   int64   `json:"total_tokens"`
	Credits  float64 `json:"credits"`
}

// Daily 是 GET /v1/usage/daily 的响应体。
//
// 字段全部是「数字」，不含 token / key / 会话内容：与 /v1/balance 同级信任，
// 由网关 api_key 鉴权。
type Daily struct {
	Generated string     `json:"generated"`
	Days      int        `json:"days"`
	Today     DayTotals  `json:"today"`
	ByDay     []DayPoint `json:"by_day"`
	ByAccount []DayRow   `json:"by_account"`
	ByModel   []DayRow   `json:"by_model"`
}

// UsageSource 是所需数据源的最小接口；*usage.Recorder 天然满足。
// 单独定义接口而不是直接接收 *usage.Recorder，是为了单测能注入假数据
// （与 balance.go 的 PoolSource 同一做法）。
type UsageSource interface {
	SnapshotWindow(w usage.Window, nicks map[string]string, currentRate func(realm, model string) string) usage.Snapshot
}

// BuildDaily 把 Recorder 的窗口快照投影成按日视图。
//
// from 是窗口起点（本地时区的某天 00:00），now 用来定「今天」。
// 全程只读：SnapshotWindow 内部复制桶后聚合，不阻塞写入。
func BuildDaily(src UsageSource, days int, now time.Time) Daily {
	if days < 1 {
		days = 1
	}
	if days > dayMost {
		days = dayMost
	}
	now = now.In(time.Local)
	startOfToday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	from := startOfToday.AddDate(0, 0, -(days - 1))

	out := Daily{
		Generated: now.Format(time.RFC3339),
		Days:      days,
		ByDay:     make([]DayPoint, 0, days),
		ByAccount: []DayRow{},
		ByModel:   []DayRow{},
	}
	if src == nil {
		return out
	}
	snap := src.SnapshotWindow(usage.Window{From: from}, nil, nil)

	// 按日归并时序点。日桶（90 天前折叠）与小时桶不会落在同一天，但仍以
	// 「该天已有日桶则用日桶、否则累加小时桶」兜底，避免口径翻倍。
	dayAgg := make(map[string]*DayTotals, days)
	dayFolded := make(map[string]bool, days)
	for _, p := range snap.Series {
		day, folded, ok := pointDay(p.Scope, p.T)
		if !ok {
			continue
		}
		if folded {
			if dayFolded[day] {
				continue
			}
			dayFolded[day] = true
			dayAgg[day] = totalsOf(p.Agg)
			continue
		}
		if dayFolded[day] {
			continue
		}
		cur := dayAgg[day]
		if cur == nil {
			cur = &DayTotals{}
			dayAgg[day] = cur
		}
		addAgg(cur, p.Agg)
	}

	days_sorted := make([]string, 0, len(dayAgg))
	for d := range dayAgg {
		days_sorted = append(days_sorted, d)
	}
	sort.Strings(days_sorted)
	for _, d := range days_sorted {
		out.ByDay = append(out.ByDay, DayPoint{Day: d, DayTotals: *dayAgg[d]})
	}

	todayKey := startOfToday.Format("2006-01-02")
	if t, ok := dayAgg[todayKey]; ok {
		out.Today = *t
	}

	for _, a := range snap.ByAccount {
		out.ByAccount = append(out.ByAccount, DayRow{
			Key: a.Key, Realm: a.Realm, Nickname: a.Extra,
			Requests: a.Requests, Tokens: a.TotalTokens, Credits: a.Credits,
		})
	}
	sortRows(out.ByAccount)
	for _, m := range snap.ByModel {
		out.ByModel = append(out.ByModel, DayRow{
			Key: m.Key, Realm: m.Realm,
			Requests: m.Requests, Tokens: m.TotalTokens, Credits: m.Credits,
		})
	}
	sortRows(out.ByModel)
	return out
}

// pointDay 把一个时序点归到它所属的那一天（本地时区，YYYY-MM-DD）。
//
// usage.Point 的约定（internal/usage/usage.go，SnapshotWindow 末尾）：
//   - Scope 只有两个值 "day" / "hour"；
//   - T 是去掉前缀的桶键：日桶 "2026-11-01"，小时桶 "2026-11-01T14"。
//
// 第二个返回值表示这是不是已折叠的日桶——日桶与小时桶不会落在同一天，
// 但仍以日桶优先兜底，避免口径翻倍。
func pointDay(scope, t string) (day string, folded bool, ok bool) {
	switch scope {
	case "day":
		if d, good := dayOf(t); good {
			return d, true, true
		}
	case "hour":
		if d, good := dayOf(t); good {
			return d, false, true
		}
	}
	if d, good := dayOf(t); good {
		return d, false, true
	}
	return "", false, false
}

// dayOf 取一个桶键的前 10 个字符（YYYY-MM-DD）并校验它是本地时区的合法日期。
func dayOf(v string) (string, bool) {
	if len(v) < 10 {
		return "", false
	}
	if _, err := time.ParseInLocation("2006-01-02", v[:10], time.Local); err != nil {
		return "", false
	}
	return v[:10], true
}

func totalsOf(a usage.Agg) *DayTotals {
	d := &DayTotals{}
	addAgg(d, a)
	return d
}

func addAgg(d *DayTotals, a usage.Agg) {
	d.Requests += a.Requests
	d.Errors += a.Errors
	d.Input += a.PromptTokens
	d.Output += a.CompletionTok
	d.Cache += a.CacheHitTok
	d.Tokens += a.TotalTokens
	d.Credits += a.Credits
}

// sortRows 按 credits 降序、同额按 tokens 降序，让「花得最多」的排在最前。
func sortRows(rows []DayRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Credits != rows[j].Credits {
			return rows[i].Credits > rows[j].Credits
		}
		return rows[i].Tokens > rows[j].Tokens
	})
}

// DailyHandler 返回 GET /v1/usage/daily 的 handler。
// 鉴权由调用方以 withAuth 包裹，本包因此不依赖 server 的鉴权实现。
//
// 查询参数 days：1..90，默认 7。响应不缓存（Cache-Control: no-store）。
func DailyHandler(src UsageSource, now func() time.Time) http.HandlerFunc {
	if now == nil {
		now = time.Now
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
			return
		}
		days := 7
		if v := r.URL.Query().Get("days"); v != "" {
			n, err := parseDays(v)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_days"})
				return
			}
			days = n
		}
		body, err := json.Marshal(BuildDaily(src, days, now()))
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write(body)
	}
}

// parseDays 只接受纯数字；越界在 BuildDaily 里夹取，这里只拒绝非数字。
func parseDays(s string) (int, error) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errBadDays
		}
		n = n*10 + int(c-'0')
		if n > 100000 {
			return 0, errBadDays
		}
	}
	if n == 0 {
		return 0, errBadDays
	}
	return n, nil
}

var errBadDays = &jsonError{"bad_days"}

type jsonError struct{ s string }

func (e *jsonError) Error() string { return e.s }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
