package stats

import (
	"math"
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
	Reordered uint64  // 乱序到达包数（v2.9.0：到达时 seq 已小于当前最大已见 seq，不含孔内重复包）

	// 时延/抖动（v2.7.0）：DelayValid=false 表示发送端无时间戳（旧版本），其余字段为 0。
	DelayValid   bool     // 是否收到过带时间戳的包
	DelayAvgMs   float64  // 最近采样窗口（100ms）平均时延（毫秒）
	DelayTotAvgMs float64 // 全程累计平均时延（毫秒，报告/最终判定用）
	DelayMinMs   float64 // 全程最小（毫秒，校正后）
	DelayMaxMs   float64 // 全程最大（毫秒，校正后）
	JitterMs     float64 // RFC3550 抖动（毫秒，累计）
	DelayP95Ms   float64 // 全程 p95 时延（毫秒，v2.8.0，1ms 直方图桶下界精度）
	DelayP99Ms   float64 // 全程 p99 时延（毫秒，v2.8.0）
	ClockOffsetMs float64 // v2.8.1 估计的时钟偏差（毫秒）：未钳制差值的最小值；双机无同步时≈两机时钟差，单机≈最小时延
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

// 乱序容忍窗口（v2.8.0）：seq 空洞不立即计丢包，先跟踪 reorderGrace 时长，
// 宽限期内乱序包到达并补齐空洞则不计丢包；超时未补齐才计丢包。
const (
	reorderWin   = 64                     // 单孔最大跟踪宽度（更大的空洞直接计丢包）
	reorderGrace = 200 * time.Millisecond // 孔洞保留时长
)

// 时延百分位直方图（v2.8.0）：线性 1ms 桶覆盖 [0, delayHistMaxMs)，末桶为溢出桶。
// 桶精度 1ms（报告桶下界）；溢出桶样本用全程最大时延代表。
const (
	delayHistMaxMs    = 2000
	delayHistOverflow = delayHistMaxMs
	delayHistBuckets  = delayHistMaxMs + 1
)

// delayState 是单条流的时延/乱序统计状态，由 delayMu[flowIdx] 保护。
// 窗口（win*）在每次 Snapshot 时清零；累计（tot*、min/max、jitter、hist）跨窗口保留。
type delayState struct {
	valid      bool
	winSumNs   uint64 // 窗口内时延和（ns）
	winCount   uint64
	totSumNs   uint64 // 全程时延和（ns）
	totCount   uint64
	delayMinNs int64 // 全程最小（ns，校正后）
	delayMaxNs int64 // 全程最大（ns，校正后）
	minRawNs   int64 // 未钳制差值的最小值（ns）：时钟偏差+最小时延的估计，用于校正
	hintSet    bool   // 是否已注入外部时钟偏差估计（v2.8.1）
	prevDelay  int64 // 上一包时延（ns，未校正）
	prevSet    bool
	jitterNs   float64 // RFC3550 抖动（ns）
	hist       [delayHistBuckets]uint64 // 全程时延直方图（v2.8.0，1ms 桶，末桶为溢出）

	// 乱序孔洞（v2.8.0）：seq 空洞先跟踪不立即计丢包，宽限期内乱序包补齐则不计。
	haveHole   bool
	holeStart  uint32    // 孔洞起始 seq
	holeEnd    uint32    // 孔洞结束 seq
	holeBits   uint64    // 孔洞内已补齐的位图（holeStart+i 对应 bit i）
	holeRemain uint8     // 孔洞内仍未到达的包数
	holeSince  time.Time // 孔洞创建时刻（超 reorderGrace 未补齐 → 计丢包）
}

const sampleWindow = time.Second

// Aggregator 汇总所有流的计数并计算实时速率。
// 并发安全：RecordTx 多 goroutine；RecordRx 单 goroutine（抓包线程）；
// Snapshot/Current/History 可并发。
type Aggregator struct {
	nFlows   int
	txPkts   []atomic.Uint64
	txB      []atomic.Uint64
	rxPkts   []atomic.Uint64
	rxB      []atomic.Uint64
	lost     []atomic.Uint64
	reorder  []atomic.Uint64 // 乱序到达包数（v2.9.0）
	lastSeq  []atomic.Uint32

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
		reorder: make([]atomic.Uint64, nFlows),
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
// 乱序容忍（v2.8.0）：seq 大于 lastSeq 时中间缺失的序号先记入待定孔洞（≤ reorderWin 个），
// 宽限期内到达的乱序包逐个补齐孔洞不计丢包；超时未补齐、或孔洞被更大的空洞越过时，
// 剩余缺失才计为丢失；超过窗口宽度的大空洞立即计丢失。
// sendTs>0 时更新时延统计（sendTs==0 表示旧版发送端，无时间戳）；recv 为捕获时刻。
// flowIdx 超出 [0, nFlows) 时直接忽略（调用方 bug 防御，不 panic）。
func (a *Aggregator) RecordRx(flowIdx int, bytes uint64, seq uint32, recv time.Time, sendTs int64) {
	if flowIdx < 0 || flowIdx >= a.nFlows {
		return
	}
	a.rxPkts[flowIdx].Add(1)
	a.rxB[flowIdx].Add(bytes)
	a.delayMu[flowIdx].Lock()
	st := &a.delay[flowIdx]
	last := a.lastSeq[flowIdx].Load()

	// 过期孔洞先结算：乱序包晚到超过宽限期 → 计丢包。
	// 注意宽限期边界：孔洞结算后到达的极晚乱序包仍计入 rx（rxPackets）但不再
	// 回冲丢失计数，丢包率在该边界上可能高估至多 1 包/孔，属可接受的设计取舍。
	if st.haveHole && recv.Sub(st.holeSince) > reorderGrace {
		a.lost[flowIdx].Add(uint64(st.holeRemain))
		st.haveHole = false
	}

	if seq > last {
		if last != 0 && seq > last+1 {
			gap := seq - last - 1 // 缺失包数
			if st.haveHole {
				// 已有孔洞又被越过：旧孔不可能再被补齐，先结算
				a.lost[flowIdx].Add(uint64(st.holeRemain))
				st.haveHole = false
			}
			if gap <= reorderWin {
				// 小空洞：跟踪等待乱序包补齐
				st.holeStart, st.holeEnd = last+1, seq-1
				st.holeBits, st.holeRemain, st.holeSince, st.haveHole = 0, uint8(gap), recv, true
			} else {
				a.lost[flowIdx].Add(uint64(gap)) // 大空洞直接计丢包
			}
		}
		a.lastSeq[flowIdx].Store(seq)
	} else if seq < last {
		// 乱序到达（v2.9.0）：seq 已小于当前最大已见序号。
		// 孔内重复包（位图已置位）不算乱序；孔外晚到包与补孔包均计。
		// 孔洞关闭后到达的重复包无法与极晚乱序包区分（无历史跟踪），按乱序计。
		dup := false
		if st.haveHole && seq >= st.holeStart && seq <= st.holeEnd {
			// 乱序/重传包补齐孔洞
			bit := uint64(1) << (seq - st.holeStart)
			if st.holeBits&bit == 0 {
				st.holeBits |= bit
				st.holeRemain--
				if st.holeRemain == 0 {
					st.haveHole = false
				}
			} else {
				dup = true // 孔内重复包
			}
		}
		if !dup {
			a.reorder[flowIdx].Add(1)
		}
	}

	if sendTs > 0 {
		// 时钟偏差估计与校正（v2.8.1）：rawD = 真实时延 + 时钟偏差。
		// 全程最小 rawD 即偏差+最小时延的估计（LAN 最小时延≈0），
		// 校正后 d = rawD − minRaw 消除固定偏差 → 双机场景时延也有意义。
		// 抖动用未钳制的原始差值计算：RFC3550 抖动是相邻包时延差，固定时钟偏移
		// 在差中抵消，不应钳制（否则双机时钟差会把抖动也打成 0）。
		rawD := recv.UnixNano() - sendTs
		if !st.valid {
			if st.hintSet {
				// 已有外部偏差基准（控制通道测量）：只接受更小的观测值
				if rawD < st.minRawNs {
					st.minRawNs = rawD
				}
			} else {
				st.minRawNs = rawD
			}
		} else if rawD < st.minRawNs {
			st.minRawNs = rawD
		}
		d := rawD - st.minRawNs
		if d < 0 {
			d = 0 // 时钟漂移导致瞬时负值，钳 0
		}
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
		// 时延直方图（1ms 桶 + 溢出桶），百分位统计用（v2.8.0）
		if ms := d / int64(time.Millisecond); ms >= delayHistMaxMs {
			st.hist[delayHistOverflow]++
		} else {
			st.hist[ms]++
		}
		if st.prevSet {
			diff := st.prevDelay - rawD
			if diff < 0 {
				diff = -diff
			}
			st.jitterNs += (float64(diff) - st.jitterNs) / 16 // RFC3550: J += (|D(i-1,i)| - J)/16
		}
		st.prevDelay, st.prevSet = rawD, true
	}
	a.delayMu[flowIdx].Unlock()
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
		s := FlowSnapshot{
			FlowIdx:   i,
			TxPackets: txP,
			TxBytes:   txB,
			RxPackets: rxP,
			RxBytes:   rxB,
		}
		if hasSample && dt > 0 {
			s.TxBps = float64(txB-oldest.txB[i]) / dt
			s.TxPps = float64(txP-oldest.txP[i]) / dt
			s.RxBps = float64(rxB-oldest.rxB[i]) / dt
			s.RxPps = float64(rxP-oldest.rxP[i]) / dt
		}
		// 时延字段 + 孔洞过期结算：短临界区（快照线程与抓包线程共享）
		a.delayMu[i].Lock()
		st := &a.delay[i]
		if st.haveHole && now.Sub(st.holeSince) > reorderGrace {
			a.lost[i].Add(uint64(st.holeRemain))
			st.haveHole = false
		}
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
			s.DelayP95Ms = st.percentileMs(0.95)
			s.DelayP99Ms = st.percentileMs(0.99)
			s.ClockOffsetMs = float64(st.minRawNs) / 1e6
		}
		a.delayMu[i].Unlock()
		lost := a.lost[i].Load()
		s.Lost = lost
		s.Reordered = a.reorder[i].Load()
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
		Dly: make([][]float64, a.nFlows),
	}
	for i := 0; i < a.nFlows; i++ {
		out.TxB[i] = append([]float64(nil), a.hist.TxB[i]...)
		out.RxB[i] = append([]float64(nil), a.hist.RxB[i]...)
		out.Dly[i] = append([]float64(nil), a.hist.Dly[i]...)
	}
	return out
}

