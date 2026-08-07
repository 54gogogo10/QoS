package stats

import (
	"sync"
	"sync/atomic"
	"time"
)

// FlowSnapshot 是一条流的实时统计快照。
type FlowSnapshot struct {
	FlowIdx   int
	TxBps     float64 // TxBps 字节/秒
	TxPps     float64
	RxBps     float64 // RxBps 字节/秒
	RxPps     float64
	TxPackets uint64
	TxBytes   uint64
	RxPackets uint64
	RxBytes   uint64
	Lost      uint64
	LossRate  float64 // 0..1

	// 时延/抖动（v2.7.0）：DelayValid=false 表示发送端无时间戳（旧版本），其余字段为 0。
	DelayValid   bool     // 是否收到过带时间戳的包
	DelayAvgMs   float64  // 最近采样窗口（100ms）平均时延（毫秒）
	DelayTotAvgMs float64 // 全程累计平均时延（毫秒，报告/最终判定用）
	DelayMinMs   float64 // 全程最小（毫秒）
	DelayMaxMs   float64 // 全程最大（毫秒）
	JitterMs     float64 // RFC3550 抖动（毫秒，累计）
}

// History 是 Web 图表用的历史序列（并行数组，单位 字节/秒）。
// Dly 为每流窗口平均时延（毫秒），无时延数据时为 -1 哨兵（JSON 不支持 NaN）。
type History struct {
	T   []int64     // unix 毫秒
	TxB [][]float64 // [flow][point]
	RxB [][]float64 // [flow][point]
	Dly [][]float64 // [flow][point] 时延 ms，无效 = -1
}

type sample struct {
	t   time.Time
	txB []uint64
	txP []uint64
	rxB []uint64
	rxP []uint64
}

// delayState 是单条流的时延统计状态，由 delayMu[flowIdx] 保护。
// 窗口（win*）在每次 Snapshot 时清零；累计（tot*、min/max、jitter）跨窗口保留。
type delayState struct {
	valid      bool
	winSumNs   uint64 // 窗口内时延和（ns）
	winCount   uint64
	totSumNs   uint64 // 全程时延和（ns）
	totCount   uint64
	delayMinNs int64 // 全程最小（ns）
	delayMaxNs int64 // 全程最大（ns）
	prevDelay  int64 // 上一包时延（ns）
	prevSet    bool
	jitterNs   float64 // RFC3550 抖动（ns）
}

const sampleWindow = time.Second

// Aggregator 汇总所有流的计数并计算实时速率。
// 并发安全：RecordTx 多 goroutine；RecordRx 单 goroutine（抓包线程）；
// Snapshot/Current/History 可并发。
type Aggregator struct {
	nFlows  int
	txPkts  []atomic.Uint64
	txB     []atomic.Uint64
	rxPkts  []atomic.Uint64
	rxB     []atomic.Uint64
	lost    []atomic.Uint64
	lastSeq []atomic.Uint32

	delay    []delayState
	delayMu  []sync.Mutex // 每流一把：RecordRx（抓包线程）与 Snapshot/Current（采样线程）互斥

	mu      sync.Mutex
	samples []sample
	hist    History
	histCap int
}

// NewAggregator 创建 nFlows 条流的聚合器，histCap 为 Web 历史容量。
func NewAggregator(nFlows, histCap int) *Aggregator {
	return &Aggregator{
		nFlows:  nFlows,
		txPkts:  make([]atomic.Uint64, nFlows),
		txB:     make([]atomic.Uint64, nFlows),
		rxPkts:  make([]atomic.Uint64, nFlows),
		rxB:     make([]atomic.Uint64, nFlows),
		lost:    make([]atomic.Uint64, nFlows),
		lastSeq: make([]atomic.Uint32, nFlows),
		delay:   make([]delayState, nFlows),
		delayMu: make([]sync.Mutex, nFlows),
		hist: History{
			TxB: make([][]float64, nFlows),
			RxB: make([][]float64, nFlows),
			Dly: make([][]float64, nFlows),
		},
		histCap: histCap,
	}
}

