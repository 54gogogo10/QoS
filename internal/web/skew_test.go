package web

import (
	"math"
	"testing"
	"time"

	"qostool/internal/stats"
)

// TestClockEstimatorOffset 固定偏差下估计收敛到真值（RTT 小的样本占优）。
func TestClockEstimatorOffset(t *testing.T) {
	e := NewClockEstimator(32)
	// 真实偏差 +5000ms；样本 RTT 与测量噪声（offset 噪声随 RTT 增大）
	for i := 0; i < 20; i++ {
		rtt := int64(1e6 + i*500*1e3) // 1ms 起递增
		noise := rtt / 4              // 不对称性噪声 ≈ RTT/4
		if i%2 == 0 {
			noise = -noise
		}
		e.AddSample(rtt, 5000*1_000_000+noise)
	}
	off, rtt, ok := e.Estimate()
	if !ok {
		t.Fatal("应有估计")
	}
	if math.Abs(float64(off)-5000e6) > 1e6 { // ±1ms
		t.Fatalf("offset = %v ns, want ≈5000ms", off)
	}
	if rtt != 1e6 {
		t.Fatalf("最优 RTT = %v, want 1ms 样本", rtt)
	}
}

// TestClockEstimatorSampleCap 环形缓冲：样本数不超过上限且仍可估计。
func TestClockEstimatorSampleCap(t *testing.T) {
	e := NewClockEstimator(8)
	for i := 0; i < 100; i++ {
		e.AddSample(2*1e6, int64(i)*1e6)
	}
	if e.Samples() != 8 {
		t.Fatalf("Samples = %d, want 8", e.Samples())
	}
	if _, _, ok := e.Estimate(); !ok {
		t.Fatal("应有估计")
	}
}

// TestClockEstimatorInsufficient 样本不足不估计。
func TestClockEstimatorInsufficient(t *testing.T) {
	e := NewClockEstimator(32)
	e.AddSample(1e6, 1)
	if _, _, ok := e.Estimate(); ok {
		t.Fatal("1 个样本不应估计")
	}
	// 异常 RTT 丢弃
	e.AddSample(-5, 0)
	e.AddSample(20*1e9, 0) // 20s > 10s 上限
	if e.Samples() != 1 {
		t.Fatalf("异常样本应丢弃, Samples = %d", e.Samples())
	}
}

// TestClockEstimatorBadSamplesFiltered 极端 RTT 样本被最小 25% 过滤排除。
func TestClockEstimatorBadSamplesFiltered(t *testing.T) {
	e := NewClockEstimator(32)
	// 20 个正常样本（RTT 1ms，offset 真值 100ms）+ 10 个拥塞样本（RTT 500ms，offset 噪声 ±250ms）
	for i := 0; i < 20; i++ {
		e.AddSample(1*1e6, 100*1e6)
	}
	for i := 0; i < 10; i++ {
		noise := int64(250 * 1_000_000)
		if i%2 == 0 {
			noise = -noise
		}
		e.AddSample(500*1_000_000, 100*1_000_000+noise)
	}
	off, _, ok := e.Estimate()
	if !ok {
		t.Fatal("应有估计")
	}
	if math.Abs(float64(off)-100e6) > 1e6 { // ±1ms
		t.Fatalf("offset = %v ns, want ≈100ms（拥塞样本应被过滤）", off)
	}
}

// TestClockHintInjectionSign 锁定 hint 符号约定（回归：v2.8.1 注入未取负，
// 发送端时钟慢于接收端时全部时延被放大 2 倍偏差）。
// 模拟双机：接收端按 NTP 四时间戳测发送端 /api/time（与 remoteLoopOnce 相同的
// 计算方式），Estimate 结果经取负注入聚合器（生产路径的符号转换），再灌测试流，
// 校正后时延必须回到真实单程时延。两个时钟方向都要验证。
func TestClockHintInjectionSign(t *testing.T) {
	for _, senderSkew := range []time.Duration{-30 * time.Second, 30 * time.Second} {
		// senderSkew = 发送端时钟 − 接收端时钟（真值）
		est := NewClockEstimator(32)
		recvBase := time.Now()
		const rtt = 2 * time.Millisecond
		for i := 0; i < 5; i++ {
			t1 := recvBase.Add(time.Duration(i) * time.Second)
			serverTime := t1.Add(senderSkew) // 发送端时钟（t2≈t3）
			t4 := t1.Add(rtt)
			est.AddSample(int64(t4.Sub(t1)), serverTime.UnixNano()-(t1.UnixNano()+t4.UnixNano())/2)
		}
		offNs, _, ok := est.Estimate()
		if !ok {
			t.Fatalf("skew=%v: 应有估计", senderSkew)
		}
		if math.Abs(float64(offNs)-float64(senderSkew)) > float64(2*time.Millisecond) {
			t.Fatalf("skew=%v: estimator offset=%dms 偏离真值", senderSkew, offNs/int64(time.Millisecond))
		}

		// 生产注入路径（handleStart / runCLI）：取负后注入
		agg := stats.NewAggregator(1, 100)
		agg.SetClockOffsetHint(-offNs)

		// 测试流：真实单程时延 0.4ms 起每包 +0.2ms 变化（恒定时延下相对 min 法
		// avg 恒为 0 无判别力；hint 带 −rtt/2 测量偏差，真实场景校正后为毫秒级）
		recvClock := time.Now()
		for i := 0; i < 100; i++ {
			realDelay := 400*time.Microsecond + time.Duration(i%5)*200*time.Microsecond
			sendTs := recvClock.Add(time.Duration(i) * 10 * time.Millisecond).Add(senderSkew).UnixNano() // 发送端时钟下的发送时刻
			recvAt := recvClock.Add(time.Duration(i)*10*time.Millisecond + realDelay)                    // 接收端时钟下的到达时刻
			agg.RecordRx(0, 100, uint32(i+1), recvAt, sendTs)
		}
		agg.Finalize()
		s := agg.Current(time.Now())[0]
		if !s.DelayValid {
			t.Fatalf("skew=%v: 应有时延数据", senderSkew)
		}
		// 符号正确：毫秒级（≤变化幅度量级）；符号反了：≈2|skew|=60000ms；未校正：≈|skew|
		if s.DelayTotAvgMs > 50 || s.DelayTotAvgMs < 0.1 {
			t.Fatalf("skew=%v: 校正后时延 %.3f ms, want 毫秒级 0.1~50（hint 符号错误或未校正）",
				senderSkew, s.DelayTotAvgMs)
		}
	}
}
