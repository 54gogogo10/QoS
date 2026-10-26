package stats

// v2.11.0 滑动窗口最差统计测试：秒聚合、环形缓冲、1/10/60s 窗口最差值。

import (
	"math"
	"testing"
	"time"
)

// snapAt 在指定时刻做一次 Snapshot（驱动秒聚合）。
func snapAt(a *Aggregator, t time.Time) {
	a.Snapshot(t)
}

// TestWorstWindowsLoss 逐秒构造丢包，验证各窗口最差丢包率。
// 秒 1：0% 丢包（10 收 0 丢）；秒 2：50%（seq 跳洞 10 个）；秒 3：10%。
func TestWorstWindowsLoss(t *testing.T) {
	a := NewAggregator(1, 10)
	base := time.Unix(1_000_000, 0)

	// 秒 1：seq 1..10 正常到达
	for s := uint32(1); s <= 10; s++ {
		a.RecordRx(0, 100, s, base.Add(time.Duration(s)*time.Millisecond), 0)
	}
	snapAt(a, base.Add(500*time.Millisecond))

	// 秒 2：只到达 seq 21（跳过 11..20 → 10 包丢失），加上 22..30 共 10 收 10 丢
	a.RecordRx(0, 100, 21, base.Add(time.Second), 0)
	for s := uint32(22); s <= 30; s++ {
		a.RecordRx(0, 100, s, base.Add(time.Second+time.Duration(s)*time.Millisecond), 0)
	}
	snapAt(a, base.Add(1500*time.Millisecond))

	// 秒 3：seq 31..40 正常（上一秒空洞已在宽限期内结算；本秒 0 丢）
	for s := uint32(31); s <= 40; s++ {
		a.RecordRx(0, 100, s, base.Add(2*time.Second+time.Duration(s)*time.Millisecond), 0)
	}
	snapAt(a, base.Add(2500*time.Millisecond))

	// 跨入秒 4：把秒 3 落盘
	snapAt(a, base.Add(3500*time.Millisecond))

	w := a.WorstWindows()[0]
	// 最差 1s = 秒 2 的 50%
	if math.Abs(w.LossPct[0]-50) > 0.01 {
		t.Fatalf("最差 1s 丢包 = %v, want 50", w.LossPct[0])
	}
	// 3 秒累计：20 收 10 丢 → 3s 窗口 33.33%；10s/60s 窗口不足（<10 桶）→ -1
	if math.Abs(w.LossPct[1]-(-1)) > 1e-9 || math.Abs(w.LossPct[2]-(-1)) > 1e-9 {
		t.Fatalf("不足 10/60 秒应为 -1: %+v", w)
	}
	// sendTs=0 → 无时延数据
	if w.DelayMs[0] >= 0 {
		t.Fatalf("无时间戳不应有时延窗口值: %+v", w)
	}
}

// TestWorstWindowsDelay 带时间戳验证窗口平均时延的最差值。
func TestWorstWindowsDelay(t *testing.T) {
	a := NewAggregator(1, 10)
	base := time.Unix(2_000_000, 0)
	// 秒 1：10 包，时延约 5ms；秒 2：10 包，时延约 20ms（min 校正后 0/15ms）
	sendDelay := func(sec time.Time, seq uint32, d time.Duration) {
		recv := sec.Add(time.Duration(seq) * time.Millisecond)
		a.RecordRx(0, 100, seq, recv, recv.Add(-d).UnixNano())
	}
	for s := uint32(1); s <= 10; s++ {
		sendDelay(base, s, 5*time.Millisecond)
	}
	snapAt(a, base.Add(500*time.Millisecond))
	for s := uint32(11); s <= 20; s++ {
		sendDelay(base.Add(time.Second), s, 20*time.Millisecond)
	}
	snapAt(a, base.Add(1500*time.Millisecond))
	// 秒 3：无包（空桶也应落盘，维持窗口连续性）
	snapAt(a, base.Add(2500*time.Millisecond))

	w := a.WorstWindows()[0]
	// min rawD=5ms 校正后：秒 1 时延≈0、秒 2 ≈15ms → 最差 1s ≈15
	if w.DelayMs[0] < 14 || w.DelayMs[0] > 16 {
		t.Fatalf("最差 1s 时延 = %v, want ≈15ms", w.DelayMs[0])
	}
	// 秒 1 无数据（DelayMs[0] 只看有样本的秒）……秒 1 有样本（≈0ms），
	// 但最差取最大 → 仍 ≈15ms
	if w.LossPct[0] < 0 {
		t.Fatalf("有收包应有丢包窗口值: %+v", w)
	}
}

// TestWorstWindowsRingOverwrite 环形缓冲覆盖：写入超过容量后最旧数据被淘汰。
func TestWorstWindowsRingOverwrite(t *testing.T) {
	w := &worstState{}
	base := int64(3_000_000)
	// 累计计数器必须单调增长（真实聚合器语义）：第 1 秒只有 10 个丢失，
	// 之后每秒净收 10 包、0 丢包
	w.accumulate(base, 0, 10, 0, 0) // 累计 rx=0 lost=10
	rxCum := uint64(0)
	for s := int64(1); s <= worstRingCap+2; s++ {
		rxCum += 10
		w.accumulate(base+s, rxCum, 10, 0, 0)
	}
	loss, _ := w.query()
	// 第 1 秒（100% 丢包桶）已被覆盖 → 最差 1s = 0%
	for i, l := range loss {
		if l != 0 {
			t.Fatalf("覆盖后窗口 %d 最差丢包 = %v, want 0", i, l)
		}
	}
	// n 封顶
	if w.n != worstRingCap {
		t.Fatalf("n = %d, want %d", w.n, worstRingCap)
	}
}

// TestWorstWindowsEmpty 无任何数据：全部 -1。
func TestWorstWindowsEmpty(t *testing.T) {
	a := NewAggregator(2, 10)
	w := a.WorstWindows()
	for i := range w {
		for k := range w[i].LossPct {
			if w[i].LossPct[k] != -1 || w[i].DelayMs[k] != -1 {
				t.Fatalf("无数据应为 -1: %+v", w[i])
			}
		}
	}
}

// TestWorstWindowsCounterReset 计数器回退（异常场景）按 0 增量处理，不产生负数。
func TestWorstWindowsCounterReset(t *testing.T) {
	w := &worstState{}
	w.accumulate(100, 50, 5, 0, 0) // 累计 rx=50 lost=5
	w.accumulate(100, 10, 0, 0, 0) // 回退：不产生负增量
	w.accumulate(101, 10, 0, 0, 0) // 落盘秒 100 的桶；秒 101 无增长（空桶）
	loss, _ := w.query()
	if loss[0] < 0 {
		t.Fatalf("不应出现负丢包率: %v", loss[0])
	}
	// 唯一有数据的桶 {rx:50 lost:5} → 5/55 ≈ 9.09%
	if math.Abs(loss[0]-5.0/55.0*100) > 0.01 {
		t.Fatalf("回退后桶数据 = %v, want ≈9.09", loss[0])
	}
}