// RecordTx 发送端每发出 n 个包（共 bytes 字节）调用一次。
// flowIdx 超出 [0, nFlows) 时直接忽略（调用方 bug 防御，不 panic）。
func (a *Aggregator) RecordTx(flowIdx int, n, bytes uint64) {
	if flowIdx < 0 || flowIdx >= a.nFlows {
		return
	}
	a.txPkts[flowIdx].Add(n)
	a.txB[flowIdx].Add(bytes)
}

// RecordRx 抓包端每命中一个包调用一次。
// seq 大于 lastSeq 时，中间跳过的序号计为丢失；小于等于视为乱序/重传，不计数。
// sendTs>0 时更新时延统计（sendTs==0 表示旧版发送端，无时间戳）；recv 为捕获时刻。
// flowIdx 超出 [0, nFlows) 时直接忽略（调用方 bug 防御，不 panic）。
func (a *Aggregator) RecordRx(flowIdx int, bytes uint64, seq uint32, recv time.Time, sendTs int64) {
	if flowIdx < 0 || flowIdx >= a.nFlows {
		return
	}
	a.rxPkts[flowIdx].Add(1)
	a.rxB[flowIdx].Add(bytes)
	last := a.lastSeq[flowIdx].Load()
	if seq > last {
		if last != 0 && seq > last+1 {
			a.lost[flowIdx].Add(uint64(seq - last - 1))
		}
		a.lastSeq[flowIdx].Store(seq)
	}
	if sendTs > 0 {
		d := recv.UnixNano() - sendTs
		if d < 0 {
			d = 0 // 双机时钟偏移：钳到 0，不产生负时延
		}
		a.delayMu[flowIdx].Lock()
		st := &a.delay[flowIdx]
		if !st.valid {
			st.valid = true
			st.delayMinNs, st.delayMaxNs = d, d
		} else {
			if d < st.delayMinNs {
				st.delayMinNs = d
			}
			if d > st.delayMaxNs {
				st.delayMaxNs = d
			}
		}
		st.winSumNs += uint64(d)
		st.winCount++
		st.totSumNs += uint64(d)
		st.totCount++
		if st.prevSet {
			diff := st.prevDelay - d
			if diff < 0 {
				diff = -diff
			}
			st.jitterNs += (float64(diff) - st.jitterNs) / 16 // RFC3550: J += (|D(i-1,i)| - J)/16
		}
		st.prevDelay, st.prevSet = d, true
		a.delayMu[flowIdx].Unlock()
	}
}

// Snapshot 记录一个采样点、计算速率并追加 Web 历史（报告循环调用）。
// 同时重置时延窗口累加器（min/max、累计平均、抖动跨窗口保留）。
func (a *Aggregator) Snapshot(now time.Time) []FlowSnapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.samples = append(a.samples, sample{
		t:   now,
		txB: loadAll(a.txB), txP: loadAll(a.txPkts),
		rxB: loadAll(a.rxB), rxP: loadAll(a.rxPkts),
	})
	out := a.ratesLocked(now)
	a.hist.T = append(a.hist.T, now.UnixMilli())
	for i := 0; i < a.nFlows; i++ {
		a.hist.TxB[i] = append(a.hist.TxB[i], out[i].TxBps)
		a.hist.RxB[i] = append(a.hist.RxB[i], out[i].RxBps)
		dly := -1.0 // 无时延数据（旧发送端/未收包）：-1 哨兵，JSON 不能用 NaN
		if out[i].DelayValid {
			dly = out[i].DelayAvgMs
		}
		a.hist.Dly[i] = append(a.hist.Dly[i], dly)
	}
	if len(a.hist.T) > a.histCap {
		drop := len(a.hist.T) - a.histCap
		a.hist.T = a.hist.T[drop:]
		for i := 0; i < a.nFlows; i++ {
			a.hist.TxB[i] = a.hist.TxB[i][drop:]
			a.hist.RxB[i] = a.hist.RxB[i][drop:]
			a.hist.Dly[i] = a.hist.Dly[i][drop:]
		}
	}
	// 窗口清零：下一个采样窗口重新累计（锁序 a.mu → delayMu，与 RecordRx 无反向，无死锁）
	for i := 0; i < a.nFlows; i++ {
		a.delayMu[i].Lock()
		a.delay[i].winSumNs = 0
		a.delay[i].winCount = 0
		a.delayMu[i].Unlock()
	}
	return out
}

