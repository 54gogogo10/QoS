package stats

import (
	"math"
	"testing"
	"time"
)

func TestLossDetection(t *testing.T) {
	a := NewAggregator(1, 10)
	t0 := time.Now()
	a.RecordRx(0, 100, 1, t0, 0)
	a.RecordRx(0, 100, 2, t0.Add(time.Millisecond), 0)
	a.RecordRx(0, 100, 7, t0.Add(2*time.Millisecond), 0) // 3,4,5,6 缺失
	snap := a.Current(t0.Add(3 * time.Second))            // 超过乱序宽限期 → 计丢包
	if snap[0].Lost != 4 {
		t.Fatalf("Lost = %d, want 4", snap[0].Lost)
	}
	if snap[0].RxPackets != 3 {
		t.Fatalf("RxPackets = %d, want 3", snap[0].RxPackets)
	}
	if math.Abs(snap[0].LossRate-4.0/7.0) > 1e-9 {
		t.Fatalf("LossRate = %v, want %v", snap[0].LossRate, 4.0/7.0)
	}
}

// TestReorderNotCountedAsLoss 乱序容忍：空洞在宽限期内被乱序包补齐 → 不计丢包。
func TestReorderNotCountedAsLoss(t *testing.T) {
	a := NewAggregator(1, 10)
	t0 := time.Now()
	a.RecordRx(0, 100, 1, t0, 0)
	a.RecordRx(0, 100, 2, t0, 0)
	a.RecordRx(0, 100, 5, t0, 0) // 空洞 3,4
	a.RecordRx(0, 100, 3, t0, 0) // 乱序补齐
	a.RecordRx(0, 100, 4, t0, 0) // 补齐完成 → 空洞撤销
	a.RecordRx(0, 100, 6, t0, 0)
	snap := a.Current(t0.Add(time.Second))
	if snap[0].Lost != 0 {
		t.Fatalf("Lost = %d, want 0（乱序补齐不计丢包）", snap[0].Lost)
	}
}

// TestHoleExpiresAfterGrace 宽限期内未补齐的孔洞 → 计丢包；迟到的乱序包不再计入。
func TestHoleExpiresAfterGrace(t *testing.T) {
	a := NewAggregator(1, 10)
	t0 := time.Now()
	a.RecordRx(0, 100, 1, t0, 0)
	a.RecordRx(0, 100, 2, t0, 0)
	a.RecordRx(0, 100, 5, t0, 0)                                                      // 空洞 3,4
	a.RecordRx(0, 100, 3, t0.Add(reorderGrace+50*time.Millisecond), 0)                // 迟到超过宽限期
	snap := a.Current(t0)
	if snap[0].Lost != 2 {
		t.Fatalf("Lost = %d, want 2（孔洞超时计丢包）", snap[0].Lost)
	}
	if snap[0].RxPackets != 4 {
		t.Fatalf("RxPackets = %d, want 4（迟到包仍计入 RX）", snap[0].RxPackets)
	}
}

// TestLateFillWithinGrace 宽限期内补齐的孔洞 → 不计丢包（即使查询时间已过宽限期）。
func TestLateFillWithinGrace(t *testing.T) {
	a := NewAggregator(1, 10)
	t0 := time.Now()
	a.RecordRx(0, 100, 1, t0, 0)
	a.RecordRx(0, 100, 2, t0, 0)
	a.RecordRx(0, 100, 5, t0, 0) // 空洞 3,4
	a.RecordRx(0, 100, 3, t0.Add(50*time.Millisecond), 0)
	a.RecordRx(0, 100, 4, t0.Add(100*time.Millisecond), 0) // 补齐
	snap := a.Current(t0.Add(time.Second))
	if snap[0].Lost != 0 {
		t.Fatalf("Lost = %d, want 0（宽限期内补齐不计丢包）", snap[0].Lost)
	}
}

// TestLargeGapImmediateLoss 超过跟踪窗口的大空洞 → 立即计丢包。
func TestLargeGapImmediateLoss(t *testing.T) {
	a := NewAggregator(1, 10)
	t0 := time.Now()
	a.RecordRx(0, 100, 1, t0, 0)
	a.RecordRx(0, 100, 2, t0.Add(time.Millisecond), 0)
	a.RecordRx(0, 100, 100, t0.Add(2*time.Millisecond), 0) // 空洞 3..99 = 97 包 > 64
	snap := a.Current(t0)
	if snap[0].Lost != 97 {
		t.Fatalf("Lost = %d, want 97（大空洞立即计丢包）", snap[0].Lost)
	}
}

