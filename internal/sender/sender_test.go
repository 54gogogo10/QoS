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

// TestSenderPacingLocalhost 用本地 UDP socket 接收，验证 1s 内发包数 ≈ 1000 (±15%)。
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
		DSCP: 46, RateMbps: 0, RatePPS: 1000, PayloadSize: 64,
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
		fid, seq, ok := protocol.DecodeHeader(buf[:n])
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
	lo := uint64(825) * uint64(10+64)
	hi := uint64(1375) * uint64(10+64)
	if tx < lo || tx > hi {
		t.Fatalf("txBytes = %d, 期望 [%d, %d]", tx, lo, hi)
	}
}
