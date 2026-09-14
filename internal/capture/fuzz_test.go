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

// FuzzParsePacket 原生模糊测试目标（v2.9.0）：`go test` 模式跑种子语料，
// `go test -fuzz FuzzParsePacket -fuzztime 30s` 模式持续探索畸形包。
// 契约：parsePacket 对任意输入不 panic、不返回 DSCP 越界的包。
func FuzzParsePacket(f *testing.F) {
	seeds := [][]byte{
		{0x45, 0x00, 0x00, 0x28, 0, 0, 0, 0, 64, 17, 0, 0, 1, 2, 3, 4, 5, 6, 7, 8,
			0x30, 0x39, 0x9c, 0x40, 0x00, 0x18, 0, 0, 'Q', 'O', 'S', 'T', 0, 1, 0, 0, 0, 0, 0, 0},
		{0x60, 0, 0, 0, 0, 18, 0x40, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16,
			17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32,
			0x30, 0x39, 0x9c, 0x40, 0x00, 0x18, 0, 0, 'Q', 'O', 'S', 'T', 0, 1, 0, 0, 0, 0, 0, 0},
		{0x45, 0xb8},   // DSCP=EF 的截断头
		{0x46},         // IHL=6
		make([]byte, 48), // 全零
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		pkt, err := parsePacket(data)
		if err == nil && pkt != nil {
			if pkt.key.DSCP < 0 || pkt.key.DSCP > 63 {
				t.Fatalf("DSCP 越界: %d", pkt.key.DSCP)
			}
			if pkt.payload != nil && len(pkt.payload) > len(data) {
				t.Fatalf("payload 长度越界: %d > %d", len(pkt.payload), len(data))
			}
		}
	})
}