// TestSecondHoleSettlesFirst 已有孔洞又被更大的空洞越过：旧孔立即结算，新孔继续跟踪。
func TestSecondHoleSettlesFirst(t *testing.T) {
	a := NewAggregator(1, 10)
	t0 := time.Now()
	a.RecordRx(0, 100, 1, t0, 0)
	a.RecordRx(0, 100, 2, t0, 0)
	a.RecordRx(0, 100, 5, t0, 0)  // 孔洞 3,4
	a.RecordRx(0, 100, 20, t0, 0) // 越过旧孔 → 结算 2；新孔 6..19
	a.RecordRx(0, 100, 6, t0, 0)  // 补齐 6
	a.RecordRx(0, 100, 7, t0, 0)  // 补齐 7
	snap := a.Current(t0.Add(time.Second))
	if snap[0].Lost != 14 { // 旧孔 2 + 新孔 12（14 个中补齐 2 个）
		t.Fatalf("Lost = %d, want 14", snap[0].Lost)
	}
}

// TestClockOffsetHint 外部时钟偏差注入（v2.8.1）：测试前注入控制通道估计值，
// 首包不直接覆盖（取更小值），时延校正从测试一开始就基于外部基准。
func TestClockOffsetHint(t *testing.T) {
	a := NewAggregator(1, 10)
	hint := int64(-60) * 1e9 // 控制通道测得：接收端慢 60s
	a.SetClockOffsetHint(hint)
	base := time.Now()
	// 真实时延 10/30ms 交替；模拟接收端时钟慢 60s（rawD = -60s + 时延）
	const skew = 60 * time.Second
	delays := []int64{10, 30}
	for i := 0; i < 20; i++ {
		recv := base.Add(time.Duration(i)*5*time.Millisecond).Add(-skew)
		send := recv.Add(skew).Add(-time.Duration(delays[i%2]) * time.Millisecond)
		a.RecordRx(0, 100, uint32(i+1), recv, send.UnixNano())
	}
	s := a.Current(base.Add(100 * time.Millisecond))[0]
	// 基准 = min(hint, 首包 rawD)：hint(-60s) < rawD(-60s+10ms) → 保留 hint
	if math.Abs(s.ClockOffsetMs-(-60000)) > 1 {
		t.Fatalf("ClockOffsetMs = %v, want ≈-60000（外部基准优先）", s.ClockOffsetMs)
	}
	// 校正后时延 = 10/30ms（外部基准已精确消除偏差，不再是 min 法的 0/20）
	if math.Abs(s.DelayAvgMs-20) > 0.5 {
		t.Fatalf("avg = %v, want ≈20（外部基准校正后保留真实时延）", s.DelayAvgMs)
	}
}

// TestFinalizeFlushesHoles 停止时强制结算未补齐孔洞。
func TestFinalizeFlushesHoles(t *testing.T) {
	a := NewAggregator(1, 10)
	t0 := time.Now()
	a.RecordRx(0, 100, 1, t0, 0)
	a.RecordRx(0, 100, 2, t0, 0)
	a.RecordRx(0, 100, 5, t0, 0) // 空洞 3,4（宽限期未到）
	a.Finalize()
	snap := a.Current(t0)
	if snap[0].Lost != 2 {
		t.Fatalf("Finalize 后 Lost = %d, want 2", snap[0].Lost)
	}
	a.RecordRx(0, 100, 3, t0, 0) // Finalize 后到达的乱序包不再影响统计
	if snap := a.Current(t0); snap[0].Lost != 2 {
		t.Fatalf("Finalize 后迟到包不应改变 Lost = %d, want 2", snap[0].Lost)
	}
}

func TestRateSlidingWindow(t *testing.T) {
	a := NewAggregator(1, 10)
	t0 := time.Now()
	a.RecordTx(0, 100, 100000) // 100KB
	s1 := a.Snapshot(t0)
	if s1[0].TxBps != 0 {
		t.Fatalf("首个快照 TxBps = %v, want 0", s1[0].TxBps)
	}
	a.RecordTx(0, 100, 100000) // 累计 200KB
	s2 := a.Snapshot(t0.Add(time.Second))
	if math.Abs(s2[0].TxBps-100000) > 1 {
		t.Fatalf("TxBps = %v, want 100000", s2[0].TxBps)
	}
	if s2[0].TxPackets != 200 || s2[0].TxBytes != 200000 {
		t.Fatalf("累计计数错误: %+v", s2[0])
	}
}

