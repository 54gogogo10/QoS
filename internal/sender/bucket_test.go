package sender

import (
	"testing"
	"time"
)

func TestBucketPPSOnly(t *testing.T) {
	b := newBucket(0, 1000, 10*time.Millisecond)
	t0 := time.Now()
	b.last = t0
	total := 0
	for i := 0; i < 100; i++ { // 模拟 100 个 tick = 1s
		total += b.take(t0.Add(time.Duration(i)*10*time.Millisecond+10*time.Millisecond), 72)
	}
	if total < 950 || total > 1050 {
		t.Fatalf("1s 内共发 %d 包, 期望 ~1000", total)
	}
}

func TestBucketBurstCap(t *testing.T) {
	b := newBucket(0, 1000, 10*time.Millisecond)
	t0 := time.Now()
	b.last = t0
	// 空闲 1s 后一次能拿到的令牌不超过 2 个 tick 配额 = 20 包
	n := b.take(t0.Add(time.Second), 72)
	if n > 20 {
		t.Fatalf("burst = %d, 期望 <= 20", n)
	}
}

func TestBucketBPSLimit(t *testing.T) {
	// 8 Mbps = 1e6 B/s；包 1000B，1s 内应发 ~1000 包（字节桶先到）
	b := newBucket(8, 1e9, 10*time.Millisecond)
	t0 := time.Now()
	b.last = t0
	total := 0
	for i := 0; i < 100; i++ {
		total += b.take(t0.Add(time.Duration(i)*10*time.Millisecond+10*time.Millisecond), 1000)
	}
	if total < 950 || total > 1050 {
		t.Fatalf("1s 内共发 %d 包, 期望 ~1000", total)
	}
}

func TestBucketBothRatesZero(t *testing.T) {
	b := newBucket(0, 0, 10*time.Millisecond)
	t0 := time.Now()
	b.last = t0
	if n := b.take(t0.Add(time.Second), 100); n != 0 {
		t.Fatalf("take = %d, want 0", n)
	}
}
