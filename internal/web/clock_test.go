package web

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"qostool/internal/config"
	"qostool/internal/controller"
)

// TestClockMeasurementEndToEnd 起两个 Server 实例模拟双机（发送端 + 接收端设 remote），
// 验证控制通道时钟测量产生样本（v2.8.1）。
func TestClockMeasurementEndToEnd(t *testing.T) {
	// 发送端实例
	sendCfg := &config.Config{Flows: []config.Flow{{Name: "f", SrcIP: "1.1.1.1", DstIP: "2.2.2.2", SrcPort: 1, DstPort: 2, DSCP: 0, RatePPS: 100, IPLen: 92}}}
	send := New(map[controller.Mode]*controller.Controller{
		controller.ModeSend: controller.New(sendCfg, controller.ModeSend),
	}, map[controller.Mode]string{}, true)
	// 先占一个空闲端口再释放，交给 ListenAndServe 自己监听（其内部按 Addr 重新 listen）
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	go send.ListenAndServe(addr)
	time.Sleep(200 * time.Millisecond) // 等服务就绪

	// 接收端实例（CLI 场景：recv 也起自己的 web 服务，与发送端同机时端口不同）
	recvCfg := &config.Config{Flows: []config.Flow{{Name: "f", SrcIP: "1.1.1.1", DstIP: "2.2.2.2", SrcPort: 1, DstPort: 2, DSCP: 0}}}
	recv := New(map[controller.Mode]*controller.Controller{
		controller.ModeRecv: controller.New(recvCfg, controller.ModeRecv),
	}, map[controller.Mode]string{}, true)
	ln2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	recvAddr := ln2.Addr().String()
	ln2.Close()

	// 完全复现 runCLI 时序：SetRemote 在 go ListenAndServe 之前
	recv.SetRemote(addr)
	go recv.ListenAndServe(recvAddr)
	time.Sleep(200 * time.Millisecond)

	// 直接调用，暴露错误
	if off, err := recv.queryRemoteTime(ln.Addr().String()); err != nil {
		t.Logf("queryRemoteTime 失败: %v", err)
		// 二分：原生 http.Get 是否成功？
		client := &http.Client{Timeout: 2 * time.Second}
		resp, err2 := client.Get("http://" + ln.Addr().String() + "/api/time")
		if err2 != nil {
			t.Fatalf("原生 http.Get 也失败: %v", err2)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		t.Logf("原生 http.Get 成功: %s", string(b))
	} else {
		t.Logf("queryRemoteTime 直接调用成功: %d", off)
	}

	recv.SetRemote(addr)
	// 纯观察：不手动调用，只看 goroutine（立即调用 + ticker）是否持续产生样本
	time.Sleep(1200 * time.Millisecond)
	n1 := recv.clockEst.Samples()
	time.Sleep(1500 * time.Millisecond)
	n2 := recv.clockEst.Samples()
	t.Logf("1.2s 后 Samples=%d，再过 1.5s 后 Samples=%d", n1, n2)
	if n2 >= 2 {
		if off, rtt, ok := recv.ClockOffsetNs(); ok {
			t.Logf("时钟偏差估计: %d ns, RTT %d ns（样本 %d）", off, rtt, n2)
			return
		}
	}
	t.Fatalf("ticker 未产生样本（Samples=%d）", n2)
}

// TestTimeEndpoint 直接请求 /api/time 端点。
func TestTimeEndpoint(t *testing.T) {
	s := newTestServer()
	rec := httptest.NewRecorder()
	s.handleTime(rec, httptest.NewRequest("GET", "/api/time", nil))
	if rec.Code != 200 {
		t.Fatalf("code = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "server_time_ns") {
		t.Fatalf("响应缺少 server_time_ns: %s", body)
	}
}

// TestRemoteSnapshotClockFieldsAlwaysPresent 回归（v2.9.0 审计修复）：
// 偏差恰为 0.0 时 clock_offset_ms/clock_rtt_ms 不能被 omitempty 省略，
// 否则前端 remote.clock_offset_ms.toFixed() 抛 TypeError。
func TestRemoteSnapshotClockFieldsAlwaysPresent(t *testing.T) {
	b, err := json.Marshal(&remoteSnapshot{Online: true, ClockOffsetMs: 0, ClockRTTMs: 0, ClockSamples: 2})
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{`"clock_offset_ms":0`, `"clock_rtt_ms":0`, `"clock_samples":2`} {
		if !strings.Contains(s, want) {
			t.Fatalf("快照 JSON 缺少 %q: %s", want, s)
		}
	}
}