func TestHistoryCap(t *testing.T) {
	a := NewAggregator(2, 5)
	t0 := time.Now()
	for i := 0; i < 7; i++ {
		a.RecordTx(0, 10, 100)
		a.Snapshot(t0.Add(time.Duration(i) * 100 * time.Millisecond))
	}
	h := a.History()
	if len(h.T) != 5 {
		t.Fatalf("history len = %d, want 5", len(h.T))
	}
	if len(h.TxB[0]) != 5 || len(h.TxB[1]) != 5 {
		t.Fatalf("历史序列长度错误: %d %d", len(h.TxB[0]), len(h.TxB[1]))
	}
	// 容量裁剪后最旧时间戳是第 3 个快照
	if h.T[0] != t0.Add(2*100*time.Millisecond).UnixMilli() {
		t.Fatalf("最旧时间戳 = %d, want %d", h.T[0], t0.Add(200*time.Millisecond).UnixMilli())
	}
}

func TestRecordOutOfRangeFlowIdx(t *testing.T) {
	a := NewAggregator(1, 10)
	a.RecordTx(99, 1, 1)
	a.RecordRx(99, 1, 1, time.Now(), 0)
	a.RecordTx(-1, 1, 1)
	a.RecordRx(-1, 1, 1, time.Now(), 0)
	if tx, rx, lost := a.Totals(); tx != 0 || rx != 0 || lost != 0 {
		t.Fatalf("越界 flowIdx 不应计入统计: Totals = %d %d %d", tx, rx, lost)
	}
}

func TestTotals(t *testing.T) {
	a := NewAggregator(2, 10)
	a.RecordTx(0, 5, 500)
	a.RecordTx(1, 3, 300)
	t0 := time.Now()
	a.RecordRx(1, 100, 1, t0, 0)
	a.RecordRx(1, 100, 4, t0, 0) // 丢 2 个
	a.Current(t0.Add(time.Second)) // 孔洞超宽限期 → 结算（Totals 只汇总已结算的丢失）
	tx, rx, lost := a.Totals()
	if tx != 800 || rx != 200 || lost != 2 {
		t.Fatalf("Totals = %d %d %d", tx, rx, lost)
	}
}
func TestSeqWraparound(t *testing.T) {
	a := NewAggregator(1, 10)
	a.RecordRx(0, 100, math.MaxUint32-1, time.Now(), 0)
	a.RecordRx(0, 100, math.MaxUint32, time.Now(), 0)
	a.RecordRx(0, 100, 1, time.Now(), 0) // 回绕
	snap := a.Current(time.Now())
	if snap[0].Lost != 0 {
		t.Fatalf("回绕后 Lost = %d, want 0（已知限制：seq 回绕不计丢失）", snap[0].Lost)
	}
}

func TestDelayStats(t *testing.T) {
	a := NewAggregator(1, 10)
	base := time.Now()
	// 三包时延：10ms、20ms、30ms（发送时刻统一在接收前 20ms，接收时刻递推）
	// v2.8.1 时钟偏差校正：min raw=10ms → 校正后 0/10/20
	a.RecordRx(0, 100, 1, base.Add(-10*time.Millisecond), base.Add(-20*time.Millisecond).UnixNano())
	a.RecordRx(0, 100, 2, base, base.Add(-20*time.Millisecond).UnixNano())
	a.RecordRx(0, 100, 3, base.Add(10*time.Millisecond), base.Add(-20*time.Millisecond).UnixNano())
	s := a.Current(base.Add(10 * time.Millisecond))[0]
	if !s.DelayValid {
		t.Fatal("DelayValid = false, want true")
	}
	if math.Abs(s.DelayAvgMs-10) > 1e-6 || math.Abs(s.DelayTotAvgMs-10) > 1e-6 {
		t.Fatalf("avg = %v/%v, want 10（校正后）", s.DelayAvgMs, s.DelayTotAvgMs)
	}
	if math.Abs(s.DelayMinMs-0) > 1e-6 || math.Abs(s.DelayMaxMs-20) > 1e-6 {
		t.Fatalf("min/max = %v/%v, want 0/20（校正后）", s.DelayMinMs, s.DelayMaxMs)
	}
	if math.Abs(s.ClockOffsetMs-10) > 1e-6 {
		t.Fatalf("ClockOffsetMs = %v, want 10（min raw 差值）", s.ClockOffsetMs)
	}
}

