package magpie

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/misakano7545/workbuddy2api-panel/internal/pool"
)

func sampleStatus() []pool.Status {
	exp := time.Date(2030, 1, 1, 0, 0, 0, 0, time.FixedZone("CST", 8*3600))
	return []pool.Status{
		{UID: "u2", Nickname: "账号 A", Realm: "cn", Credits: 3000, CreditsTotal: 5000,
			CreditsEarliestExpiry: exp, CreditsEarliestRemaining: 3000},
		{UID: "u1", Nickname: "账号 B", Realm: "cn", Credits: 2000, CreditsTotal: 2000,
			Paused: true, CreditsEarliestExpiry: exp, CreditsEarliestRemaining: 2000},
	}
}

func TestBuildTotalsAndWindows(t *testing.T) {
	s := Build(sampleStatus(), Counts{Total: 2, Healthy: 1, Paused: 1})
	if s.TotalCredits != 5000 {
		t.Fatalf("total_credits = %d, want 5000", s.TotalCredits)
	}
	if s.Usage.Balance != "5000 credits" {
		t.Fatalf("usage.balance = %q", s.Usage.Balance)
	}
	if s.Usage.Plan == "" || s.Usage.Error != "" {
		t.Fatalf("plan/error unexpected: %+v", s.Usage)
	}
	if len(s.Usage.Windows) != 2 {
		t.Fatalf("windows = %d, want 2", len(s.Usage.Windows))
	}
	// 按 UID 排序：u1 在前
	if s.Usage.Windows[0].Name != "账号 B（暂停）" {
		t.Fatalf("window[0].name = %q", s.Usage.Windows[0].Name)
	}
	if !s.Usage.Windows[0].Aside {
		t.Fatal("paused account must be aside=true")
	}
	if s.Usage.Windows[1].Aside {
		t.Fatal("healthy account must not be aside")
	}
	if s.Usage.Windows[0].RunsOut != 2000 || s.Usage.Windows[0].RunsOutAt.IsZero() {
		t.Fatalf("runsOut fields wrong: %+v", s.Usage.Windows[0])
	}
}

func TestBuildEmpty(t *testing.T) {
	s := Build(nil, Counts{})
	if s.TotalCredits != 0 || len(s.Usage.Windows) != 0 {
		t.Fatalf("empty pool snapshot wrong: %+v", s)
	}
	if s.Usage.Balance != "0 credits" {
		t.Fatalf("balance = %q", s.Usage.Balance)
	}
}

func TestBuildLimitFallback(t *testing.T) {
	// credits_total 为 0（未知）时，limit 回落为 credits，避免显示 100/0
	s := Build([]pool.Status{{UID: "x", Credits: 100}}, Counts{Total: 1, Healthy: 1})
	if s.Usage.Windows[0].Limit != 100 {
		t.Fatalf("limit fallback = %d, want 100", s.Usage.Windows[0].Limit)
	}
}

func TestHandlerGETJSON(t *testing.T) {
	h := Handler(func() Snapshot {
		return Build(sampleStatus(), Counts{Total: 2, Healthy: 1, Paused: 1})
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/balance", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct == "" {
		t.Fatal("missing content-type")
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("cache-control = %q", cc)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if got["object"] != "balance" {
		t.Fatalf("object = %v", got["object"])
	}
}

func TestHandlerRejectsPOST(t *testing.T) {
	h := Handler(func() Snapshot { return Build(nil, Counts{}) })
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/balance", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

// TestWindowUsesBalanceNotAmount 锁定口径：必须给 balance（剩余），
// 不能给 amount（magpie 语义为「已用」），否则余额页会读反。
func TestWindowUsesBalanceNotAmount(t *testing.T) {
	s := Build([]pool.Status{{UID: "u", Credits: 700, CreditsTotal: 1000}}, Counts{Total: 1, Healthy: 1})
	raw, err := json.Marshal(s.Usage.Windows[0])
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if _, has := m["amount"]; has {
		t.Fatalf("window must not emit amount (means used in magpie): %s", raw)
	}
	if got := m["balance"]; got != float64(700) {
		t.Fatalf("balance = %v, want 700", got)
	}
	if got := m["limit"]; got != float64(1000) {
		t.Fatalf("limit = %v, want 1000", got)
	}
}

// TestBuildZeroCreditsZeroLimit 0/0 不应 panic 或产生奇怪字符串。
func TestBuildZeroCreditsZeroLimit(t *testing.T) {
	s := Build([]pool.Status{{UID: "z"}}, Counts{Total: 1})
	w := s.Usage.Windows[0]
	if w.Balance != 0 || w.Limit != 0 {
		t.Fatalf("unexpected: %+v", w)
	}
	if w.Display != "剩 0 credits" {
		t.Fatalf("display = %q", w.Display)
	}
}

// TestBuildPausedFlagsAside paused 账号必须 aside=true（不参与路由判定）。
func TestBuildPausedFlagsAside(t *testing.T) {
	s := Build([]pool.Status{{UID: "p", Credits: 10, Paused: true}}, Counts{Total: 1, Paused: 1})
	if !s.Usage.Windows[0].Aside {
		t.Fatal("paused window must be aside")
	}
}

// TestHandlerHead 覆盖 HEAD 方法（Go 服务端会自动丢弃 body）。
func TestHandlerHead(t *testing.T) {
	h := Handler(func() Snapshot { return Build(nil, Counts{}) })
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/v1/balance", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

// TestUsageErrorField 空池时 error 必须为空串（不是缺省）。
func TestUsageErrorField(t *testing.T) {
	s := Build(nil, Counts{})
	if s.Usage.Error != "" {
		t.Fatalf("error = %q, want empty", s.Usage.Error)
	}
}
