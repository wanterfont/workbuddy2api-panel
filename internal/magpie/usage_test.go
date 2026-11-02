package magpie

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/misakano7545/workbuddy2api-panel/internal/usage"
)

// fixedNow 是一个本地时区的固定时刻，用来断言「今天」的边界。
func fixedNow(t *testing.T) time.Time {
	t.Helper()
	return time.Date(2026, 11, 2, 7, 57, 0, 0, time.Local)
}

// fakeUsage 是 UsageSource 的假实现：记录收到的窗口，回放预置快照。
type fakeUsage struct {
	gotWindow usage.Window
	snap      usage.Snapshot
}

func (f *fakeUsage) SnapshotWindow(w usage.Window, _ map[string]string, _ func(string, string) string) usage.Snapshot {
	f.gotWindow = w
	return f.snap
}

// pointAt 按 internal/usage 的真实约定造一个时序点：
// Scope 是 "hour"，T 是 "YYYY-MM-DDTHH"。
func pointAt(day string, hour int, a usage.Agg) usage.Point {
	return usage.Point{
		T:     day + "T" + fmtTwo(hour),
		Scope: "hour",
		Agg:   a,
	}
}

// dayPoint 造一个已折叠的日桶：Scope "day"，T 是 "YYYY-MM-DD"。
func dayPoint(day string, a usage.Agg) usage.Point {
	return usage.Point{T: day, Scope: "day", Agg: a}
}

func fmtTwo(n int) string {
	return string([]byte{byte('0' + n/10), byte('0' + n%10)})
}

func TestBuildDailyCountsTodayAndGroupsByDay(t *testing.T) {
	u := &fakeUsage{snap: usage.Snapshot{
		Series: []usage.Point{
			pointAt("2026-11-01", 10, usage.Agg{Requests: 2, PromptTokens: 100, CompletionTok: 50, TotalTokens: 150, Credits: 0.5}),
			pointAt("2026-11-01", 11, usage.Agg{Requests: 3, PromptTokens: 200, CompletionTok: 60, TotalTokens: 260, Credits: 1.0}),
			pointAt("2026-11-02", 7, usage.Agg{Requests: 1, PromptTokens: 10, CompletionTok: 5, TotalTokens: 15, Credits: 0.25}),
		},
	}}
	d := BuildDaily(u, 7, fixedNow(t))

	if d.Days != 7 {
		t.Fatalf("days = %d", d.Days)
	}
	// 「今天」= 本地时区的 2026-11-02，只含当天那一个点
	if d.Today.Requests != 1 || d.Today.Tokens != 15 {
		t.Fatalf("today = %+v", d.Today)
	}
	if d.Today.Credits != 0.25 {
		t.Fatalf("today credits = %v", d.Today.Credits)
	}
	// 11-01 的两个小时点合并成一行
	if len(d.ByDay) != 2 {
		t.Fatalf("by_day len = %d, %+v", len(d.ByDay), d.ByDay)
	}
	if d.ByDay[0].Day != "2026-11-01" || d.ByDay[0].Requests != 5 || d.ByDay[0].Tokens != 410 {
		t.Fatalf("by_day[0] = %+v", d.ByDay[0])
	}
	if d.ByDay[0].Credits != 1.5 {
		t.Fatalf("by_day[0] credits = %v", d.ByDay[0].Credits)
	}
	// 升序
	if d.ByDay[1].Day != "2026-11-02" {
		t.Fatalf("by_day order = %+v", d.ByDay)
	}
}

func TestBuildDailyWindowStartsAtLocalMidnight(t *testing.T) {
	u := &fakeUsage{}
	BuildDaily(u, 7, fixedNow(t))
	from := u.gotWindow.From
	if from.Hour() != 0 || from.Minute() != 0 || from.Second() != 0 {
		t.Fatalf("window does not start at midnight: %v", from)
	}
	// 7 天含今天：起点是 10-27 00:00
	if want := time.Date(2026, 10, 27, 0, 0, 0, 0, time.Local); !from.Equal(want) {
		t.Fatalf("window from = %v, want %v", from, want)
	}
}

// 日桶（90 天前折叠）与同一天的小时桶不会同时出现；若同时出现，以日桶为准，不翻倍。
func TestBuildDailyPrefersFoldedDayBucket(t *testing.T) {
	u := &fakeUsage{snap: usage.Snapshot{
		Series: []usage.Point{
			dayPoint("2026-11-01", usage.Agg{Requests: 9, TotalTokens: 900, Credits: 9}),
			pointAt("2026-11-01", 10, usage.Agg{Requests: 100, TotalTokens: 1000, Credits: 100}),
		},
	}}
	d := BuildDaily(u, 7, fixedNow(t))
	if len(d.ByDay) != 1 {
		t.Fatalf("by_day = %+v", d.ByDay)
	}
	if d.ByDay[0].Requests != 9 || d.ByDay[0].Tokens != 900 {
		t.Fatalf("day bucket not preferred: %+v", d.ByDay[0])
	}
}