func TestDelayWindowReset(t *testing.T) {
	a := NewAggregator(1, 10)
	base := time.Now()
	// 第一窗口：时延 10ms → 校正后 0
	a.RecordRx(0, 100, 1, base, base.Add(-10*time.Millisecond).UnixNano())
	a.Snapshot(base)
	// 第二窗口：时延 40ms → 校正后 30（窗口 avg 应变 30，累计 avg 变 15）
	a.RecordRx(0, 100, 2, base.Add(time.Second), base.Add(time.Second).Add(-40*time.Millisecond).UnixNano())
	s := a.Snapshot(base.Add(time.Second))[0]
	if math.Abs(s.DelayAvgMs-30) > 1e-6 {
		t.Fatalf("窗口 avg = %v, want 30（校正后）", s.DelayAvgMs)
	}
	if math.Abs(s.DelayTotAvgMs-15) > 1e-6 {
		t.Fatalf("累计 avg = %v, want 15（校正后）", s.DelayTotAvgMs)
	}
	if math.Abs(s.DelayMinMs-0) > 1e-6 || math.Abs(s.DelayMaxMs-30) > 1e-6 {
		t.Fatalf("min/max = %v/%v, want 0/30（校正后）", s.DelayMinMs, s.DelayMaxMs)
	}
}

func TestDelayJitterRFC3550(t *testing.T) {
	a := NewAggregator(1, 10)
	base := time.Now()
	// 时延序列 10、30、10、30… 交替 → 相邻差恒 20 → 抖动指数收敛到 ~20ms
	delays := []int64{10, 30}
	for i := 0; i < 60; i++ {
		recv := base.Add(time.Duration(i) * 5 * time.Millisecond)
		send := recv.Add(-time.Duration(delays[i%2]) * time.Millisecond)
		a.RecordRx(0, 100, uint32(i+1), recv, send.UnixNano())
	}
	s := a.Current(base.Add(300 * time.Millisecond))[0]
	if math.Abs(s.JitterMs-20) > 1.5 {
		t.Fatalf("jitter = %v, want ~20", s.JitterMs)
	}
}

// TestJitterSurvivesClockSkew 回归（v2.8.0）：双机时钟差（接收端时钟比发送端慢）时，
// RFC3550 抖动仍必须有效（用未钳制差值，固定偏移在相邻差中抵消）；
// v2.8.1 起时延同时被时钟偏差校正（min raw 差值），不再是钳制的 0。
func TestJitterSurvivesClockSkew(t *testing.T) {
	a := NewAggregator(1, 10)
	base := time.Now()
	// 模拟接收端时钟慢 60s：真实时延 10/30ms 交替，raw 差值恒为负
	const skew = 60 * time.Second
	delays := []int64{10, 30}
	for i := 0; i < 60; i++ {
		recv := base.Add(time.Duration(i)*5*time.Millisecond).Add(-skew)
		send := recv.Add(skew).Add(-time.Duration(delays[i%2]) * time.Millisecond)
		a.RecordRx(0, 100, uint32(i+1), recv, send.UnixNano())
	}
	s := a.Current(base.Add(300 * time.Millisecond))[0]
	if !s.DelayValid {
		t.Fatal("DelayValid 应为 true（有带时间戳的包）")
	}
	// 时钟偏差估计 ≈ -60s + 最小时延 10ms = -59990ms（接收端慢 60s）
	if math.Abs(s.ClockOffsetMs-(-59990)) > 1 {
		t.Fatalf("ClockOffsetMs = %v, want ≈-59990（接收端慢 60s + 最小时延 10ms）", s.ClockOffsetMs)
	}
	// 校正后时延 = 0/20ms 交替 → avg ≈ 10ms（不再是钳制的 0）
	if math.Abs(s.DelayAvgMs-10) > 0.5 {
		t.Fatalf("校正后 avg = %v, want ≈10", s.DelayAvgMs)
	}
	if math.Abs(s.JitterMs-20) > 1.5 {
		t.Fatalf("时钟偏移下 jitter = %v, want ~20（抖动应抗固定偏移）", s.JitterMs)
	}
}

func TestDelayLegacyNoTs(t *testing.T) {
	a := NewAggregator(1, 10)
	a.RecordRx(0, 100, 1, time.Now(), 0) // 旧发送端：sendTs=0
	s := a.Current(time.Now())[0]
	if s.DelayValid {
		t.Fatal("旧包 DelayValid 应为 false")
	}
	if s.DelayAvgMs != 0 || s.JitterMs != 0 {
		t.Fatalf("旧包时延应全 0, got %+v", s)
	}
}

