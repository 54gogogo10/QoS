package web

// v2.10.0 新功能测试：/api/rates 动态调速、控制面 token 门禁、
// 控制端点每 IP 限流、/metrics 指标端点。

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"qostool/internal/config"
	"qostool/internal/controller"
	"qostool/internal/stats"
)

// postJSON 向 server handler 发 JSON POST（httptest 直连，不经网络）。
func postJSON(t *testing.T, s *Server, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	return rec
}

func TestHandleRatesLifecycle(t *testing.T) {
	cfg := &config.Config{Flows: []config.Flow{{
		Name: "ef", Protocol: "udp", SrcIP: "127.0.0.1", DstIP: "127.0.0.1",
		SrcPort: 14001, DstPort: 14002, DSCP: 46, RatePPS: 1000, IPLen: 92,
	}}}
	send := controller.New(cfg, controller.ModeSend)
	s := New(map[controller.Mode]*controller.Controller{controller.ModeSend: send},
		map[controller.Mode]string{}, true)
	// 未运行：400
	if rec := postJSON(t, s, "/api/rates?mode=send", `{"mode":"send","scale":1.5}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("未运行调速应 400, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	if err := send.Start(""); err != nil {
		t.Fatalf("send 启动失败: %v", err)
	}
	defer send.Stop()
	rec := postJSON(t, s, "/api/rates?mode=send", `{"mode":"send","scale":1.5}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("运行中调速应 200, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	var out struct {
		OK    bool    `json:"ok"`
		Scale float64 `json:"scale"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || out.Scale != 1.5 || send.LiveScale() != 1.5 {
		t.Fatalf("响应 scale=%v LiveScale=%v, want 1.5", out.Scale, send.LiveScale())
	}
	// 非法倍率：400 且倍率不变
	if rec := postJSON(t, s, "/api/rates?mode=send", `{"mode":"send","scale":-1}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("负倍率应 400, got %d", rec.Code)
	}
	if send.LiveScale() != 1.5 {
		t.Fatalf("非法倍率不应改变当前值, got %v", send.LiveScale())
	}
	// 非数字：decode 失败 400
	if rec := postJSON(t, s, "/api/rates?mode=send", `{"mode":"send","scale":"abc"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("非数字倍率应 400, got %d", rec.Code)
	}
}

func TestHandleRatesReadOnlyMode(t *testing.T) {
	cfg := &config.Config{Flows: []config.Flow{{
		Name: "ef", Protocol: "udp", SrcIP: "127.0.0.1", DstIP: "127.0.0.1",
		SrcPort: 1, DstPort: 2, DSCP: 46, RatePPS: 1000, IPLen: 92,
	}}}
	s := New(map[controller.Mode]*controller.Controller{controller.ModeBidir: controller.New(cfg, controller.ModeBidir)},
		map[controller.Mode]string{}, false) // CLI 只读
	if rec := postJSON(t, s, "/api/rates", `{"scale":2}`); rec.Code != http.StatusForbidden {
		t.Fatalf("CLI 只读模式调速应 403, got %d", rec.Code)
	}
}

func TestTokenAuth(t *testing.T) {
	s := newTestServer()
	s.SetToken("sekret")
	// 无 token → 401
	if rec := postJSON(t, s, "/api/stop", `{"mode":"recv"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("无 token 应 401, got %d", rec.Code)
	}
	// 错误 token → 401
	req := httptest.NewRequest(http.MethodPost, "/api/stop", strings.NewReader(`{"mode":"recv"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Auth-Token", "wrong")
	rec := httptest.NewRecorder()
	s.handleStop(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("错误 token 应 401, got %d", rec.Code)
	}
	// 正确 token → 200
	req2 := httptest.NewRequest(http.MethodPost, "/api/stop", strings.NewReader(`{"mode":"recv"}`))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("X-Auth-Token", "sekret")
	rec2 := httptest.NewRecorder()
	s.handleStop(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("正确 token 应 200, got %d (body=%s)", rec2.Code, rec2.Body.String())
	}
	// 读端点不受 token 影响
	rec3 := httptest.NewRecorder()
	s.handleStats(rec3, httptest.NewRequest(http.MethodGet, "/api/stats", nil))
	if rec3.Code != http.StatusOK {
		t.Fatalf("读端点不应要求 token, got %d", rec3.Code)
	}
}

// TestTokenGatesRemoteSync CLI 只读模式 + token：机器间 remote 通知同样必须携带
// 正确 token（token 为空的对端兼容路径由 NotifyPeer* 侧不带头发送覆盖）。
func TestTokenGatesRemoteSync(t *testing.T) {
	cfg := &config.Config{Flows: []config.Flow{
		{Name: "ef", Protocol: "udp", SrcIP: "127.0.0.1", DstIP: "127.0.0.1",
			SrcPort: 1, DstPort: 2, DSCP: 46, RateMbps: 1, IPLen: 92},
	}}
	s := New(map[controller.Mode]*controller.Controller{
		controller.ModeRecv: controller.New(cfg, controller.ModeRecv),
	}, map[controller.Mode]string{}, false) // CLI 模式
	s.SetToken("tok")
	// 无 token 的 remote 通知 → 401（token 门禁先于 remote 放行逻辑）
	if rec := postJSON(t, s, "/api/start", `{"mode":"recv","remote":true}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("无 token remote 通知应 401, got %d", rec.Code)
	}
	// 带正确 token：通过门禁进入接口处理（无 iface → 400 提示，而非 403/401）
	req := httptest.NewRequest(http.MethodPost, "/api/start", strings.NewReader(`{"mode":"recv","remote":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Auth-Token", "tok")
	rec := httptest.NewRecorder()
	s.handleStart(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "iface") {
		t.Fatalf("带 token remote 通知应进入接口逻辑, got %d (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestControlRateLimit(t *testing.T) {
	s := newTestServer()
	s.rateLimiter = newCtrlRateLimiter(3, time.Minute) // 收紧窗口便于测试
	// 前 3 次放行，第 4 次 429
	codes := []int{}
	for i := 0; i < 4; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/stop", strings.NewReader(`{"mode":"recv"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.handleStop(rec, req)
		codes = append(codes, rec.Code)
	}
	if codes[0] != http.StatusOK || codes[1] != http.StatusOK || codes[2] != http.StatusOK {
		t.Fatalf("前 3 次应放行: %v", codes)
	}
	if codes[3] != http.StatusTooManyRequests {
		t.Fatalf("第 4 次应 429: %v", codes)
	}
	// 读端点不受限流影响
	rec := httptest.NewRecorder()
	s.handleStats(rec, httptest.NewRequest(http.MethodGet, "/api/stats", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("读端点不应限流, got %d", rec.Code)
	}
}

func TestRateLimiterWindowReset(t *testing.T) {
	l := newCtrlRateLimiter(2, 50*time.Millisecond)
	now := time.Now()
	if !l.allow("1.1.1.1", now) || !l.allow("1.1.1.1", now) {
		t.Fatal("窗口内前 2 次应放行")
	}
	if l.allow("1.1.1.1", now.Add(10*time.Millisecond)) {
		t.Fatal("窗口内第 3 次应拒绝")
	}
	if !l.allow("1.1.1.1", now.Add(60*time.Millisecond)) {
		t.Fatal("新窗口应重新计数")
	}
	if !l.allow("2.2.2.2", now) {
		t.Fatal("不同 IP 互不影响")
	}
}

// TestRateLimiterTableCap 超量源 IP 灌满限流表：容量有硬上限，
// 淘汰最旧条目（而非整表重置——存留条目的计数应保持有效）。
func TestRateLimiterTableCap(t *testing.T) {
	l := newCtrlRateLimiter(5, time.Hour)
	base := time.Now()
	ip := func(i int) string {
		return "10." + strconv.Itoa(i/250) + "." + strconv.Itoa(i%250) + ".1"
	}
	for i := 0; i < rateTabMax+10; i++ {
		l.allow(ip(i), base.Add(time.Duration(i)*time.Millisecond)) // 递增 start：最旧可判定
	}
	if len(l.entries) > rateTabMax {
		t.Fatalf("限流表超上限: %d > %d", len(l.entries), rateTabMax)
	}
	if l.entries[ip(0)] != nil {
		t.Fatal("最旧条目应被淘汰")
	}
	// 存留条目计数保持：该 IP 已用 1 次（插入时），再 4 次仍放行、第 5+1 次拒绝
	keep := ip(rateTabMax + 9) // 最新插入的
	for i := 0; i < 4; i++ {
		if !l.allow(keep, base.Add(time.Hour)) {
			t.Fatalf("存留条目计数不应被重置（第 %d 次应放行）", i+2)
		}
	}
	if l.allow(keep, base.Add(time.Hour)) {
		t.Fatal("第 6 次应被限流")
	}
}

// TestTokenAtomicAccess SetToken 与门禁读取并发（-race 回归）：
// token 用 atomic.Value 存取，运行中修改令牌无数据竞争。
func TestTokenAtomicAccess(t *testing.T) {
	s := newTestServer()
	s.SetToken("t0")
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				s.SetToken("t" + strconv.Itoa(i%2))
			}
		}
	}()
	for i := 0; i < 20; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/stop", nil)
		req.RemoteAddr = "10.9.9.9:1234"
		s.checkControlGate(httptest.NewRecorder(), req) // 401/200 取决于当前令牌，只求无竞争
	}
	close(stop)
	wg.Wait()
}

func TestMetrics(t *testing.T) {
	s := newTestServer()
	injectAgg(s)
	rec := httptest.NewRecorder()
	s.handleMetrics(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/plain") {
		t.Fatalf("Content-Type = %q", ct)
	}
	body, _ := io.ReadAll(rec.Body)
	b := string(body)
	for _, want := range []string{
		"qostool_running{mode=\"bidir\"} 1",
		"qostool_live_scale",
		`qostool_tx_packets_total{flow_idx="0",flow="ef"`,
		"qostool_rx_packets_total",
		"qostool_lost_total",
		"qostool_reordered_total",
		"qostool_loss_rate",
		"# TYPE qostool_tx_bps gauge",
		"# TYPE qostool_lost_total counter",
	} {
		if !strings.Contains(b, want) {
			t.Fatalf("metrics 缺少 %q\n%s", want, b)
		}
	}
	// injectAgg 用 sendTs=0（旧版发送端模拟）→ 无时延序列
	if strings.Contains(b, "qostool_jitter_ms") {
		t.Fatalf("无时间戳数据不应输出时延序列:\n%s", b)
	}
	// POST → 405
	rec2 := httptest.NewRecorder()
	s.handleMetrics(rec2, httptest.NewRequest(http.MethodPost, "/metrics", nil))
	if rec2.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /metrics 应 405, got %d", rec2.Code)
	}
}

// TestMetricsDelaySeries 带发送时间戳的数据应输出时延/抖动/百分位序列。
func TestMetricsDelaySeries(t *testing.T) {
	cfg := &config.Config{Flows: []config.Flow{
		{Name: "ef", SrcIP: "1.1.1.1", DstIP: "2.2.2.2", SrcPort: 100, DstPort: 200, DSCP: 46, RateMbps: 1},
	}}
	s := New(map[controller.Mode]*controller.Controller{
		controller.ModeBidir: controller.New(cfg, controller.ModeBidir),
	}, map[controller.Mode]string{}, true)
	agg := stats.NewAggregator(1, 10)
	t0 := time.Now()
	agg.RecordRx(0, 500, 1, t0, t0.Add(-time.Millisecond).UnixNano())
	s.ctrl(controller.ModeBidir).SetAggregatorForTest(agg)
	rec := httptest.NewRecorder()
	s.handleMetrics(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	b := rec.Body.String()
	for _, want := range []string{"qostool_delay_avg_ms", "qostool_delay_p95_ms", "qostool_delay_p99_ms", "qostool_jitter_ms", "qostool_clock_offset_ms"} {
		if !strings.Contains(b, want) {
			t.Fatalf("带时间戳数据应输出 %s:\n%s", want, b)
		}
	}
}

// TestMetricsFamilyHeaderOnce 族级输出（v2.10.0 优化）：多流时每个指标族
// 只输出一次 HELP/TYPE 头，族内各流序列跟在其后（exposition 标准布局）。
func TestMetricsFamilyHeaderOnce(t *testing.T) {
	cfg := &config.Config{Flows: []config.Flow{
		{Name: "a", SrcIP: "1.1.1.1", DstIP: "2.2.2.2", SrcPort: 1, DstPort: 2, DSCP: 46, RateMbps: 1},
		{Name: "b", SrcIP: "1.1.1.1", DstIP: "2.2.2.2", SrcPort: 3, DstPort: 4, DSCP: 26, RateMbps: 1},
	}}
	s := New(map[controller.Mode]*controller.Controller{
		controller.ModeBidir: controller.New(cfg, controller.ModeBidir),
	}, map[controller.Mode]string{}, true)
	s.ctrl(controller.ModeBidir).SetAggregatorForTest(stats.NewAggregator(2, 10))
	rec := httptest.NewRecorder()
	s.handleMetrics(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	b := rec.Body.String()
	if n := strings.Count(b, "# TYPE qostool_tx_bps "); n != 1 {
		t.Fatalf("tx_bps 族应只有 1 个 TYPE 头, got %d", n)
	}
	if n := strings.Count(b, "qostool_tx_bps{"); n != 2 {
		t.Fatalf("tx_bps 应有 2 条序列（每流一条）, got %d", n)
	}
	if n := strings.Count(b, "# HELP qostool_lost_total "); n != 1 {
		t.Fatalf("lost_total 族应只有 1 个 HELP 头, got %d", n)
	}
}

func TestMetricsNoAggregator(t *testing.T) {
	s := newTestServer() // 未注入聚合器：仅输出 running gauge，不 panic
	rec := httptest.NewRecorder()
	s.handleMetrics(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "qostool_running") {
		t.Fatal("至少应输出 running gauge")
	}
}

func TestEscapeLabel(t *testing.T) {
	in := `a\b"c` + "\n" + "d"
	want := `a\\b\"c\nd`
	if got := escapeLabel(in); got != want {
		t.Fatalf("escapeLabel = %q, want %q", got, want)
	}
}

func TestMetricsLabelEscaping(t *testing.T) {
	cfg := &config.Config{Flows: []config.Flow{
		{Name: `坏"名\字`, SrcIP: "1.1.1.1", DstIP: "2.2.2.2", SrcPort: 100, DstPort: 200, DSCP: 46, RateMbps: 1},
	}}
	s := New(map[controller.Mode]*controller.Controller{
		controller.ModeBidir: controller.New(cfg, controller.ModeBidir),
	}, map[controller.Mode]string{}, true)
	s.ctrl(controller.ModeBidir).SetAggregatorForTest(stats.NewAggregator(1, 10))
	rec := httptest.NewRecorder()
	s.handleMetrics(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()
	if !strings.Contains(body, `flow="坏\"名\\字"`) {
		t.Fatalf("标签未正确转义:\n%s", body)
	}
}
