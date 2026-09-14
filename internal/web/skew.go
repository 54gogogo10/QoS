package web

import (
	"sort"
	"sync"
)

// ClockEstimator 基于控制通道（HTTP 轮询）的 NTP 风格四时间戳时钟偏差估计（v2.8.1）。
//
// 每轮采样：
//
//	t1 = 请求发出前（本地时钟）
//	t2 = 服务器处理时刻（发送端时钟，/api/time 返回）
//	t3 ≈ t2（响应生成与请求处理同刻）
//	t4 = 响应到达（本地时钟）
//
//	offset = t2 − (t1+t4)/2   （路径对称假设下消除路径时延；误差 ≈ 路径不对称/2）
//	rtt    = t4 − t1
//
// 估计策略：保留最近 maxSamples 个样本，取 RTT 最小的前 25% 样本的 offset 中位数
// （NTP 过滤思想：RTT 越小越接近对称路径，抵消 TCP 拥塞/重传抖动）。
type ClockEstimator struct {
	mu         sync.Mutex
	maxSamples int
	rtt        []int64 // ns
	offset     []int64 // ns
}

// NewClockEstimator 创建估计器。
func NewClockEstimator(maxSamples int) *ClockEstimator {
	if maxSamples < 4 {
		maxSamples = 4
	}
	return &ClockEstimator{
		maxSamples: maxSamples,
		rtt:        make([]int64, 0, maxSamples),
		offset:     make([]int64, 0, maxSamples),
	}
}

// AddSample 加入一次四时间戳测量的结果（offset/rtt 单位 ns）。
// 丢弃 RTT 明显异常的样本（>10s，发送端离线误判等）。
// 并发安全：轮询 goroutine 写入，HTTP handler 读取。
func (e *ClockEstimator) AddSample(rttNs, offsetNs int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if rttNs <= 0 || rttNs > 10*1e9 {
		return
	}
	if len(e.rtt) == e.maxSamples {
		// 环形：丢弃最旧样本
		copy(e.rtt, e.rtt[1:])
		copy(e.offset, e.offset[1:])
		e.rtt = e.rtt[:e.maxSamples-1]
		e.offset = e.offset[:e.maxSamples-1]
	}
	e.rtt = append(e.rtt, rttNs)
	e.offset = append(e.offset, offsetNs)
}

// Estimate 返回当前估计：offsetNs（发送端−接收端时钟差，发送端快为正；
// 注入 stats.SetClockOffsetHint 时需取负）、rttNs（最优样本 RTT）。
// 样本不足 2 个时 ok=false。并发安全。
func (e *ClockEstimator) Estimate() (offsetNs, rttNs int64, ok bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := len(e.rtt)
	if n < 2 {
		return 0, 0, false
	}
	// 按 RTT 升序取前 25%（至少 1 个）样本
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool { return e.rtt[idx[a]] < e.rtt[idx[b]] })
	best := idx[:max(1, n/4)]
	offs := make([]int64, len(best))
	for i, j := range best {
		offs[i] = e.offset[j]
	}
	sort.Slice(offs, func(a, b int) bool { return offs[a] < offs[b] })
	return offs[len(offs)/2], e.rtt[idx[0]], true
}

// Samples 返回已收集的样本数。并发安全。
func (e *ClockEstimator) Samples() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.rtt)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
