package sender

import (
	"context"
	"net"
	"testing"
	"time"

	"qostool/internal/config"
	"qostool/internal/protocol"
	"qostool/internal/stats"
)

// TestSenderPacingLocalhost 用本地 UDP socket 接收，验证 1s 内发包数 ≈ 1100 (±25%)。
func TestSenderPacingLocalhost(t *testing.T) {
	rcv, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer rcv.Close()
	dstPort := rcv.LocalAddr().(*net.UDPAddr).Port

	cfg := &config.Config{Flows: []config.Flow{{
		Name: "t", Protocol: "udp",
		SrcIP: "127.0.0.1", DstIP: "127.0.0.1",
		SrcPort: 0, DstPort: dstPort,
		DSCP: 46, RateMbps: 0, RatePPS: 1000, IPLen: 92,
	}}}
	agg := stats.NewAggregator(1, 10)
	s, err := New(cfg, agg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go s.Run(ctx)
	time.Sleep(1100 * time.Millisecond) // 1000pps × 1.1s ≈ 1100 包
	cancel()

	rcv.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	var pkts int
	var lastSeq uint32
	buf := make([]byte, 2048)
	for {
		n, _, err := rcv.ReadFromUDP(buf)
		if err != nil {
			break
		}
		fid, seq, _, ok := protocol.DecodeHeader(buf[:n])
		if !ok {
			t.Fatal("载荷 magic 错误")
		}
		if fid != 0 {
			t.Fatalf("flow_id = %d, want 0", fid)
		}
		if seq <= lastSeq {
			t.Fatalf("seq 非递增: %d -> %d", lastSeq, seq)
		}
		lastSeq = seq
		pkts++
	}
	// 110 个 tick × 10 包 = 1100，留 ±25% 余量防机器抖动
	if pkts < 825 || pkts > 1375 {
		t.Fatalf("收到 %d 包, 期望 ~1100 (±25%%)", pkts)
	}
	tx, _, _ := agg.Totals()
	lo := uint64(825) * uint64(protocol.HeaderSize+64+28)
	hi := uint64(1375) * uint64(protocol.HeaderSize+64+28)
	if tx < lo || tx > hi {
		t.Fatalf("txBytes = %d, 期望 [%d, %d]", tx, lo, hi)
	}
}

// TestNewCleansUpOnPartialFailure 回归测试：部分流创建失败时，
// 已创建 socket 的端口必须释放（否则下次启动 bind 失败）。
func TestNewCleansUpOnPartialFailure(t *testing.T) {
	// 占用一个端口，让第二条流 bind 失败
	blocker, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	blockedPort := blocker.LocalAddr().(*net.UDPAddr).Port

	// 找一个空闲端口给 flow1
	probe, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	freePort := probe.LocalAddr().(*net.UDPAddr).Port
	probe.Close()

	cfg := &config.Config{Flows: []config.Flow{
		{Name: "f1", Protocol: "udp", SrcIP: "127.0.0.1", DstIP: "127.0.0.1",
			SrcPort: freePort, DstPort: freePort + 100, DSCP: 46, RatePPS: 10, IPLen: 92},
		{Name: "f2", Protocol: "udp", SrcIP: "127.0.0.1", DstIP: "127.0.0.1",
			SrcPort: blockedPort, DstPort: blockedPort + 100, DSCP: 46, RatePPS: 10, IPLen: 92},
	}}
	agg := stats.NewAggregator(2, 10)
	if _, err := New(cfg, agg); err == nil {
		t.Fatal("第二条流应创建失败")
	}
	// 验证 flow1 的端口已被释放：1 秒内应能重新绑定
	deadline := time.Now().Add(time.Second)
	for {
		c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: freePort})
		if err == nil {
			c.Close()
			return // 端口已释放
		}
		if time.Now().After(deadline) {
			t.Fatalf("flow1 的端口 %d 未释放: %v", freePort, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestSetRatesChangesPacing 验证动态调速：SetRates 后批间隔必须缩短，且长度不符报错。
func TestSetRatesChangesPacing(t *testing.T) {
	cfg := &config.Config{Flows: []config.Flow{{Name: "t", Protocol: "udp",
		SrcIP: "127.0.0.1", DstIP: "127.0.0.1", SrcPort: 0, DstPort: 9, DSCP: 0,
		RateMbps: 0, RatePPS: 100, IPLen: 92}}}
	s, err := New(cfg, stats.NewAggregator(1, 10))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	before := s.flows[0].batchGapNs.Load()
	if err := s.SetRates([]RateSpec{{RateMbps: 0, RatePPS: 1000}}); err != nil {
		t.Fatal(err)
	}
	after := s.flows[0].batchGapNs.Load()
	if after >= before || after <= 0 {
		t.Fatalf("SetRates 后 batchGap = %d, want < %d", after, before)
	}
	if err := s.SetRates([]RateSpec{}); err == nil {
		t.Fatal("长度不符应报错")
	}
}
