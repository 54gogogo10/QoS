// Package sender 实现 8 条流的 UDP 发送引擎。
package sender

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"qostool/internal/config"
	"qostool/internal/protocol"
	"qostool/internal/stats"
)

// pacing 参数
const (
	// spinBudget 是等待的最后阶段用忙等校准的时长上限；
	// Windows 上 <1ms 的 Sleep 不可靠（系统定时器粒度），只有 spin 能到 µs 级精度。
	spinBudget = 100 * time.Microsecond
	// batchTarget 是精细批量的目标组时长：>1000pps 时按组发送，组内突发 ≤500µs。
	batchTarget = 500 * time.Microsecond
)

// Sender 管理全部流的发送 goroutine。
type Sender struct {
	flows []*flow
	agg   *stats.Aggregator
}

// New 创建发送器；每条流一个 UDP socket。
// 任一 flow 创建失败时，已创建的 socket 全部关闭（避免端口泄漏）。
func New(cfg *config.Config, agg *stats.Aggregator) (*Sender, error) {
	// Windows 非管理员运行且配置含非零 DSCP：标记可能无法生效，提前警告
	if !isElevated() {
		for _, f := range cfg.Flows {
			if f.DSCP > 0 {
				log.Printf("警告: 当前非管理员运行，DSCP 标记可能无法生效（Windows 权限限制，Win7 尤甚）。建议右键以管理员身份运行，或用 netsh qos 配置 DSCP 策略")
				break
			}
		}
	}
	s := &Sender{agg: agg}
	for i, f := range cfg.Flows {
		fw, err := newFlow(i, f, agg)
		if err != nil {
			s.Close() // 关闭已创建的 socket，释放端口
			return nil, fmt.Errorf("flow %d (%s): %w", i+1, f.Name, err)
		}
		s.flows = append(s.flows, fw)
	}
	return s, nil
}

// Run 启动全部流 goroutine，直到 ctx 取消。
func (s *Sender) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, f := range s.flows {
		wg.Add(1)
		go func(f *flow) {
			defer wg.Done()
			f.run(ctx)
		}(f)
	}
	wg.Wait()
}

// Close 关闭全部流的 UDP socket（用于启动失败时的清理）。
func (s *Sender) Close() {
	for _, f := range s.flows {
		if f.qos != nil {
			f.qos.close() // Windows QoS2 流（其他平台 nil）
		}
		f.conn.Close()
	}
}

type flow struct {
	idx          int
	conn         *net.UDPConn
	payload      []byte
	seq          uint32
	agg          *stats.Aggregator
	wireOverhead int
	packetIPSize int // IP 层包长（计入字节速率）

	qos *qosFlow // Windows QoS2 打标流句柄（其他平台 nil）

	cfgCfg     config.Flow   // 速率配置副本（SetRates 动态调速时更新）
	interval   time.Duration // 包间隔（仅计算用，运行读原子值）
	batch      int           // 每批包数（interval < batchTarget 时 >1）
	batchGap   time.Duration // 批间隔 = interval * batch
	batchGapNs atomic.Int64  // 批间隔 ns（run 循环逐批读取，SetRates 原子更新）
	batchN     atomic.Int32  // 批大小（同上）
}

func newFlow(idx int, cfg config.Flow, agg *stats.Aggregator) (*flow, error) {
	laddr := &net.UDPAddr{IP: net.ParseIP(cfg.SrcIP), Port: cfg.SrcPort}
	raddr := &net.UDPAddr{IP: net.ParseIP(cfg.DstIP), Port: cfg.DstPort}
	conn, err := net.DialUDP("udp", laddr, raddr)
	if err != nil {
		return nil, err
	}
	qos, err := setDSCP(conn, int(cfg.DSCP))
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("设置 DSCP=%d 失败: %w", cfg.DSCP, err)
	}
	wireOverhead := 28 // IPv4: 20 IP + 8 UDP
	if net.ParseIP(cfg.SrcIP).To4() == nil {
		wireOverhead = 48 // IPv6: 40 IP + 8 UDP
	}
	// cfg.IPLen 是 IP 包总长：载荷 = IP 包长 - IP/UDP 头
	payload := make([]byte, cfg.IPLen-wireOverhead)
	protocol.EncodeHeader(payload, uint16(idx), 0, 0)
	f := &flow{
		idx:          idx,
		conn:         conn,
		payload:      payload,
		agg:          agg,
		wireOverhead: wireOverhead,
		packetIPSize: cfg.IPLen,
		qos:          qos,
		cfgCfg:       cfg,
	}
	f.computePacing(cfg)
	return f, nil
}

