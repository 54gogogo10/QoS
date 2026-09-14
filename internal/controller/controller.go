// Package controller 管理测试运行的生命周期：开始、停止、配置更新与统计采样。
package controller

import (
	"context"
	"encoding/csv"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
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
	drainDelay     = 500 * time.Millisecond
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
	Running    bool      `json:"running"`
	Iface      string    `json:"iface"`
	Started    time.Time `json:"started"`
	LogFile    string    `json:"log_file"`    // 本次运行的 CSV 日志路径（空=未启用）
	ReportFile string    `json:"report_file"` // 最近一次生成的 HTML 报告路径（空=未生成）
}

// Controller 持有配置与运行状态。Start 启动一轮测试，Stop 停止。
// 并发安全：Start/Stop/Restart/UpdateConfig/Aggregator/Status 可并发调用。
type Controller struct {
	// lmu 生命周期互斥：Start/Stop/Restart 全程（含停发送后的排空等待与
	// goroutine 退出等待）串行化，防止停止期间并发 Start 插入导致
	// 旧一轮的收尾误关新一轮刚打开的日志、或双份发送/抓包 goroutine 泄漏。
	lmu sync.Mutex
	mu  sync.Mutex
	wg  sync.WaitGroup
	cfg *config.Config
	mode Mode
	agg  *stats.Aggregator
	lastAgg *stats.Aggregator // 上次运行的聚合器（停止后保留，供页面查看）

	senderCancel  context.CancelFunc // 发送 goroutine 的取消（先停）
	captureCancel context.CancelFunc // 接收 goroutine 的取消（后停）
	cap           *capture.Capturer            // 当前抓包器（IfaceStats 诊断用）
	remoteTX      atomic.Pointer[RemoteTXFunc] // 远端发送端 TX 数据提供者（recv 模式日志用）
	iface         string
	running       bool
	started       time.Time

	clockOffsetHint int64 // 控制通道时钟偏差（ns，v2.8.1）：Start 前设置，创建聚合器时应用
	clockHintSet    bool

	lastStarted time.Time // 上一轮开始时刻（v2.9.0：停止后导出报告用，防零值时间戳）

	drainDelay time.Duration // 停止时：停发送后等待在途包被捕获的时长
	logDir     string        // 日志根目录（其下建 logs/）；空=不记录
	logFile    *os.File
	csvW       *csv.Writer
	logPath    string // 本次 CSV 日志路径
	lastReportPath string // 最近一次生成的 HTML 报告路径（停止时/导出按钮）
}

// New 创建控制器。
func New(cfg *config.Config, mode Mode) *Controller {
	return &Controller{cfg: cfg, mode: mode, drainDelay: drainDelay}
}

// RemoteTXFunc 返回远端发送端的 TX 统计（pps/包数/字节），ok=false 表示无远端数据。
type RemoteTXFunc func() (pps []float64, pkts, bytes []uint64, ok bool)