// percentileMs 返回全程时延直方图的 p 分位（毫秒，桶下界精度 1ms）。
// 无样本返回 -1；溢出桶（≥ delayHistMaxMs）样本用全程最大时延代表。
// 调用方必须持有 delayMu[flowIdx]。
func (st *delayState) percentileMs(p float64) float64 {
	if st.totCount == 0 {
		return -1
	}
	rank := uint64(math.Ceil(p * float64(st.totCount))) // 最邻近秩
	if rank < 1 {
		rank = 1
	}
	if rank > st.totCount {
		rank = st.totCount
	}
	var cum uint64
	for i, n := range st.hist {
		cum += n
		if cum >= rank {
			if i < delayHistMaxMs {
				return float64(i)
			}
			return float64(st.delayMaxNs) / 1e6
		}
	}
	return float64(st.delayMaxNs) / 1e6
}

// DelayHistogram 返回每流全程时延直方图快照（v2.9.0，HTML 报告用）：
// 1ms 桶 × delayHistBuckets，末桶为 ≥2000ms 溢出桶；无样本的流为 nil。
// 拷贝返回，调用方可自由持有（与继续到达的包互不影响）。
func (a *Aggregator) DelayHistogram() [][]uint64 {
	out := make([][]uint64, a.nFlows)
	for i := 0; i < a.nFlows; i++ {
		a.delayMu[i].Lock()
		if a.delay[i].totCount > 0 {
			h := make([]uint64, delayHistBuckets)
			copy(h, a.delay[i].hist[:])
			out[i] = h
		}
		a.delayMu[i].Unlock()
	}
	return out
}