func TestDelayHistory(t *testing.T) {
	a := NewAggregator(1, 10)
	base := time.Now()
	a.RecordRx(0, 100, 1, base, base.Add(-5*time.Millisecond).UnixNano())
	a.Snapshot(base)
	h := a.History()
	if len(h.Dly) != 1 || len(h.Dly[0]) != 1 {
		t.Fatalf("Dly = %+v", h.Dly)
	}
	// 单包时延 5ms → 校正后 0（自身即基准）
	if math.Abs(h.Dly[0][0]-0) > 1e-6 {
		t.Fatalf("Dly[0][0] = %v, want 0（单包校正后）", h.Dly[0][0])
	}
	// 无时间戳包 → -1 哨兵
	a2 := NewAggregator(1, 10)
	a2.RecordRx(0, 100, 1, time.Now(), 0)
	a2.Snapshot(time.Now())
	if h2 := a2.History(); h2.Dly[0][0] != -1 {
		t.Fatalf("无效时延应记 -1, got %v", h2.Dly[0][0])
	}
}

// TestDelayPercentiles 百分位：样本 [10,20,30]ms → 校正后 [0,10,20] → p95=p99=20（最邻近秩，桶下界）。
func TestDelayPercentiles(t *testing.T) {
	a := NewAggregator(1, 10)
	base := time.Now()
	for i, d := range []int64{10, 20, 30} {
		recv := base.Add(time.Duration(i) * time.Millisecond)
		a.RecordRx(0, 100, uint32(i+1), recv, recv.Add(-time.Duration(d)*time.Millisecond).UnixNano())
	}
	s := a.Current(base.Add(time.Second))[0]
	if !s.DelayValid {
		t.Fatal("DelayValid = false, want true")
	}
	if math.Abs(s.DelayP95Ms-20) > 1e-6 || math.Abs(s.DelayP99Ms-20) > 1e-6 {
		t.Fatalf("p95/p99 = %v/%v, want 20/20（校正后）", s.DelayP95Ms, s.DelayP99Ms)
	}
}

// TestDelayPercentileBucketFloor 非整毫秒样本取桶下界：20.9ms → 桶 20（校正后 10.9 → 桶 10）。
func TestDelayPercentileBucketFloor(t *testing.T) {
	a := NewAggregator(1, 10)
	base := time.Now()
	delays := []int64{10000, 20900, 30000} // 10ms、20.9ms、30ms（微秒）
	for i, d := range delays {
		recv := base.Add(time.Duration(i) * time.Millisecond)
		a.RecordRx(0, 100, uint32(i+1), recv, recv.Add(-time.Duration(d)*time.Microsecond).UnixNano())
	}
	s := a.Current(base.Add(time.Second))[0]
	if math.Abs(s.DelayP95Ms-20) > 1e-6 { // 校正后 0/10.9/20 → rank=3 → 桶 20
		t.Fatalf("p95 = %v, want 20（校正后）", s.DelayP95Ms)
	}
}

// TestDelayPercentileOverflow 超过直方图上限的样本进溢出桶，用全程最大时延代表。
func TestDelayPercentileOverflow(t *testing.T) {
	a := NewAggregator(1, 10)
	base := time.Now()
	recv := base
	a.RecordRx(0, 100, 1, recv, recv.Add(-10*time.Millisecond).UnixNano())
	recv2 := base.Add(2 * time.Second)
	a.RecordRx(0, 100, 2, recv2, recv2.Add(-2500*time.Millisecond).UnixNano()) // 2500ms > 2000ms
	s := a.Current(base.Add(3 * time.Second))[0]
	if math.Abs(s.DelayP95Ms-2490) > 1e-6 { // 校正后 0/2490 → p95 在溢出桶，用最大 2490 代表
		t.Fatalf("p95 = %v, want 2490（校正后，溢出桶用最大时延代表）", s.DelayP95Ms)
	}
	if math.Abs(s.DelayMaxMs-2490) > 1e-6 {
		t.Fatalf("max = %v, want 2490（校正后）", s.DelayMaxMs)
	}
}

