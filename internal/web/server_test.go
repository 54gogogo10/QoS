package web

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"qostool/internal/config"
	"qostool/internal/controller"
	"qostool/internal/stats"
)

func newTestServer() *Server {
	cfg := &config.Config{Flows: []config.Flow{
		{Name: "ef", SrcIP: "1.1.1.1", DstIP: "2.2.2.2", SrcPort: 100, DstPort: 200, DSCP: 46,
			RateMbps: 1, IPLen: 92},
	}}
	ctrls := map[controller.Mode]*controller.Controller{
		controller.ModeSend:  controller.New(cfg, controller.ModeSend),
		controller.ModeRecv:  controller.New(cfg, controller.ModeRecv),
		controller.ModeBidir: controller.New(cfg, controller.ModeBidir),
	}
	return New(ctrls, map[controller.Mode]string{}, true)
}

// injectAgg 把预置统计的聚合器注入控制器（不启动真实测试）。
func injectAgg(s *Server) {
	agg := stats.NewAggregator(1, 10)
	t0 := time.Now()
	agg.RecordTx(0, 100, 1000)
	agg.RecordRx(0, 500, 1, t0, 0)
	agg.RecordRx(0, 500, 5, t0.Add(time.Millisecond), 0) // 空洞 2,3,4
	agg.Snapshot(t0.Add(time.Second))                     // 超过乱序宽限期 → 结算为丢包
	s.ctrl(controller.ModeBidir).SetAggregatorForTest(agg)
}

func TestAPIStats(t *testing.T) {
	s := newTestServer()
	injectAgg(s)
	rec := httptest.NewRecorder()
	s.handleStats(rec, httptest.NewRequest("GET", "/api/stats", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	var out apiStats
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Flows) != 1 {
		t.Fatalf("flows = %d", len(out.Flows))
	}
	f := out.Flows[0]
	if f.Name != "ef" || f.DSCP != 46 || f.SrcPort != 100 || f.DstPort != 200 ||
		f.TxPackets != 100 || f.TxBytes != 1000 || f.DstIP != "2.2.2.2" ||
		f.RxPackets != 2 || f.RxBytes != 1000 || f.Lost != 3 ||
		math.Abs(f.LossRate-0.6) >= 1e-9 {
		t.Fatalf("flow 字段错误: %+v", f)
	}
	if len(out.History.T) != 1 || len(out.History.Tx) != 1 || len(out.History.Tx[0]) != 1 {
		t.Fatalf("history 错误: %+v", out.History)
	}
	if !out.Running {
		t.Fatal("running 应为 true（聚合器存在）")
	}
}

func TestAPIStatsNotRunning(t *testing.T) {
	cfg := &config.Config{Flows: []config.Flow{
		{Name: "ef", SrcIP: "1.1.1.1", DstIP: "2.2.2.2", SrcPort: 100, DstPort: 200, DSCP: 46, RateMbps: 1},
	}}
	s := New(map[controller.Mode]*controller.Controller{
		controller.ModeBidir: controller.New(cfg, controller.ModeBidir),
	}, map[controller.Mode]string{}, true)
	rec := httptest.NewRecorder()
	s.handleStats(rec, httptest.NewRequest("GET", "/api/stats", nil))
	var out apiStats
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Running || len(out.Flows) != 0 {
		t.Fatalf("未运行时应返回空 flows: running=%v flows=%d", out.Running, len(out.Flows))
	}
}

func TestAPIConfigRoundTrip(t *testing.T) {
	s := newTestServer()
	// GET 当前配置
	rec := httptest.NewRecorder()
	s.handleConfig(rec, httptest.NewRequest("GET", "/api/config", nil))
	var out apiConfig
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Flows) != 1 || out.Flows[0].DSCP != "EF" {
		t.Fatalf("配置错误: %+v", out)
	}
	// POST 修改配置（DSCP 用名字，验证解析）
	body := `{"flows":[{"name":"新流","protocol":"udp","src_ip":"10.0.0.1","dst_ip":"10.0.0.2",
		"src_port":1111,"dst_port":2222,"dscp":"AF41","rate_mbps":5,"rate_pps":0,"ip_len":128}]}`
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("POST", "/api/config", strings.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	s.handleConfig(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("POST 配置 code = %d, body=%s", rec2.Code, rec2.Body.String())
	}
	cfg := s.ctrl(controller.ModeBidir).Config()
	if len(cfg.Flows) != 1 || int(cfg.Flows[0].DSCP) != 34 || cfg.Flows[0].Name != "新流" {
		t.Fatalf("配置未生效: %+v", cfg.Flows)
	}
}

func TestAPIConfigValidation(t *testing.T) {
	s := newTestServer()
	cases := []string{
		`{"flows":[]}`,
		`{"flows":[{"name":"a","protocol":"udp","src_ip":"1.1.1.1","dst_ip":"2.2.2.2","src_port":1,"dst_port":2,"dscp":"BAD","rate_pps":100,"ip_len":92}]}`,
		`{"flows":[{"name":"a","protocol":"tcp","src_ip":"1.1.1.1","dst_ip":"2.2.2.2","src_port":1,"dst_port":2,"dscp":"0","rate_pps":100,"ip_len":92}]}`,
	}
	for i, body := range cases {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/config", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		s.handleConfig(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("case %d: code = %d, want 400 (body=%s)", i, rec.Code, body)
		}
	}
}

