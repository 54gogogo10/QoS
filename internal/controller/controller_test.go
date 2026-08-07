package controller

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"qostool/internal/config"
	"qostool/internal/stats"
)

func testCfg() *config.Config {
	return &config.Config{Flows: []config.Flow{
		{Name: "a", Protocol: "udp", SrcIP: "127.0.0.1", DstIP: "127.0.0.1",
			SrcPort: 1, DstPort: 2, DSCP: 46, RatePPS: 100, IPLen: 92},
	}}
}

// cfgWithPort 用指定源端口构造配置（避免测试间端口重用冲突）。
func cfgWithPort(port int) *config.Config {
	return &config.Config{Flows: []config.Flow{
		{Name: "a", Protocol: "udp", SrcIP: "127.0.0.1", DstIP: "127.0.0.1",
			SrcPort: port, DstPort: port + 1, DSCP: 46, RatePPS: 100, IPLen: 92},
	}}
}

func TestApplyRemoteTX(t *testing.T) {
	c := New(testCfg(), ModeRecv)
	snap := []stats.FlowSnapshot{{FlowIdx: 0}, {FlowIdx: 1}}
	if c.applyRemoteTX(snap) {
		t.Fatal("未设置 provider 时不应应用")
	}
	pps := []float64{10, 20}
	pkts := []uint64{100, 200}
	bytes := []uint64{5000, 6000}
	c.SetRemoteTXProvider(func() ([]float64, []uint64, []uint64, bool) {
		return pps, pkts, bytes, true
	})
	if !c.applyRemoteTX(snap) {
		t.Fatal("设置 provider 后应应用")
	}
	if snap[0].TxPps != 10 || snap[1].TxPps != 20 {
		t.Fatalf("TxPps 未覆盖: %+v", snap)
	}
	if snap[0].TxPackets != 100 || snap[1].TxPackets != 200 {
		t.Fatalf("TxPackets 未覆盖: %+v", snap)
	}
	if snap[0].TxBytes != 5000 || snap[1].TxBytes != 6000 {
		t.Fatalf("TxBytes 未覆盖: %+v", snap)
	}
	// 远端 ok=false（发送端离线）时不应用，TX 保持本端 0
	c.SetRemoteTXProvider(func() ([]float64, []uint64, []uint64, bool) {
		return nil, nil, nil, false
	})
	if c.applyRemoteTX(snap) {
		t.Fatal("ok=false 不应应用")
	}
}

func TestStartBadIface(t *testing.T) {
	c := New(testCfg(), ModeBidir)
	if err := c.Start("__no_such_iface__"); err == nil {
		t.Fatal("坏接口应报错")
	}
	if c.Status().Running {
		t.Fatal("失败后不应处于运行状态")
	}
	if c.Aggregator() != nil {
		t.Fatal("失败后聚合器应为 nil")
	}
}

func TestStopIdempotent(t *testing.T) {
	c := New(testCfg(), ModeBidir)
	c.Stop() // 未运行，应无操作不 panic
	if c.Status().Running {
		t.Fatal("不应运行")
	}
}

func TestSendModeStartStop(t *testing.T) {
	c := New(cfgWithPort(11001), ModeSend) // send 模式不需要接口
	if err := c.Start(""); err != nil {
		t.Fatalf("send 模式启动失败: %v", err)
	}
	if !c.Status().Running {
		t.Fatal("应处于运行状态")
	}
	if c.Aggregator() == nil {
		t.Fatal("应有聚合器")
	}
	c.Stop()
	if c.Status().Running {
		t.Fatal("停止后不应运行")
	}
	if c.Aggregator() == nil {
		t.Fatal("停止后聚合器应保留（供页面查看上次数据）")
	}
}

func TestDoubleStart(t *testing.T) {
	c := New(cfgWithPort(11002), ModeSend)
	if err := c.Start(""); err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	if err := c.Start(""); err == nil {
		t.Fatal("重复启动应报错")
	}
}

func TestUpdateConfigNotRunning(t *testing.T) {
	c := New(testCfg(), ModeBidir)
	cfg2 := testCfg()
	cfg2.Flows[0].RatePPS = 200
	restarted, err := c.UpdateConfig(cfg2)
	if err != nil || restarted {
		t.Fatalf("未运行时应不重启: restarted=%v err=%v", restarted, err)
	}
	if c.Config().Flows[0].RatePPS != 200 {
		t.Fatal("配置未更新")
	}
}

func TestRestartNotRunning(t *testing.T) {
	c := New(testCfg(), ModeBidir)
	if err := c.Restart(); err != nil {
		t.Fatalf("未运行时 Restart 应无操作: %v", err)
	}
}

// TestLogAndDataRetention 验证：停止后数据保留（页面可继续查看）+ 日志落盘。
func TestLogAndDataRetention(t *testing.T) {
	logDir := t.TempDir()
	c := New(cfgWithPort(12001), ModeSend)
	c.SetLogDir(logDir)
	if err := c.Start(""); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1300 * time.Millisecond) // ≥10 次采样，写 1 行日志
	c.Stop()

	// 1. 停止后聚合器保留（页面数据不消失）
	agg := c.Aggregator()
	if agg == nil {
		t.Fatal("停止后聚合器应为保留（非 nil）")
	}
	// 2. CSV 日志存在且含表头 + 数据行
	matches, _ := filepath.Glob(filepath.Join(logDir, "logs", "qostool_*.csv"))
	if len(matches) != 1 {
		t.Fatalf("CSV 日志 = %v, want 1 个", matches)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 2 {
		t.Fatalf("CSV 应含表头+数据行, got %d 行", len(lines))
	}
	if !strings.Contains(lines[0], "time") || !strings.Contains(lines[0], "tx_pps_1") {
		t.Fatalf("CSV 表头错误: %s", lines[0])
	}
	// 3. 汇总 txt 存在
	sums, _ := filepath.Glob(filepath.Join(logDir, "logs", "qostool_*_summary.txt"))
	if len(sums) != 1 {
		t.Fatalf("汇总文件 = %v, want 1 个", sums)
	}
	sumData, err := os.ReadFile(sums[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sumData), "qostool 运行汇总") {
		t.Fatalf("汇总内容错误: %s", sumData)
	}
}

// TestStopGeneratesHTMLReport 验证停止后自动生成 HTML 报告并记录路径。
func TestStopGeneratesHTMLReport(t *testing.T) {
	dir := t.TempDir()
	cfg := testCfg()
	cfg.Flows[0].MaxLossRatePct = 0.5
	c := New(cfg, ModeBidir)
	c.SetLogDir(dir)
	agg := stats.NewAggregator(1, 10)
	agg.RecordTx(0, 100, 10000)
	agg.RecordRx(0, 100, 100, time.Now(), time.Now().Add(-time.Millisecond).UnixNano())
	c.SetAggregatorForTest(agg)
	c.Stop()
	if !strings.HasSuffix(c.LastReportPath(), ".html") {
		t.Fatalf("LastReportPath = %q, want html", c.LastReportPath())
	}
	if _, err := os.Stat(c.LastReportPath()); err != nil {
		t.Fatalf("报告文件不存在: %v", err)
	}
	data, _ := os.ReadFile(c.LastReportPath())
	if !strings.Contains(string(data), "FAIL") {
		t.Fatal("1%% 丢包超 0.5%% 阈值应生成 FAIL 报告")
	}
}
