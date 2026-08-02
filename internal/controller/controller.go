// Package controller 管理测试运行的生命周期：开始、停止、配置更新与统计采样。
package controller

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"qostool/internal/capture"
	"qostool/internal/config"
	"qostool/internal/report"
	"qostool/internal/sender"
	"qostool/internal/stats"
)

const (
	sampleInterval = 100 * time.Millisecond
	histCap        = 3000
	logRowEvery    = 10 // 每 10 次采样（1s）写一行 CSV 日志
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
	LogFile string    `json:"log_file"` // 本次运行的 CSV 日志路径（空=未启用）
}

// Controller 持有配置与运行状态。Start 启动一轮测试，Stop 停止。
// 并发安全：Start/Stop/Restart/UpdateConfig/Aggregator/Status 可并发调用。
type Controller struct {
	mu      sync.Mutex
	wg      sync.WaitGroup
	cfg     *config.Config
	mode    Mode
	agg     *stats.Aggregator
	lastAgg *stats.Aggregator // 上次运行的聚合器（停止后保留，供页面查看）
	cancel  context.CancelFunc
	iface   string
	running bool
	started time.Time

	logDir  string // 日志根目录（其下建 logs/）；空=不记录
	logFile *os.File
	csvW    *csv.Writer
	logPath string // 本次 CSV 日志路径
}

// New 创建控制器。
func New(cfg *config.Config, mode Mode) *Controller {
	return &Controller{cfg: cfg, mode: mode}
}

// SetLogDir 启用统计日志：每次运行在 <dir>/logs/ 下生成
// qostool_<时间戳>.csv（每秒一行）与 _summary.txt（停止时汇总）。
// 空串禁用日志。
func (c *Controller) SetLogDir(dir string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.logDir = dir
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

// Stop 停止当前测试并等待所有 goroutine 退出（socket/pcap 句柄释放）；
// 停止后保留最后一份统计供页面查看，并写出汇总日志。未运行时无操作。
func (c *Controller) Stop() {
	c.mu.Lock()
	if !c.running {
		c.mu.Unlock()
		return
	}
	iface := c.iface
	startedAt := c.started
	c.cancel()
	c.running = false
	c.iface = ""
	c.lastAgg = c.agg // 保留最后数据，停止后页面仍可查看
	c.agg = nil
	stopped := time.Now()
	c.mu.Unlock()
	c.wg.Wait() // 等待 goroutine 退出，确保端口/句柄已释放

	c.mu.Lock()
	defer c.mu.Unlock()
	c.closeLogLocked(c.lastAgg, startedAt, iface, stopped)
}

// Restart 用当前配置在相同接口上重启测试；未运行时无操作。
func (c *Controller) Restart() error {
	c.mu.Lock()
	if !c.running {
		c.mu.Unlock()
		return nil
	}
	iface := c.iface
	startedAt := c.started
	oldAgg := c.agg
	c.cancel()
	c.running = false
	c.mu.Unlock()
	c.wg.Wait() // 等旧 goroutine 退出、句柄释放后再启动

	c.mu.Lock()
	defer c.mu.Unlock()
	c.closeLogLocked(oldAgg, startedAt, iface, time.Now()) // 收尾上一轮日志
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

// Aggregator 返回当前运行的聚合器；停止后返回上次运行的聚合器（页面继续显示）。
func (c *Controller) Aggregator() *stats.Aggregator {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.agg != nil {
		return c.agg
	}
	return c.lastAgg
}

// Status 返回当前状态。
func (c *Controller) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Status{Running: c.running, Iface: c.iface, Started: c.started, LogFile: c.logPath}
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
	c.lastAgg = nil // 新一轮测试开始，清掉上一轮数据
	if c.logDir != "" {
		c.openLogLocked(c.started) // 日志失败不阻塞测试
	}
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

// sampleLoop 每 100ms 采样一次快照（速率与历史），每 1s 写一行 CSV 日志。
func (c *Controller) sampleLoop(ctx context.Context, agg *stats.Aggregator) {
	ticker := time.NewTicker(sampleInterval)
	defer ticker.Stop()
	n := 0
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			agg.Snapshot(now)
			n++
			if n%logRowEvery == 0 {
				c.writeLogRow(now, agg)
			}
		}
	}
}

// openLogLocked 创建本次运行的 CSV 日志（调用方持有锁）。
func (c *Controller) openLogLocked(start time.Time) {
	dir := filepath.Join(c.logDir, "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	path := filepath.Join(dir, fmt.Sprintf("qostool_%s.csv", start.Format("20060102_150405")))
	f, err := os.Create(path)
	if err != nil {
		return
	}
	c.logFile = f
	c.logPath = path
	c.csvW = csv.NewWriter(f)
	rec := []string{"time"}
	for i := range c.cfg.Flows {
		rec = append(rec, fmt.Sprintf("tx_pps_%d", i+1))
	}
	for i := range c.cfg.Flows {
		rec = append(rec, fmt.Sprintf("rx_pps_%d", i+1))
	}
	for i := range c.cfg.Flows {
		rec = append(rec, fmt.Sprintf("loss_pct_%d", i+1))
	}
	c.csvW.Write(rec)
	c.csvW.Flush()
}

// writeLogRow 写一行 1s 粒度的统计（调用方持有锁）。
func (c *Controller) writeLogRow(now time.Time, agg *stats.Aggregator) {
	if c.csvW == nil {
		return
	}
	snap := agg.Current(now)
	rec := []string{now.Format("2006-01-02 15:04:05")}
	for _, s := range snap {
		rec = append(rec, fmt.Sprintf("%d", int(s.TxPps+0.5)))
	}
	for _, s := range snap {
		rec = append(rec, fmt.Sprintf("%d", int(s.RxPps+0.5)))
	}
	for _, s := range snap {
		rec = append(rec, fmt.Sprintf("%.3f", s.LossRate*100))
	}
	c.csvW.Write(rec)
	c.csvW.Flush()
}

// closeLogLocked 关闭 CSV 并写出汇总 txt（调用方持有锁）。
func (c *Controller) closeLogLocked(agg *stats.Aggregator, started time.Time, iface string, stopped time.Time) {
	if c.logFile == nil {
		return
	}
	c.csvW.Flush()
	c.logFile.Close()
	c.logFile = nil
	c.csvW = nil

	// 汇总文件
	sumPath := c.logPath[:len(c.logPath)-len(".csv")] + "_summary.txt"
	c.logPath = ""
	if agg == nil {
		return
	}
	snap := agg.Current(stopped)
	txTotal, rxTotal, lost := agg.Totals()
	dur := stopped.Sub(started).Round(time.Second)
	var b []byte
	b = append(b, fmt.Sprintf("qostool 运行汇总\n")...)
	b = append(b, fmt.Sprintf("开始: %s\n", started.Format("2006-01-02 15:04:05"))...)
	b = append(b, fmt.Sprintf("结束: %s\n", stopped.Format("2006-01-02 15:04:05"))...)
	b = append(b, fmt.Sprintf("时长: %s\n", dur)...)
	b = append(b, fmt.Sprintf("接口: %s\n", iface)...)
	b = append(b, fmt.Sprintf("\n")...)
	b = append(b, report.Summary(c.cfg, snap, txTotal, rxTotal, lost)...)
	os.WriteFile(sumPath, b, 0o644)
}