func TestControlDisabledInCLIMode(t *testing.T) {
	cfg := &config.Config{Flows: []config.Flow{
		{Name: "ef", SrcIP: "1.1.1.1", DstIP: "2.2.2.2", SrcPort: 100, DstPort: 200, DSCP: 46, RateMbps: 1},
	}}
	s := New(map[controller.Mode]*controller.Controller{
		controller.ModeBidir: controller.New(cfg, controller.ModeBidir),
	}, map[controller.Mode]string{}, false) // CLI 模式
	rec := httptest.NewRecorder()
	s.handleStart(rec, httptest.NewRequest("POST", "/api/start", strings.NewReader(`{"iface":"x"}`)))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("CLI 模式下 start 应返回 403, got %d", rec.Code)
	}
	rec2 := httptest.NewRecorder()
	s.handleConfig(rec2, httptest.NewRequest("POST", "/api/config", strings.NewReader(`{"flows":[]}`)))
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("CLI 模式下 POST config 应返回 403, got %d", rec2.Code)
	}
}

func TestStartBadIface(t *testing.T) {
	s := newTestServer()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/start", strings.NewReader(`{"iface":"不存在的接口名"}`))
	req.Header.Set("Content-Type", "application/json")
	s.handleStart(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("start 坏接口 code = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestIndexPage(t *testing.T) {
	s := newTestServer()
	rec := httptest.NewRecorder()
	s.handleIndex(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "canvas") || !strings.Contains(body, "api/stats") ||
		!strings.Contains(body, "开始测试") {
		t.Fatal("页面缺少关键元素")
	}
}

// TestAPIStatsDelayAndVerdict 验证 stats API 的时延字段与阈值判定。
func TestAPIStatsDelayAndVerdict(t *testing.T) {
	s := newTestServer()
	// 带阈值 + 时延数据的聚合器：1% 丢包超 0.5% → FAIL
	cfg := s.ctrl(controller.ModeBidir).Config()
	cfg.Flows[0].MaxLossRatePct = 0.5
	agg := stats.NewAggregator(1, 10)
	now := time.Now()
	agg.RecordTx(0, 100, 1000)
	agg.RecordRx(0, 500, 1, now, now.Add(-5*time.Millisecond).UnixNano())
	agg.RecordRx(0, 500, 5, now, now.Add(-5*time.Millisecond).UnixNano()) // seq 2-4 丢失 → 60% 丢包
	agg.Snapshot(now.Add(time.Second)) // 超过乱序宽限期：空洞结算为丢包；窗口清零
	s.ctrl(controller.ModeBidir).SetAggregatorForTest(agg)

	rec := httptest.NewRecorder()
	s.handleStats(rec, httptest.NewRequest("GET", "/api/stats", nil))
	var out apiStats
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	f := out.Flows[0]
	// Snapshot 已清零窗口：窗口 avg=0；v2.8.1 单包 5ms 经时钟偏差校正后为 0，偏差估计=5ms
	if !f.DelayValid || math.Abs(f.DelayTotAvgMs-0) > 1e-6 || math.Abs(f.DelayMaxMs-0) > 1e-6 || math.Abs(f.ClockOffsetMs-5) > 1e-6 {
		t.Fatalf("时延字段错误: %+v", f)
	}
	if f.Verdict != "fail" {
		t.Fatalf("verdict = %q, want fail", f.Verdict)
	}
	if len(out.History.Dly) != 1 || len(out.History.Dly[0]) != 1 {
		t.Fatalf("history.dly 错误: %+v", out.History.Dly)
	}
}

func TestAPIReport(t *testing.T) {
	s := newTestServer()
	s.ctrl(controller.ModeBidir).SetLogDir(t.TempDir())
	injectAgg(s)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/report", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	s.handleReport(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		OK   bool   `json:"ok"`
		Path string `json:"path"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || !strings.HasSuffix(out.Path, ".html") {
		t.Fatalf("report 响应错误: %+v", out)
	}
	if _, err := os.Stat(out.Path); err != nil {
		t.Fatalf("报告文件不存在: %v", err)
	}
	// CLI 模式应拒绝
	s2 := New(map[controller.Mode]*controller.Controller{
		controller.ModeBidir: controller.New(s.ctrl(controller.ModeBidir).Config(), controller.ModeBidir),
	}, map[controller.Mode]string{}, false)
	rec2 := httptest.NewRecorder()
	s2.handleReport(rec2, httptest.NewRequest("POST", "/api/report", nil))
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("CLI 模式 report 应 403, got %d", rec2.Code)
	}
}

// TestAPIReportRequiresJSONContentTy 回归（v2.9.0 安全加固）：
// /api/report 是控制类写操作，必须校验 Content-Type=JSON（防跨站表单 CSRF 刷报告文件）。
func TestAPIReportRequiresJSONContentType(t *testing.T) {
	s := newTestServer()
	s.ctrl(controller.ModeBidir).SetLogDir(t.TempDir())
	injectAgg(s)
	// 无 Content-Type（跨站表单可达）：拒绝
	rec := httptest.NewRecorder()
	s.handleReport(rec, httptest.NewRequest("POST", "/api/report", strings.NewReader("{}")))
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("无 JSON Content-Type 应 415, got %d", rec.Code)
	}
	// 表单 Content-Type：拒绝
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("POST", "/api/report", strings.NewReader("a=1"))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.handleReport(rec2, req2)
	if rec2.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("表单 Content-Type 应 415, got %d", rec2.Code)
	}
}

// TestAPIRemoteAddrValidation 回归（v2.9.0 安全加固）：
// /api/remote 的地址必须为合法 host:port，拒绝任意字符串（防注入 URL）。
func TestAPIRemoteAddrValidation(t *testing.T) {
	s := newTestServer()
	for _, bad := range []string{"javascript:alert(1)", "http://evil/x", "1.2.3.4:http", "[::1", "host:0", "host:70000"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/remote", strings.NewReader(`{"addr":"`+bad+`"}`))
		req.Header.Set("Content-Type", "application/json")
		s.handleRemote(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("addr %q 应 400, got %d", bad, rec.Code)
		}
	}
	// 合法地址（自动补端口 + 显式端口）应 200
	for _, ok := range []string{"192.168.1.10", "192.168.1.10:16666", "[::1]:16666"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/remote", strings.NewReader(`{"addr":"`+ok+`"}`))
		req.Header.Set("Content-Type", "application/json")
		s.handleRemote(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("addr %q 应 200, got %d: %s", ok, rec.Code, rec.Body.String())
		}
	}
}
