package capture

import (
	"math/rand"
	"testing"
)

// TestParsePacketFuzz 畸形包不 panic。
func TestParsePacketFuzz(t *testing.T) {
	r := rand.New(rand.NewSource(42))
	for i := 0; i < 5000; i++ {
		n := r.Intn(128)
		buf := make([]byte, n)
		r.Read(buf)
		pkt, err := parsePacket(buf)
		if err == nil && pkt != nil {
			// 解析成功也要安全
			if pkt.key.DSCP < 0 || pkt.key.DSCP > 63 {
				t.Fatalf("DSCP 越界: %d", pkt.key.DSCP)
			}
		}
	}
	// 固定畸形头
	bad := [][]byte{
		{0x45},                              // 太短
		{0x46, 0x00, 0x00, 0x00},            // IHL=6 但长度不足
		{0x45, 0x00, 0x00, 0x28, 0, 0, 0, 0, 64, 17, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, // UDP 头截断
		make([]byte, 20),                    // 全零 IPv4
		{0x60},                              // IPv6 太短
		make([]byte, 40),                    // 全零 IPv6
	}
	for _, b := range bad {
		parsePacket(b) // 不应 panic
	}
}