func TestBuildDailyClampsDays(t *testing.T) {
	for _, c := range []struct{ in, want int }{{0, 1}, {-5, 1}, {1, 1}, {365, 90}, {7, 7}} {
		d := BuildDaily(&fakeUsage{}, c.in, fixedNow(t))
		if d.Days != c.want {
			t.Fatalf("days(%d) = %d, want %d", c.in, d.Days, c.want)
		}
	}
}

func TestBuildDailyNilSource(t *testing.T) {
	d := BuildDaily(nil, 7, fixedNow(t))
	if d.Today.Requests != 0 || len(d.ByDay) != 0 {
		t.Fatalf("nil source should be empty: %+v", d)
	}
	if d.ByAccount == nil || d.ByModel == nil {
		t.Fatalf("slices should be empty, not nil (JSON [] not null)")
	}
}

func TestBuildDailyAccountAndModelRowsSorted(t *testing.T) {
	u := &fakeUsage{snap: usage.Snapshot{
		ByAccount: []usage.KeyedAgg{
			{Key: "u1", Realm: "cn", Extra: "账号 B", Agg: usage.Agg{Requests: 1, TotalTokens: 10, Credits: 0.1}},
			{Key: "u2", Realm: "cn", Extra: "账号 C", Agg: usage.Agg{Requests: 9, TotalTokens: 90, Credits: 5}},
		},
		ByModel: []usage.KeyedAgg{
			{Key: "cn:fast-model", Agg: usage.Agg{Requests: 3, TotalTokens: 30, Credits: 0.2}},
		},
	}}
	d := BuildDaily(u, 7, fixedNow(t))
	if len(d.ByAccount) != 2 || d.ByAccount[0].Key != "u2" {
		t.Fatalf("by_account not sorted by credits: %+v", d.ByAccount)
	}
	if d.ByAccount[0].Nickname != "账号 C" {
		t.Fatalf("nickname lost: %+v", d.ByAccount[0])
	}
	if len(d.ByModel) != 1 || d.ByModel[0].Key != "cn:fast-model" {
		t.Fatalf("by_model = %+v", d.ByModel)
	}
}

func TestDailyHandlerDaysParam(t *testing.T) {
	u := &fakeUsage{}
	h := DailyHandler(u, func() time.Time { return fixedNow(t) })

	for _, c := range []struct {
		query string
		code  int
		days  int
	}{
		{"", 200, 7},
		{"?days=30", 200, 30},
		{"?days=abc", 400, 0},
		{"?days=0", 400, 0},
		{"?days=-1", 400, 0},
		{"?days=999", 200, 90},
	} {
		req := httptest.NewRequest(http.MethodGet, "/v1/usage/daily"+c.query, nil)
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != c.code {
			t.Fatalf("%q: code = %d, want %d", c.query, rec.Code, c.code)
		}
		if c.code != 200 {
			continue
		}
		var got Daily
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("%q: bad json: %v", c.query, err)
		}
		if got.Days != c.days {
			t.Fatalf("%q: days = %d, want %d", c.query, got.Days, c.days)
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%q: cache-control missing", c.query)
		}
	}
}

func TestDailyHandlerMethodAndHead(t *testing.T) {
	h := DailyHandler(&fakeUsage{}, func() time.Time { return fixedNow(t) })

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/v1/usage/daily", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST code = %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodHead, "/v1/usage/daily", nil))
	if rec.Code != 200 {
		t.Fatalf("HEAD code = %d", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("HEAD wrote a body: %q", rec.Body.String())
	}
}

// 响应里不得出现 token / key / 会话内容这类字段。
func TestDailyHasNoSecrets(t *testing.T) {
	u := &fakeUsage{snap: usage.Snapshot{
		ByAccount: []usage.KeyedAgg{{Key: "u1", Extra: "账号 B", Agg: usage.Agg{Credits: 1}}},
	}}
	body, err := json.Marshal(BuildDaily(u, 7, fixedNow(t)))
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"access_token", "refresh_token", "api_key", "password", "cookie", "session", "authorization"} {
		if containsFold(string(body), banned) {
			t.Fatalf("response carries %q: %s", banned, body)
		}
	}
}

func containsFold(s, sub string) bool {
	s, sub = lower(s), lower(sub)
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func lower(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 32
		}
	}
	return string(b)
}