// SetRemoteTXProvider 设置远端发送端 TX 数据提供者：
// recv 模式写日志/汇总时，TX 列用远端发送端的数据（而非本端 0）。
// 经 atomic 指针读写，可在运行期间安全调用。
func (c *Controller) SetRemoteTXProvider(fn RemoteTXFunc) {
	c.remoteTX.Store(&fn)
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

// SetMode 切换运行模式（发送/接收/双向）。运行中时自动重启以生效。
func (c *Controller) SetMode(m Mode) error {
	c.mu.Lock()
	c.mode = m
	running := c.running
	c.mu.Unlock()
	if running {
		return c.Restart()
	}
	return nil
}

// Start 在指定接口上启动一轮测试；已在运行时返回错误。
func (c *Controller) Start(iface string) error {
	c.lmu.Lock()
	defer c.lmu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running {
		return fmt.Errorf("测试已在运行中（接口 %s）", c.iface)
	}
	return c.startWithRetryLocked(iface)
}

// Stop 停止测试：先停发送，等待在途包被接收端捕获（drainDelay），
// 再停接收并等待所有 goroutine 退出。停止后保留最后一份统计供页面查看，
// 并写出汇总日志。未运行时无操作。
// 持有 lmu 全程：与 Start/Restart 串行，收尾（关日志/写汇总）不会插进新一轮运行。
func (c *Controller) Stop() {
	c.lmu.Lock()
	defer c.lmu.Unlock()
	c.mu.Lock()
	if !c.running {
		c.mu.Unlock()
		return
	}
	iface := c.iface
	startedAt := c.started
	c.lastStarted = startedAt // 保留上一轮开始时刻：停止后导出报告不再是零值时间（v2.9.0）
	if c.senderCancel != nil {
		c.senderCancel() // 1. 先停发送：不再产生新包
	}
	c.running = false
	c.iface = ""
	savedAgg := c.agg
	c.lastAgg = c.agg // 保留最后数据，停止后页面仍可查看
	c.agg = nil
	c.cap = nil
	c.started = time.Time{}
	stopped := time.Now()
	c.mu.Unlock()

	// 2. 等待在途包被接收端捕获（发送已停，RX 追平 TX）
	if c.captureCancel != nil {
		time.Sleep(c.drainDelay)
		c.captureCancel() // 3. 再停接收
	}
	c.wg.Wait() // 4. 等所有 goroutine 退出（socket/pcap 句柄释放）

	if savedAgg != nil {
		savedAgg.Finalize() // 不再有包到达：宽限期内未补齐的乱序孔洞直接计丢包
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.closeLogLocked(savedAgg, startedAt, iface, stopped)
}

// Restart 用当前配置在相同接口上重启测试；未运行时无操作。
// 同样先停发送、延迟停接收，避免尾部差。
func (c *Controller) Restart() error {
	c.lmu.Lock()
	defer c.lmu.Unlock()
	c.mu.Lock()
	if !c.running {
		c.mu.Unlock()
		return nil
	}
	iface := c.iface
	startedAt := c.started
	oldAgg := c.agg
	c.lastStarted = startedAt // 同 Stop：保留开始时刻供停止后导出报告
	if c.senderCancel != nil {
		c.senderCancel()
	}
	c.running = false
	c.mu.Unlock()

	if c.captureCancel != nil {
		time.Sleep(c.drainDelay)
		c.captureCancel()
	}
	c.wg.Wait() // 等旧 goroutine 退出、句柄释放后再启动

	if oldAgg != nil {
		oldAgg.Finalize() // 不再有包到达：宽限期内未补齐的乱序孔洞直接计丢包
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.closeLogLocked(oldAgg, startedAt, iface, time.Now()) // 收尾上一轮日志
	return c.startWithRetryLocked(iface)
}

// startWithRetryLocked 启动组件；Windows 上 UDP socket 关闭后端口可能延迟释放，
// bind 失败时每 100ms 重试，最多 10 次（调用方必须持有锁）。
func (c *Controller) startWithRetryLocked(iface string) error {
	var err error
	for i := 0; i < 10; i++ {
		err = c.startLocked(iface)
		if err == nil {
			return nil
		}
		if i < 9 {
			c.mu.Unlock()
			time.Sleep(100 * time.Millisecond)
			c.mu.Lock()
		}
	}
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

// IfaceStats 返回接口级抓包统计；未抓包时返回零值。
func (c *Controller) IfaceStats() capture.IfaceStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cap == nil {
		return capture.IfaceStats{}
	}
	return c.cap.Stats()
}

// Status 返回当前状态。
func (c *Controller) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Status{Running: c.running, Iface: c.iface, Started: c.started, LogFile: c.logPath, ReportFile: c.lastReportPath}
}

// LastReportPath 返回最近一次生成的 HTML 报告路径（空=未生成）。
func (c *Controller) LastReportPath() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastReportPath
}

// WriteHTMLReport 按当前（或上次）统计立即生成 HTML 报告（Web 导出按钮用）。
func (c *Controller) WriteHTMLReport() (string, error) {
	c.mu.Lock()
	agg, cfg, logDir := c.agg, c.cfg, c.logDir
	mode := c.mode // 与 SetMode 的写并发，必须在锁内读
	if agg == nil {
		agg = c.lastAgg
	}
	st := Status{Running: c.running, Iface: c.iface, Started: c.started}
	if !c.running {
		st.Started = c.lastStarted // 停止后导出：用上一轮开始时刻（v2.9.0，防 0001-01-01）
	}
	c.mu.Unlock()
	if agg == nil || logDir == "" {
		return "", fmt.Errorf("无统计数据或未设置日志目录")
	}
	snap := agg.Current(time.Now())
	vs := report.Verdicts(cfg, snap, true)
	path, err := report.HTMLReport(cfg, snap, vs, report.ReportMeta{
		Started: st.Started, Stopped: time.Now(), Iface: st.Iface, Mode: string(mode), Version: report.Version,
	}, filepath.Join(logDir, "logs"), mode != ModeRecv, agg.DelayHistogram())
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	c.lastReportPath = path
	c.mu.Unlock()
	return path, nil
}

// SetClockOffsetHint 注入外部时钟偏差估计（ns，控制通道测量，v2.8.1）：
// 运行中直接注入当前聚合器；未运行时保存，Start 创建聚合器时自动应用
// （作为时延校正基准，保守融合见 stats.SetClockOffsetHint）。
func (c *Controller) SetClockOffsetHint(hintNs int64) {
	c.mu.Lock()
	c.clockOffsetHint = hintNs
	c.clockHintSet = true
	agg := c.agg
	c.mu.Unlock()
	if agg != nil {
		agg.SetClockOffsetHint(hintNs)
	}
}

// SetAggregatorForTest 仅供测试注入聚合器（标记为运行中）。
func (c *Controller) SetAggregatorForTest(agg *stats.Aggregator) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.agg = agg
	c.running = true
	c.senderCancel = func() {} // no-op，避免 Restart 触发 nil 调用
	c.captureCancel = func() {}
}

