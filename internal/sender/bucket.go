package sender

import "time"

// bucket 双参数令牌桶：字节速率 + 包速率，哪个先耗尽就限哪个。
// 令牌上限（burst）为两个 tick 的配额，防止长时间空闲后突发。
type bucket struct {
	rateBps float64 // 字节/秒，0 = 不限
	ratePps float64 // 包/秒，0 = 不限
	burstB  float64
	burstP  float64
	tokensB float64
	tokensP float64
	last    time.Time
}

func newBucket(rateMbps, ratePps float64, tick time.Duration) *bucket {
	bps := rateMbps * 1e6 / 8
	burst := 2 * tick.Seconds()
	return &bucket{
		rateBps: bps,
		ratePps: ratePps,
		burstB:  bps * burst,
		burstP:  ratePps * burst,
		last:    time.Now(),
	}
}

func (b *bucket) refill(now time.Time) {
	dt := now.Sub(b.last).Seconds()
	if dt < 0 {
		dt = 0
	}
	b.last = now
	if b.rateBps > 0 {
		b.tokensB += dt * b.rateBps
		if b.tokensB > b.burstB {
			b.tokensB = b.burstB
		}
	}
	if b.ratePps > 0 {
		b.tokensP += dt * b.ratePps
		if b.tokensP > b.burstP {
			b.tokensP = b.burstP
		}
	}
}

// take 返回 now 时刻最多能发的包数（每包 bytes 字节），并扣除对应令牌。
func (b *bucket) take(now time.Time, bytes int) int {
	if bytes <= 0 || (b.rateBps == 0 && b.ratePps == 0) {
		return 0
	}
	b.refill(now)
	n := int(^uint(0) >> 1)
	if b.ratePps > 0 {
		n = min(n, int(b.tokensP))
	}
	if b.rateBps > 0 {
		n = min(n, int(b.tokensB/float64(bytes)))
	}
	b.tokensP -= float64(n)
	if b.rateBps > 0 {
		b.tokensB -= float64(n * bytes)
	}
	return n
}
