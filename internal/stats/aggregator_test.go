package stats

import (
	"math"
	"testing"
	"time"
)

func TestLossDetection(t *testing.T) {
	a := NewAggregator(1, 10)
	a.RecordRx(0, 100, 1, time.Now(), 0)
	a.RecordRx(0, 100, 2, time.Now(), 0)
	a.RecordRx(0, 100, 7, time.Now(), 0) // 3,4,5,6 丢失
	snap := a.Current(time.Now())
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

func TestReorderNotCountedAsLoss(t *testing.T) {
	a := NewAggregator(1, 10)
	a.RecordRx(0, 100, 5, time.Now(), 0)
	a.RecordRx(0, 100, 3, time.Now(), 0) // 乱序/重传，不计丢包
	snap := a.Current(time.Now())
	if snap[0].Lost != 0 {
		t.Fatalf("Lost = %d, want 0", snap[0].Lost)
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
	a.RecordRx(1, 100, 1, time.Now(), 0)
	a.RecordRx(1, 100, 4, time.Now(), 0) // 丢 2 个
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
	a.RecordRx(0, 100, 1, base.Add(-10*time.Millisecond), base.Add(-20*time.Millisecond).UnixNano())
	a.RecordRx(0, 100, 2, base, base.Add(-20*time.Millisecond).UnixNano())
	a.RecordRx(0, 100, 3, base.Add(10*time.Millisecond), base.Add(-20*time.Millisecond).UnixNano())
	s := a.Current(base.Add(10 * time.Millisecond))[0]
	if !s.DelayValid {
		t.Fatal("DelayValid = false, want true")
	}
	if math.Abs(s.DelayAvgMs-20) > 1e-6 || math.Abs(s.DelayTotAvgMs-20) > 1e-6 {
		t.Fatalf("avg = %v/%v, want 20", s.DelayAvgMs, s.DelayTotAvgMs)
	}
	if math.Abs(s.DelayMinMs-10) > 1e-6 || math.Abs(s.DelayMaxMs-30) > 1e-6 {
		t.Fatalf("min/max = %v/%v, want 10/30", s.DelayMinMs, s.DelayMaxMs)
	}
}

func TestDelayWindowReset(t *testing.T) {
	a := NewAggregator(1, 10)
	base := time.Now()
	// 第一窗口：时延 10ms
	a.RecordRx(0, 100, 1, base, base.Add(-10*time.Millisecond).UnixNano())
	a.Snapshot(base)
	// 第二窗口：时延 40ms（窗口 avg 应变 40，累计 avg 变 25）
	a.RecordRx(0, 100, 2, base.Add(time.Second), base.Add(time.Second).Add(-40*time.Millisecond).UnixNano())
	s := a.Snapshot(base.Add(time.Second))[0]
	if math.Abs(s.DelayAvgMs-40) > 1e-6 {
		t.Fatalf("窗口 avg = %v, want 40", s.DelayAvgMs)
	}
	if math.Abs(s.DelayTotAvgMs-25) > 1e-6 {
		t.Fatalf("累计 avg = %v, want 25", s.DelayTotAvgMs)
	}
	if math.Abs(s.DelayMinMs-10) > 1e-6 || math.Abs(s.DelayMaxMs-40) > 1e-6 {
		t.Fatalf("min/max = %v/%v, want 10/40", s.DelayMinMs, s.DelayMaxMs)
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
	if math.Abs(h.Dly[0][0]-5) > 1e-6 {
		t.Fatalf("Dly[0][0] = %v, want 5", h.Dly[0][0])
	}
	// 无时间戳包 → -1 哨兵
	a2 := NewAggregator(1, 10)
	a2.RecordRx(0, 100, 1, time.Now(), 0)
	a2.Snapshot(time.Now())
	if h2 := a2.History(); h2.Dly[0][0] != -1 {
		t.Fatalf("无效时延应记 -1, got %v", h2.Dly[0][0])
	}
}
