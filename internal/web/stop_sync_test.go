package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"qostool/internal/config"
	"qostool/internal/controller"
	"qostool/internal/stats"
)

// TestStopNotifiesReceivers 发送端停止时同步通知本次勾选的接收端停止监听（v2.8.1）。
func TestStopNotifiesReceivers(t *testing.T) {
	// fake 接收端：记录收到的 /api/start 与 /api/stop
	var gotStop bool
	var stopBody string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/start"):
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(r.URL.Path, "/api/stop"):
			gotStop = true
			stopBody = string(b)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer fake.Close()
	addr := strings.TrimPrefix(fake.URL, "http://")

	s := newTestServer()
	// newTestServer 的流指向 1.1.1.1（send 角色真实启动会 bind 失败），
	// 本测试需要发送端真实启动：改用环回配置自建 server
	cfg := &config.Config{Flows: []config.Flow{{
		Name: "ef", Protocol: "udp",
		SrcIP: "127.0.0.1", DstIP: "127.0.0.1",
		SrcPort: 0, DstPort: 9, DSCP: 46, RateMbps: 1, IPLen: 92,
	}}}
	s = New(map[controller.Mode]*controller.Controller{
		controller.ModeSend: controller.New(cfg, controller.ModeSend),
	}, map[controller.Mode]string{}, true)
	// 发送端启动并勾选该接收端
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/start",
		strings.NewReader(`{"mode":"send","receivers":["`+addr+`"]}`))
	req.Header.Set("Content-Type", "application/json")
	s.handleStart(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("start code = %d, body=%s", rec.Code, rec.Body.String())
	}

	// 发送端停止 → 应通知接收端停止
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("POST", "/api/stop", strings.NewReader(`{"mode":"send"}`))
	req2.Header.Set("Content-Type", "application/json")
	s.handleStop(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("stop code = %d, body=%s", rec2.Code, rec2.Body.String())
	}
	if !gotStop {
		t.Fatal("发送端停止时应通知接收端停止监听")
	}
	if !strings.Contains(stopBody, `"remote":true`) {
		t.Fatalf("停止通知应带 remote=true 标记（接收端据此延迟排空）: %s", stopBody)
	}
	var resp struct {
		Message string `json:"message"`
	}
	json.Unmarshal(rec2.Body.Bytes(), &resp)
	if !strings.Contains(resp.Message, "已同步") {
		t.Fatalf("停止响应应提示已同步接收端: %s", resp.Message)
	}
}

// TestRemoteStopDelayed 接收端被发送端同步停止：延迟排空在途包后再停（v2.8.1）。
func TestRemoteStopDelayed(t *testing.T) {
	s := newTestServer()
	ctrl := s.ctrl(controller.ModeRecv)
	ctrl.SetAggregatorForTest(stats.NewAggregator(1, 10)) // 标记运行中

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/stop", strings.NewReader(`{"mode":"recv","remote":true}`))
	req.Header.Set("Content-Type", "application/json")
	s.handleStop(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	// 立即：尚未停止（延迟排空在途包）
	if !ctrl.Status().Running {
		t.Fatal("远程停止应延迟执行（立即不应停止）")
	}
	// 延迟后：已停止
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !ctrl.Status().Running {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("远程停止未在延迟后执行")
}

// TestRemoteStopRaceGuard 延迟停止期间手动重启/停止：延迟停止不再动作（幂等/不误停新会话）。
func TestRemoteStopRaceGuard(t *testing.T) {
	s := newTestServer()
	ctrl := s.ctrl(controller.ModeRecv)
	ctrl.SetAggregatorForTest(stats.NewAggregator(1, 10))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/stop", strings.NewReader(`{"mode":"recv","remote":true}`))
	req.Header.Set("Content-Type", "application/json")
	s.handleStop(rec, req)
	// 延迟窗口内手动停止（Started 变化 → 延迟停止应跳过）
	ctrl.Stop()
	time.Sleep(remoteStopDelay + 300*time.Millisecond)
	// 不 panic、状态一致即可（Stop 幂等）
	if ctrl.Status().Running {
		t.Fatal("手动停止后不应运行")
	}
}