// TestDelayPercentileNoData 无时延样本时百分位为 0（与无效字段一致）。
func TestDelayPercentileNoData(t *testing.T) {
	a := NewAggregator(1, 10)
	a.RecordRx(0, 100, 1, time.Now(), 0) // 旧发送端：无时间戳
	s := a.Current(time.Now())[0]
	if s.DelayValid {
		t.Fatal("旧包 DelayValid 应为 false")
	}
	if s.DelayP95Ms != 0 || s.DelayP99Ms != 0 {
		t.Fatalf("无样本百分位应 0, got %v/%v", s.DelayP95Ms, s.DelayP99Ms)
	}
}

// TestReorderCounting 乱序包统计（v2.9.0）：
// 乱序 = 到达时 seq 已小于当前最大已见 seq；孔内重复包与最新包重复不计。
// 注：孔洞关闭后到达的重复包无法与极晚乱序包区分（无历史跟踪），按乱序计。
func TestReorderCounting(t *testing.T) {
	a := NewAggregator(1, 10)
	t0 := time.Now()
	// 顺序到达 1..3：无乱序
	for seq := uint32(1); seq <= 3; seq++ {
		a.RecordRx(0, 100, seq, t0, 0)
	}
	a.RecordRx(0, 100, 6, t0, 0) // 空洞 [4,5]，last=6
	a.RecordRx(0, 100, 4, t0, 0) // 补齐 4 → 乱序 1
	a.RecordRx(0, 100, 4, t0, 0) // 孔内重复 → 不计
	a.RecordRx(0, 100, 5, t0, 0) // 补齐 5 → 乱序 2，孔洞关闭
	a.RecordRx(0, 100, 6, t0, 0) // 最新包重复（seq==last）→ 不计
	a.RecordRx(0, 100, 2, t0, 0) // 极晚乱序包 → 乱序 3
	snap := a.Current(t0)
	if snap[0].Reordered != 3 {
		t.Fatalf("Reordered = %d, want 3（补齐 4/5 + 极晚包 2）", snap[0].Reordered)
	}
	if snap[0].RxPackets != 9 {
		t.Fatalf("RxPackets = %d, want 9", snap[0].RxPackets)
	}
}

// TestReorderCountingHoleExpired 孔洞结算后到达的迟到包仍计乱序（计入 RX、不回冲丢包）。
func TestReorderCountingHoleExpired(t *testing.T) {
	a := NewAggregator(1, 10)
	t0 := time.Now()
	a.RecordRx(0, 100, 1, t0, 0)
	a.RecordRx(0, 100, 2, t0, 0)
	a.RecordRx(0, 100, 5, t0, 0) // 空洞 3,4
	// 超过宽限期后 3 到达：孔洞已结算为丢包，3 计乱序
	a.RecordRx(0, 100, 3, t0.Add(reorderGrace+50*time.Millisecond), 0)
	snap := a.Current(t0.Add(time.Second))
	if snap[0].Reordered != 1 {
		t.Fatalf("Reordered = %d, want 1", snap[0].Reordered)
	}
}

// TestDelayHistogramExport DelayHistogram 导出（v2.9.0）：
// 有样本流返回拷贝、无样本流为 nil；导出后继续到达的包不影响已导出快照。
func TestDelayHistogramExport(t *testing.T) {
	a := NewAggregator(2, 10)
	t0 := time.Now()
	for i := 0; i < 10; i++ {
		a.RecordRx(0, 100, uint32(i+1), t0.Add(time.Duration(i)*time.Millisecond), t0.Add(time.Duration(i)*time.Millisecond).UnixNano())
	}
	h := a.DelayHistogram()
	if h[0] == nil || len(h[0]) != delayHistBuckets {
		t.Fatalf("流 0 应返回 %d 桶直方图", delayHistBuckets)
	}
	var sum uint64
	for _, n := range h[0] {
		sum += n
	}
	if sum != 10 {
		t.Fatalf("样本和 = %d, want 10", sum)
	}
	if h[1] != nil {
		t.Fatalf("无样本流应为 nil")
	}
	// 导出后继续记录：旧快照不变（拷贝语义）
	a.RecordRx(0, 100, 11, t0, t0.UnixNano())
	h2 := a.DelayHistogram()
	var sum2 uint64
	for _, n := range h2[0] {
		sum2 += n
	}
	if sum2 != 11 {
		t.Fatalf("新快照样本和 = %d, want 11", sum2)
	}
	var sumOld uint64
	for _, n := range h[0] {
		sumOld += n
	}
	if sumOld != 10 {
		t.Fatalf("旧快照被修改: %d, want 10", sumOld)
	}
}
