package controller

import (
	"testing"

	"qostool/internal/config"
)

func testCfg() *config.Config {
	return &config.Config{Flows: []config.Flow{
		{Name: "a", Protocol: "udp", SrcIP: "127.0.0.1", DstIP: "127.0.0.1",
			SrcPort: 1, DstPort: 2, DSCP: 46, RatePPS: 100, PayloadSize: 64},
	}}
}

// cfgWithPort 用指定源端口构造配置（避免测试间端口重用冲突）。
func cfgWithPort(port int) *config.Config {
	return &config.Config{Flows: []config.Flow{
		{Name: "a", Protocol: "udp", SrcIP: "127.0.0.1", DstIP: "127.0.0.1",
			SrcPort: port, DstPort: port + 1, DSCP: 46, RatePPS: 100, PayloadSize: 64},
	}}
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
	if c.Aggregator() != nil {
		t.Fatal("停止后聚合器应清空")
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
