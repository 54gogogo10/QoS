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
}

// History 是 Web 图表用的历史序列（并行数组，单位 字节/秒）。
type History struct {
	T   []int64     // unix 毫秒
	TxB [][]float64 // [flow][point]
	RxB [][]float64 // [flow][point]
}

type sample struct {
	t   time.Time
	txB []uint64
	txP []uint64
	rxB []uint64
	rxP []uint64
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
		hist: History{
			TxB: make([][]float64, nFlows),
			RxB: make([][]float64, nFlows),
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
// flowIdx 超出 [0, nFlows) 时直接忽略（调用方 bug 防御，不 panic）。
func (a *Aggregator) RecordRx(flowIdx int, bytes uint64, seq uint32) {
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
}

// Snapshot 记录一个采样点、计算速率并追加 Web 历史（报告循环调用）。
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
	}
	if len(a.hist.T) > a.histCap {
		drop := len(a.hist.T) - a.histCap
		a.hist.T = a.hist.T[drop:]
		for i := 0; i < a.nFlows; i++ {
			a.hist.TxB[i] = a.hist.TxB[i][drop:]
			a.hist.RxB[i] = a.hist.RxB[i][drop:]
		}
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
	}
	for i := 0; i < a.nFlows; i++ {
		out.TxB[i] = append([]float64(nil), a.hist.TxB[i]...)
		out.RxB[i] = append([]float64(nil), a.hist.RxB[i]...)
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
