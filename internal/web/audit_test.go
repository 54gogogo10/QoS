package web

// 代码审计回归测试：注册表加固、对端地址校验、配置更新窗口的流数不一致防护、
// lastIface 读写竞态（配合 -race 检测器）。

import (
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

// TestRecordReceiverPortValidation 注册端口必须是 1-65535 数字（防伪造 key 灌注册表）。
func TestRecordReceiverPortValidation(t *testing.T) {
	s := newTestServer()
	// 合法端口（空=默认 16666）应注册成功
	for _, port := range []string{"16666", "1", "65535", ""} {
		req := httptest.NewRequest("GET", "/api/tx_stats?port="+port, nil)
		req.RemoteAddr = "192.0.2.10:5555"
		s.recordReceiver(req)
	}
	for _, key := range []string{"192.0.2.10:16666", "192.0.2.10:1", "192.0.2.10:65535"} {
		if s.receivers[key] == nil {
			t.Fatalf("合法端口 %q 应注册成功", key)
		}
	}
	// 非法端口全部被拒：注册表数量不变
	before := len(s.receivers)
	for _, port := range []string{"0", "65536", "abc", "16666-x", "<script>"} {
		req := httptest.NewRequest("GET", "/api/tx_stats?port="+port, nil)
		req.RemoteAddr = "192.0.2.10:5555"
		s.recordReceiver(req)
	}
	if len(s.receivers) != before {
		t.Fatalf("非法端口不应注册: %v", s.receivers)
	}
}

// TestRecordReceiverCap 注册表容量有上限（防内存无限增长）。
func TestRecordReceiverCap(t *testing.T) {
	s := newTestServer()
	for i := 0; i < maxReceivers+50; i++ {
		req := httptest.NewRequest("GET", "/api/tx_stats?port="+strconv.Itoa(10000+i), nil)
		req.RemoteAddr = "192.0.2.10:5555"
		s.recordReceiver(req)
	}
	if len(s.receivers) > maxReceivers {
		t.Fatalf("注册表超上限: %d > %d", len(s.receivers), maxReceivers)
	}
	// 最近注册的仍在（淘汰的是最旧的）
	if s.receivers["192.0.2.10:" + strconv.Itoa(10000+maxReceivers+49)] == nil {
		t.Fatal("最新注册的接收端不应被淘汰")
	}
}

// TestValidPeerAddr 对端地址必须是 host:port（数字端口），供通知类请求拼接 URL。
func TestValidPeerAddr(t *testing.T) {
	ok := []string{"192.168.1.10:16666", "[::1]:16666", "host.example:8080"}
	bad := []string{"", "192.168.1.10", "http://evil/x", "192.168.1.10:0", "192.168.1.10:70000",
		"192.168.1.10:abc", "192.168.1.10:16666/path"}
	for _, a := range ok {
		if !validPeerAddr(a) {
			t.Fatalf("%q 应为合法地址", a)
		}
	}
	for _, a := range bad {
		if validPeerAddr(a) {
			t.Fatalf("%q 应为非法地址", a)
		}
	}
}

// TestNotifyPeerBadAddr 非法地址直接返回错误信息，不发起网络请求。
func TestNotifyPeerBadAddr(t *testing.T) {
	for _, addr := range []string{"http://evil/x", "1.2.3.4:abc"} {
		if msg := NotifyPeerListen(addr); msg == "" {
			t.Fatalf("%q: 期望返回错误信息", addr)
		}
		if msg := NotifyPeerStop(addr); msg == "" {
			t.Fatalf("%q: 期望返回错误信息", addr)
		}
	}
}

// TestStatsFlowCountMismatch 配置更新窗口：聚合器流数与配置不一致时 stats 不越界。
func TestStatsFlowCountMismatch(t *testing.T) {
	cfg := &config.Config{Flows: []config.Flow{
		{Name: "a", SrcIP: "1.1.1.1", DstIP: "2.2.2.2", SrcPort: 1, DstPort: 2, DSCP: 46, RateMbps: 1},
		{Name: "b", SrcIP: "1.1.1.1", DstIP: "2.2.2.2", SrcPort: 3, DstPort: 4, DSCP: 26, RateMbps: 1},
	}}
	s := New(map[controller.Mode]*controller.Controller{
		controller.ModeBidir: controller.New(cfg, controller.ModeBidir),
	}, map[controller.Mode]string{}, true)
	// 聚合器只有 1 条流（上一轮运行的残留）
	s.ctrl(controller.ModeBidir).SetAggregatorForTest(stats.NewAggregator(1, 10))
	rec := httptest.NewRecorder()
	s.handleStats(rec, httptest.NewRequest("GET", "/api/stats", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body=%s", rec.Code, rec.Body.String())
	}
}

// TestLastIfaceRace handleStart 读取 lastIface 必须持锁（-race 下回归）。
// 写者持续改 lastIface，读者并发走 handleStart 的诊断日志读取路径；
// 两者间不建立同步边（不 join），保证 -race 能捕获无锁读写。
// 读者用环回配置 + 非环回接口触发诊断快速返回（不进入真正的启动重试）。
func TestLastIfaceRace(t *testing.T) {
	cfg := &config.Config{Flows: []config.Flow{{
		Name: "ef", Protocol: "udp",
		SrcIP: "127.0.0.1", DstIP: "127.0.0.1",
		SrcPort: 1, DstPort: 2, DSCP: 46, RateMbps: 1, IPLen: 92,
	}}}
	s := New(map[controller.Mode]*controller.Controller{
		controller.ModeRecv: controller.New(cfg, controller.ModeRecv),
	}, map[controller.Mode]string{}, true)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for w := 0; w < 2; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				req := httptest.NewRequest("POST", "/api/iface",
					strings.NewReader(`{"iface":"eth`+strconv.Itoa(i%8)+`"}`))
				req.Header.Set("Content-Type", "application/json")
				s.handleIfaceSel(httptest.NewRecorder(), req)
			}
		}()
	}
	time.Sleep(20 * time.Millisecond) // 让写者先跑起来（不 join：保持无同步边）
	for i := 0; i < 20; i++ {
		req := httptest.NewRequest("POST", "/api/start", strings.NewReader(`{"mode":"recv","iface":"wan0"}`))
		req.Header.Set("Content-Type", "application/json")
		s.handleStart(httptest.NewRecorder(), req)
	}
	close(stop)
	wg.Wait()
}
