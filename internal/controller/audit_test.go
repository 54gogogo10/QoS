package controller

// 代码审计回归测试：Start/Stop/Restart 全程串行化（lmu）后，
// 并发启停不产生双份 goroutine、不误关新一轮日志，状态保持一致。

import (
	"sync"
	"testing"
	"time"
)

// TestConcurrentStartStopLifecycle 并发 Start/Stop/Status/Aggregator 交错：
// -race 下无数据竞争，收尾后状态一致且可重新启动。
func TestConcurrentStartStopLifecycle(t *testing.T) {
	c := New(cfgWithPort(13001), ModeSend)
	c.SetLogDir(t.TempDir())
	if err := c.Start(""); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 8; j++ {
				c.Stop()
				// 并发下"已在运行"错误是正常竞争（另一 goroutine 刚启动），
				// 本测试关注的是：无 panic、无数据竞争、收尾后状态一致
				_ = c.Start("")
				c.Status()
				c.Aggregator()
			}
		}()
	}
	wg.Wait()
	c.Stop()
	if c.Status().Running {
		t.Fatal("全部停止后不应运行")
	}
	// 停止后还能正常再启动一轮（没有被旧一轮的收尾破坏）
	if err := c.Start(""); err != nil {
		t.Fatalf("收尾后再启动失败: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	c.Stop()
}

// TestUpdateConfigRestartLoop 运行中反复 UpdateConfig（内部走 Restart）：
// 每次都应成功重启且最终可停止（日志收尾不与新一轮打开的日志串扰）。
func TestUpdateConfigRestartLoop(t *testing.T) {
	c := New(cfgWithPort(13002), ModeSend)
	c.SetLogDir(t.TempDir())
	if err := c.Start(""); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		cfg := cfgWithPort(13002)
		cfg.Flows[0].RatePPS = 100 + 10*float64(i)
		restarted, err := c.UpdateConfig(cfg)
		if err != nil {
			t.Fatalf("第 %d 次更新失败: %v", i+1, err)
		}
		if !restarted {
			t.Fatalf("运行中更新应触发重启")
		}
	}
	c.Stop()
	if c.Status().Running {
		t.Fatal("停止后不应运行")
	}
}