// RateSpec 是单条流的速率参数（SetRates 用）。
type RateSpec struct {
	RateMbps float64
	RatePPS  float64
}

// SetRates 批量更新每流速率并重算 pacing（阶梯扫描用）。
// seq 与 socket 不中断；len(rates) 必须等于流数。
func (s *Sender) SetRates(rates []RateSpec) error {
	if len(rates) != len(s.flows) {
		return fmt.Errorf("速率数量 %d 与流数 %d 不符", len(rates), len(s.flows))
	}
	for i, f := range s.flows {
		f.cfgCfg.RateMbps = rates[i].RateMbps
		f.cfgCfg.RatePPS = rates[i].RatePPS
		f.computePacing(f.cfgCfg)
	}
	return nil
}

// computePacing 根据双速率参数计算包间隔：
// 取 pps 与字节速率（Mbps→IP 字节/秒）两者中较慢者。
func (f *flow) computePacing(cfg config.Flow) {
	pps := cfg.RatePPS
	if bps := cfg.RateMbps * 1e6 / 8; bps > 0 {
		ppsFromBytes := bps / float64(f.packetIPSize)
		if pps == 0 || ppsFromBytes < pps {
			pps = ppsFromBytes
		}
	}
	if pps <= 0 {
		pps = 1
	}
	f.interval = time.Duration(float64(time.Second) / pps)
	if f.interval <= 0 {
		f.interval = time.Nanosecond
	}
	// 间隔小于 batchTarget 时按批发送：组内突发不超过 batchTarget
	f.batch = 1
	if f.interval < batchTarget {
		f.batch = int(batchTarget/f.interval) + 1
		if f.batch < 1 {
			f.batch = 1
		}
	}
	f.batchGap = f.interval * time.Duration(f.batch)
	// 运行循环读取原子副本（SetRates 可并发更新 pacing）
	f.batchGapNs.Store(int64(f.batchGap))
	f.batchN.Store(int32(f.batch))
}

// run 包级精确调度：按绝对时刻逐包（或小批量）发送，杜绝累积漂移。
func (f *flow) run(ctx context.Context) {
	defer f.conn.Close()
	next := time.Now()
	for {
		next = next.Add(time.Duration(f.batchGapNs.Load()))
		if !waitUntil(ctx, next) {
			return
		}
		for i := 0; i < int(f.batchN.Load()); i++ {
			f.seq++
			protocol.EncodeSeqTs(f.payload, f.seq, time.Now().UnixNano())
			if _, err := f.conn.Write(f.payload); err != nil {
				// Linux：UDP connect 到未监听端口会收到 ICMP port unreachable，
				// 后续 Write 返回 ECONNREFUSED——但包已实际发出（ICMP 为异步返回），不能停流。
				if errors.Is(err, syscall.ECONNREFUSED) {
					continue
				}
				return // 其他错误：停止该流
			}
		}
		f.agg.RecordTx(f.idx, uint64(f.batch), uint64(f.batch*(len(f.payload)+f.wireOverhead)))
	}
}

// waitUntil 高精度等待到时刻 t：
// 剩余大于 spinBudget 时用 Sleep（留出 spin 余量，避免睡过头），
// 最后 spinBudget 时段忙等校准到期（µs 级精度）。
// 返回 false 表示 ctx 已取消。
func waitUntil(ctx context.Context, t time.Time) bool {
	for {
		d := time.Until(t)
		if d <= 0 {
			return ctx.Err() == nil
		}
		if d > spinBudget {
			select {
			case <-ctx.Done():
				return false
			case <-time.After(d - spinBudget):
			}
			continue
		}
		// 最后 spinBudget：忙等校准
		for time.Until(t) > 0 {
		}
		return ctx.Err() == nil
	}
}