// startLocked 启动组件（调用方必须持有锁且 running==false）。
func (c *Controller) startLocked(iface string) error {
	agg := stats.NewAggregator(len(c.cfg.Flows), histCap)
	// v2.8.1：Start 前注入的控制通道时钟偏差应用到新建聚合器
	if c.clockHintSet {
		agg.SetClockOffsetHint(c.clockOffsetHint)
	}

	// 发送与接收使用独立 ctx：停止时先取消发送，延迟后再取消接收
	sCtx, sCancel := context.WithCancel(context.Background())
	cCtx, cCancel := context.WithCancel(context.Background())

	var s *sender.Sender
	var err error
	if c.mode != ModeRecv {
		s, err = sender.New(c.cfg, agg)
		if err != nil {
			sCancel()
			cCancel()
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
			sCancel()
			cCancel()
			return err
		}
	}

	c.agg, c.iface, c.running, c.started = agg, iface, true, time.Now()
	c.cap = cap
	c.lastAgg = nil // 新一轮测试开始，清掉上一轮数据
	c.senderCancel, c.captureCancel = sCancel, cCancel
	if c.logDir != "" {
		c.openLogLocked(c.started) // 日志失败不阻塞测试
	}
	if s != nil {
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			s.Run(sCtx)
		}()
	}
	if cap != nil {
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			if err := cap.Run(cCtx); err != nil && cCtx.Err() == nil {
				log.Printf("[capture] 接口 %s 抓包异常: %v", cap.Iface(), err)
			}
		}()
	}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.sampleLoop(cCtx, agg) // 采样循环跟随接收，最后收尾
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
	// v2.7.0 追加时延/抖动列（每秒一行，末尾列，不影响旧列顺序）
	for i := range c.cfg.Flows {
		rec = append(rec, fmt.Sprintf("avg_delay_ms_%d", i+1))
	}
	for i := range c.cfg.Flows {
		rec = append(rec, fmt.Sprintf("jitter_ms_%d", i+1))
	}
	// v2.8.0 追加时延百分位列
	for i := range c.cfg.Flows {
		rec = append(rec, fmt.Sprintf("p95_delay_ms_%d", i+1))
	}
	for i := range c.cfg.Flows {
		rec = append(rec, fmt.Sprintf("p99_delay_ms_%d", i+1))
	}
	// v2.9.0 追加乱序包数列
	for i := range c.cfg.Flows {
		rec = append(rec, fmt.Sprintf("reorder_%d", i+1))
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
	c.applyRemoteTX(snap)
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
	for _, s := range snap {
		if s.DelayValid {
			rec = append(rec, fmt.Sprintf("%.3f", s.DelayAvgMs))
		} else {
			rec = append(rec, "")
		}
	}
	for _, s := range snap {
		if s.DelayValid {
			rec = append(rec, fmt.Sprintf("%.3f", s.JitterMs))
		} else {
			rec = append(rec, "")
		}
	}
	for _, s := range snap {
		if s.DelayValid {
			rec = append(rec, fmt.Sprintf("%.3f", s.DelayP95Ms))
		} else {
			rec = append(rec, "")
		}
	}
	for _, s := range snap {
		if s.DelayValid {
			rec = append(rec, fmt.Sprintf("%.3f", s.DelayP99Ms))
		} else {
			rec = append(rec, "")
		}
	}
	// v2.9.0 乱序包数（累计值）
	for _, s := range snap {
		rec = append(rec, fmt.Sprintf("%d", s.Reordered))
	}
	c.csvW.Write(rec)
	c.csvW.Flush()
}