// SetClockOffsetHint 测试开始前注入外部时钟偏差估计（v2.8.1）：
// 控制通道（HTTP 四时间戳）在测试流量未开始时即可测得两机时钟差，
// 注入后作为时延校正基准（与测试流 min 法取更小值，保守融合）。
// 仅在尚无时延样本时生效。
func (a *Aggregator) SetClockOffsetHint(hintNs int64) {
	for i := 0; i < a.nFlows; i++ {
		a.delayMu[i].Lock()
		st := &a.delay[i]
		if !st.valid && !st.hintSet {
			st.hintSet = true
			st.minRawNs = hintNs
		}
		a.delayMu[i].Unlock()
	}
}

// Finalize 停止时调用：所有 RecordRx 结束后（抓包已停、无新包到达），
// 把宽限期内仍未补齐的孔洞直接计为丢包，保证最终统计不遗漏。
// controller 在抓包 goroutine 退出后、生成最终统计前调用。
func (a *Aggregator) Finalize() {
	for i := 0; i < a.nFlows; i++ {
		a.delayMu[i].Lock()
		if a.delay[i].haveHole {
			a.lost[i].Add(uint64(a.delay[i].holeRemain))
			a.delay[i].haveHole = false
		}
		a.delayMu[i].Unlock()
	}
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
