// Package controller 管理测试运行的生命周期：开始、停止、配置更新与统计采样。
package controller

import (
	"context"
	"fmt"
	"sync"
	"time"

	"qostool/internal/capture"
	"qostool/internal/config"
	"qostool/internal/sender"
	"qostool/internal/stats"
)

const (
	sampleInterval = 100 * time.Millisecond
	histCap        = 3000
)

// Mode 是运行模式，决定启动哪些组件。
type Mode string

const (
	ModeSend  Mode = "send"  // 只发送
	ModeRecv  Mode = "recv"  // 只接收
	ModeBidir Mode = "bidir" // 收发同时
)

// Status 描述当前运行状态。
type Status struct {
	Running bool      `json:"running"`
	Iface   string    `json:"iface"`
	Started time.Time `json:"started"`
}

// Controller 持有配置与运行状态。Start 启动一轮测试，Stop 停止。
// 并发安全：Start/Stop/Restart/UpdateConfig/Aggregator/Status 可并发调用。
type Controller struct {
	mu      sync.Mutex
	wg      sync.WaitGroup
	cfg     *config.Config
	mode    Mode
	agg     *stats.Aggregator
	cancel  context.CancelFunc
	iface   string
	running bool
	started time.Time
}

// New 创建控制器。
func New(cfg *config.Config, mode Mode) *Controller {
	return &Controller{cfg: cfg, mode: mode}
}

// Config 返回当前配置（只读，调用方不应修改）。
func (c *Controller) Config() *config.Config {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cfg
}

// Mode 返回运行模式。
func (c *Controller) Mode() Mode {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mode
}

// Start 在指定接口上启动一轮测试；已在运行时返回错误。
func (c *Controller) Start(iface string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running {
		return fmt.Errorf("测试已在运行中（接口 %s）", c.iface)
	}
	ctx, cancel := context.WithCancel(context.Background())
	return c.startWithRetryLocked(ctx, cancel, iface)
}

// Stop 停止当前测试并等待所有 goroutine 退出（socket/pcap 句柄释放）；未运行时无操作。
func (c *Controller) Stop() {
	c.mu.Lock()
	if !c.running {
		c.mu.Unlock()
		return
	}
	c.cancel()
	c.running = false
	c.iface = ""
	c.agg = nil
	c.started = time.Time{}
	c.mu.Unlock()
	c.wg.Wait() // 等待 goroutine 退出，确保端口/句柄已释放
}

// Restart 用当前配置在相同接口上重启测试；未运行时无操作。
func (c *Controller) Restart() error {
	c.mu.Lock()
	if !c.running {
		c.mu.Unlock()
		return nil
	}
	iface := c.iface
	c.cancel()
	c.running = false
	c.mu.Unlock()
	c.wg.Wait() // 等旧 goroutine 退出、句柄释放后再启动

	c.mu.Lock()
	defer c.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	return c.startWithRetryLocked(ctx, cancel, iface)
}

// startWithRetryLocked 启动组件；Windows 上 UDP socket 关闭后端口可能延迟释放，
// bind 失败时每 100ms 重试，最多 10 次（调用方必须持有锁）。
func (c *Controller) startWithRetryLocked(ctx context.Context, cancel context.CancelFunc, iface string) error {
	var err error
	for i := 0; i < 10; i++ {
		err = c.startLocked(ctx, cancel, iface)
		if err == nil {
			return nil
		}
		if i < 9 {
			c.mu.Unlock()
			time.Sleep(100 * time.Millisecond)
			c.mu.Lock()
		}
	}
	cancel()
	c.agg = nil
	return err
}

// UpdateConfig 替换配置；运行中则自动重启以应用新配置。
// 返回是否发生了重启。
func (c *Controller) UpdateConfig(cfg *config.Config) (restarted bool, err error) {
	c.mu.Lock()
	c.cfg = cfg
	running := c.running
	c.mu.Unlock()
	if running {
		if err := c.Restart(); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

// Aggregator 返回当前运行的聚合器；未运行时返回 nil。
func (c *Controller) Aggregator() *stats.Aggregator {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.agg
}

// Status 返回当前状态。
func (c *Controller) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Status{Running: c.running, Iface: c.iface, Started: c.started}
}

// SetAggregatorForTest 仅供测试注入聚合器（标记为运行中）。
func (c *Controller) SetAggregatorForTest(agg *stats.Aggregator) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.agg = agg
	c.running = true
	c.cancel = func() {} // no-op，避免 Restart 触发 nil 调用
}

// startLocked 启动组件（调用方必须持有锁且 running==false）。
func (c *Controller) startLocked(ctx context.Context, cancel context.CancelFunc, iface string) error {
	agg := stats.NewAggregator(len(c.cfg.Flows), histCap)

	var s *sender.Sender
	var err error
	if c.mode != ModeRecv {
		s, err = sender.New(c.cfg, agg)
		if err != nil {
			return err
		}
	}
	var cap *capture.Capturer
	if c.mode != ModeSend {
		cap, err = capture.New(iface, c.cfg, agg)
		if err != nil {
			if s != nil {
				s.Close() // 清理已创建的 socket，避免泄漏
			}
			return err
		}
	}

	c.agg, c.cancel, c.iface, c.running, c.started = agg, cancel, iface, true, time.Now()
	if s != nil {
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			s.Run(ctx)
		}()
	}
	if cap != nil {
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			cap.Run(ctx)
		}()
	}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.sampleLoop(ctx, agg)
	}()
	return nil
}

// sampleLoop 每 100ms 采样一次快照（速率与历史）。
func (c *Controller) sampleLoop(ctx context.Context, agg *stats.Aggregator) {
	ticker := time.NewTicker(sampleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			agg.Snapshot(now)
		}
	}
}
