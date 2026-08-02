package stats

import (
	"math"
	"testing"
	"time"
)

func TestLossDetection(t *testing.T) {
	a := NewAggregator(1, 10)
	a.RecordRx(0, 100, 1)
	a.RecordRx(0, 100, 2)
	a.RecordRx(0, 100, 7) // 3,4,5,6 丢失
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
	a.RecordRx(0, 100, 5)
	a.RecordRx(0, 100, 3) // 乱序/重传，不计丢包
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
	a.RecordRx(99, 1, 1)
	a.RecordTx(-1, 1, 1)
	a.RecordRx(-1, 1, 1)
	if tx, rx, lost := a.Totals(); tx != 0 || rx != 0 || lost != 0 {
		t.Fatalf("越界 flowIdx 不应计入统计: Totals = %d %d %d", tx, rx, lost)
	}
}

func TestTotals(t *testing.T) {
	a := NewAggregator(2, 10)
	a.RecordTx(0, 5, 500)
	a.RecordTx(1, 3, 300)
	a.RecordRx(1, 100, 1)
	a.RecordRx(1, 100, 4) // 丢 2 个
	tx, rx, lost := a.Totals()
	if tx != 800 || rx != 200 || lost != 2 {
		t.Fatalf("Totals = %d %d %d", tx, rx, lost)
	}
}
func TestSeqWraparound(t *testing.T) {
	a := NewAggregator(1, 10)
	a.RecordRx(0, 100, math.MaxUint32-1)
	a.RecordRx(0, 100, math.MaxUint32)
	a.RecordRx(0, 100, 1) // 回绕
	snap := a.Current(time.Now())
	if snap[0].Lost != 0 {
		t.Fatalf("回绕后 Lost = %d, want 0（已知限制：seq 回绕不计丢失）", snap[0].Lost)
	}
}
