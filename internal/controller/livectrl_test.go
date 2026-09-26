package controller

// v2.10.0 新功能测试：运行中动态调速（ScaleRates/LiveScale）与
// 阈值越限自动停止看门狗（autoStopTriggered/SetAutoStop）。

import (
	"math"
	"testing"
	"time"

	"qostool/internal/config"
	"qostool/internal/stats"
)

// scaleTestCfg 带阈值的测试配置（阈值供看门狗判定用）。
func scaleTestCfg(port int) *config.Config {
	return &config.Config{Flows: []config.Flow{{
		Name: "a", Protocol: "udp", SrcIP: "127.0.0.1", DstIP: "127.0.0.1",
		SrcPort: port, DstPort: port + 1, DSCP: 46, RatePPS: 100, IPLen: 92,
		MaxLossRatePct: 0.5,
	}}}
}

func TestScaleRatesNotRunning(t *testing.T) {
	c := New(scaleTestCfg(13001), ModeSend)
	if _, err := c.ScaleRates(1.5); err == nil {
		t.Fatal("未运行时调速应报错")
	}
	if c.LiveScale() != 1.0 {
		t.Fatalf("失败后倍率应保持 1.0, got %v", c.LiveScale())
	}
}

func TestScaleRatesRecvMode(t *testing.T) {
	c := New(scaleTestCfg(13002), ModeRecv)
	c.SetAggregatorForTest(stats.NewAggregator(1, 10)) // 标记运行中（无发送引擎）
	if _, err := c.ScaleRates(1.5); err == nil {
		t.Fatal("接收端角色调速应报错")
	}
}

func TestScaleRatesInvalid(t *testing.T) {
	c := New(scaleTestCfg(13003), ModeSend)
	c.SetAggregatorForTest(stats.NewAggregator(1, 10))
	for _, bad := range []float64{0, -1, math.NaN(), math.Inf(1), 1001} {
		if _, err := c.ScaleRates(bad); err == nil {
			t.Fatalf("倍率 %v 应被拒绝", bad)
		}
	}
	if c.LiveScale() != 1.0 {
		t.Fatalf("全部拒绝后倍率应保持 1.0, got %v", c.LiveScale())
	}
}

func TestScaleRatesRunning(t *testing.T) {
	c := New(scaleTestCfg(13004), ModeSend) // send 模式无需抓包接口
	if err := c.Start(""); err != nil {
		t.Fatalf("send 模式启动失败: %v", err)
	}
	defer c.Stop()
	applied, err := c.ScaleRates(1.5)
	if err != nil {
		t.Fatalf("运行中调速失败: %v", err)
	}
	if applied != 1.5 || c.LiveScale() != 1.5 {
		t.Fatalf("applied=%v LiveScale=%v, want 1.5", applied, c.LiveScale())
	}
	if st := c.Status(); st.LiveScale != 1.5 {
		t.Fatalf("Status.LiveScale = %v, want 1.5", st.LiveScale)
	}
	// 降速（<1 倍率）同样合法
	if _, err := c.ScaleRates(0.2); err != nil {
		t.Fatalf("降速失败: %v", err)
	}
}

func TestAutoStopDisabledByDefault(t *testing.T) {
	c := New(scaleTestCfg(13005), ModeSend)
	c.SetLogDir(t.TempDir())
	c.SetAggregatorForTest(stats.NewAggregator(1, 10))
	// 越限快照（丢包率 50% > 0.5% 阈值），但未启用自动停止 → 不触发
	snap := []stats.FlowSnapshot{{FlowIdx: 0, RxPackets: 100, LossRate: 0.5}}
	now := time.Now()
	if c.autoStopTriggered(snap, now) {
		t.Fatal("未启用自动停止不应触发")
	}
	if c.autoStopTriggered(snap, now.Add(time.Hour)) {
		t.Fatal("未启用自动停止不应触发")
	}
}

func TestAutoStopWatchdog(t *testing.T) {
	c := New(scaleTestCfg(13006), ModeSend)
	c.SetLogDir(t.TempDir())
	c.SetAggregatorForTest(stats.NewAggregator(1, 10))
	c.SetAutoStop(time.Hour)
	snap := []stats.FlowSnapshot{{FlowIdx: 0, RxPackets: 100, LossRate: 0.5}}
	now := time.Now()
	// 首次越限：记录起点，不触发
	if c.autoStopTriggered(snap, now) {
		t.Fatal("首次越限不应立即触发")
	}
	// 未越限：清零连续计时
	if c.autoStopTriggered([]stats.FlowSnapshot{{FlowIdx: 0, RxPackets: 100}}, now.Add(time.Second)) {
		t.Fatal("未越限不应触发")
	}
	// 重新越限 + 持续超过时长：触发
	if c.autoStopTriggered(snap, now.Add(2*time.Second)) {
		t.Fatal("重新越限应重置起点")
	}
	if !c.autoStopTriggered(snap, now.Add(2*time.Second).Add(2*time.Hour)) {
		t.Fatal("持续越限超过时长应触发")
	}
	// 异步 Stop 完成：运行态应结束且原因非空
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !c.Status().Running {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if c.Status().Running {
		t.Fatal("触发后应已停止")
	}
	if c.Status().StopReason == "" {
		t.Fatal("自动停止原因不应为空")
	}
}

func TestAutoStopResetOnStart(t *testing.T) {
	c := New(scaleTestCfg(13007), ModeSend)
	if err := c.Start(""); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	defer c.Stop()
	// 模拟上一轮残留的自动停止原因：新一轮 Start 后必须清空
	c.mu.Lock()
	c.stopReason = "残留原因"
	c.mu.Unlock()
	c.Stop()
	if c.Status().StopReason == "" {
		t.Fatal("停止后原因应保留（供 CLI 读取）")
	}
	if err := c.Start(""); err != nil {
		t.Fatalf("重新启动失败: %v", err)
	}
	defer c.Stop()
	if c.Status().StopReason != "" {
		t.Fatalf("新一轮不应继承停止原因: %q", c.Status().StopReason)
	}
}

func TestAutoStopDurationFromConfig(t *testing.T) {
	c := New(&config.Config{AutoStopSec: 30, Flows: scaleTestCfg(13008).Flows}, ModeRecv)
	if c.autoStopDur != 30*time.Second {
		t.Fatalf("autoStopDur = %v, want 30s", c.autoStopDur)
	}
	// UpdateConfig 即时生效
	cfg2 := scaleTestCfg(13008)
	cfg2.AutoStopSec = 0.5
	if _, err := c.UpdateConfig(cfg2); err != nil {
		t.Fatal(err)
	}
	if c.autoStopDur != 500*time.Millisecond {
		t.Fatalf("UpdateConfig 后 autoStopDur = %v, want 500ms", c.autoStopDur)
	}
	// 非法值（负数）在 config 校验拦截；构造层非法值视为禁用
	if autoStopDuration(-1) != 0 {
		t.Fatal("负值应视为禁用")
	}
}