// applyRemoteTX 用远端发送端数据覆盖快照的 TX 字段（recv 模式本端 TX 恒为 0）。
// 返回 true 表示远端数据已应用。
func (c *Controller) applyRemoteTX(snap []stats.FlowSnapshot) bool {
	fn := c.remoteTX.Load()
	if fn == nil {
		return false
	}
	pps, pkts, bytes, ok := (*fn)()
	if !ok {
		return false
	}
	for i := range snap {
		if i < len(pps) {
			snap[i].TxPps = pps[i]
		}
		if i < len(pkts) {
			snap[i].TxPackets = pkts[i]
		}
		if i < len(bytes) {
			snap[i].TxBytes = bytes[i]
		}
	}
	return true
}

// closeLogLocked 关闭 CSV 并写出汇总 txt 与 HTML 报告（调用方持有锁）。
func (c *Controller) closeLogLocked(agg *stats.Aggregator, started time.Time, iface string, stopped time.Time) {
	// HTML 报告与 CSV 解耦：无日志文件也生成（导出/CLI 场景）
	if agg != nil {
		snap := agg.Current(stopped)
		if path, err := report.HTMLReport(c.cfg, snap, report.Verdicts(c.cfg, snap, true), report.ReportMeta{
			Started: started, Stopped: stopped, Iface: iface, Mode: string(c.mode), Version: report.Version,
		}, filepath.Join(c.logDir, "logs"), c.mode != ModeRecv, agg.DelayHistogram()); err != nil {
			log.Printf("生成 HTML 报告失败: %v", err)
		} else {
			c.lastReportPath = path
		}
	}
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
	applied := c.applyRemoteTX(snap)
	txTotal, rxTotal, lost := agg.Totals()
	// 远端 TX 覆盖后重新计算总发送（用各流已覆盖的 TxBytes 求和）
	if applied {
		var t uint64
		for i := range snap {
			t += snap[i].TxBytes
		}
		txTotal = t
	}
	dur := stopped.Sub(started).Round(time.Second)
	var b []byte
	b = append(b, fmt.Sprintf("qostool 运行汇总\n")...)
	b = append(b, fmt.Sprintf("开始: %s\n", started.Format("2006-01-02 15:04:05"))...)
	b = append(b, fmt.Sprintf("结束: %s\n", stopped.Format("2006-01-02 15:04:05"))...)
	b = append(b, fmt.Sprintf("时长: %s\n", dur)...)
	b = append(b, fmt.Sprintf("接口: %s\n", iface)...)
	b = append(b, fmt.Sprintf("\n")...)
	// recv 模式 TX 来自远端轮询快照（滞后最多 1s），尾部差口径不成立，不参与丢包统计
	b = append(b, report.Summary(c.cfg, snap, txTotal, rxTotal, lost, c.remoteTX.Load() == nil)...)
	os.WriteFile(sumPath, b, 0o644)
}