// Current 计算当前速率，不追加历史（Web 轮询与表格用）。
func (a *Aggregator) Current(now time.Time) []FlowSnapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.ratesLocked(now)
}

// ratesLocked 基于最近 1s 滑动窗口计算速率；窗口为空或 dt<=0 时速率全 0。
// 同时填充时延字段（读取不清零窗口；Snapshot 在返回后负责清零）。
func (a *Aggregator) ratesLocked(now time.Time) []FlowSnapshot {
	cutoff := now.Add(-sampleWindow)
	start := 0
	for start < len(a.samples) && a.samples[start].t.Before(cutoff) {
		start++
	}
	a.samples = a.samples[start:]

	out := make([]FlowSnapshot, a.nFlows)
	var oldest sample
	hasSample := len(a.samples) > 0
	if hasSample {
		oldest = a.samples[0]
	}
	dt := 0.0
	if hasSample {
		dt = now.Sub(oldest.t).Seconds()
	}
	for i := 0; i < a.nFlows; i++ {
		txP := a.txPkts[i].Load()
		txB := a.txB[i].Load()
		rxP := a.rxPkts[i].Load()
		rxB := a.rxB[i].Load()
		lost := a.lost[i].Load()
		s := FlowSnapshot{
			FlowIdx:   i,
			TxPackets: txP,
			TxBytes:   txB,
			RxPackets: rxP,
			RxBytes:   rxB,
			Lost:      lost,
		}
		if hasSample && dt > 0 {
			s.TxBps = float64(txB-oldest.txB[i]) / dt
			s.TxPps = float64(txP-oldest.txP[i]) / dt
			s.RxBps = float64(rxB-oldest.rxB[i]) / dt
			s.RxPps = float64(rxP-oldest.rxP[i]) / dt
		}
		if exp := rxP + lost; exp > 0 {
			s.LossRate = float64(lost) / float64(exp)
		}
		// 时延字段：短临界区读每流状态（快照线程与抓包线程共享）
		a.delayMu[i].Lock()
		st := &a.delay[i]
		s.DelayValid = st.valid
		if st.valid {
			if st.winCount > 0 {
				s.DelayAvgMs = float64(st.winSumNs) / float64(st.winCount) / 1e6
			}
			if st.totCount > 0 {
				s.DelayTotAvgMs = float64(st.totSumNs) / float64(st.totCount) / 1e6
			}
			s.DelayMinMs = float64(st.delayMinNs) / 1e6
			s.DelayMaxMs = float64(st.delayMaxNs) / 1e6
			s.JitterMs = st.jitterNs / 1e6
		}
		a.delayMu[i].Unlock()
		out[i] = s
	}
	return out
}

// History 返回历史序列的深拷贝。
func (a *Aggregator) History() History {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := History{
		T:   append([]int64(nil), a.hist.T...),
		TxB: make([][]float64, a.nFlows),
		RxB: make([][]float64, a.nFlows),
		Dly: make([][]float64, a.nFlows),
	}
	for i := 0; i < a.nFlows; i++ {
		out.TxB[i] = append([]float64(nil), a.hist.TxB[i]...)
		out.RxB[i] = append([]float64(nil), a.hist.RxB[i]...)
		out.Dly[i] = append([]float64(nil), a.hist.Dly[i]...)
	}
	return out
}

// Totals 返回运行以来累计的收发字节与丢失包数（汇总报告用）。
func (a *Aggregator) Totals() (txBytes, rxBytes, lost uint64) {
	for i := 0; i < a.nFlows; i++ {
		txBytes += a.txB[i].Load()
		rxBytes += a.rxB[i].Load()
		lost += a.lost[i].Load()
	}
	return
}

func loadAll(v []atomic.Uint64) []uint64 {
	out := make([]uint64, len(v))
	for i := range v {
		out[i] = v[i].Load()
	}
	return out
}
