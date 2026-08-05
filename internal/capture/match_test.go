package capture

import (
	"net"
	"testing"

	"qostool/internal/config"
)

func testConfig() *config.Config {
	return &config.Config{Flows: []config.Flow{
		{Name: "f0", SrcIP: "192.168.1.10", DstIP: "192.168.1.20", SrcPort: 10000, DstPort: 20000, DSCP: 46},
		{Name: "f1", SrcIP: "192.168.1.10", DstIP: "192.168.1.20", SrcPort: 10001, DstPort: 20001, DSCP: 34},
	}}
}

func TestMatchExact(t *testing.T) {
	m := newMatcher(testConfig())
	k := Key{DSCP: 46, Proto: 17, SrcIP: net.ParseIP("192.168.1.10"), DstIP: net.ParseIP("192.168.1.20"), SrcPort: 10000, DstPort: 20000}
	if idx := m.match(k); idx != 0 {
		t.Fatalf("match = %d, want 0", idx)
	}
}

func TestMatchDirectionReversed(t *testing.T) {
	m := newMatcher(testConfig())
	k := Key{DSCP: 34, Proto: 17, SrcIP: net.ParseIP("192.168.1.20"), DstIP: net.ParseIP("192.168.1.10"), SrcPort: 20001, DstPort: 10001}
	if idx := m.match(k); idx != 1 {
		t.Fatalf("match = %d, want 1", idx)
	}
}

func TestMatchMiss(t *testing.T) {
	m := newMatcher(testConfig())
	cases := []Key{
		{DSCP: 0, Proto: 17, SrcIP: net.ParseIP("192.168.1.10"), DstIP: net.ParseIP("192.168.1.20"), SrcPort: 10000, DstPort: 20000}, // DSCP 不同
		{DSCP: 46, Proto: 6, SrcIP: net.ParseIP("192.168.1.10"), DstIP: net.ParseIP("192.168.1.20"), SrcPort: 10000, DstPort: 20000}, // 协议不同
		{DSCP: 46, Proto: 17, SrcIP: net.ParseIP("10.0.0.1"), DstIP: net.ParseIP("192.168.1.20"), SrcPort: 10000, DstPort: 20000},    // IP 不同
		{DSCP: 46, Proto: 17, SrcIP: net.ParseIP("192.168.1.10"), DstIP: net.ParseIP("192.168.1.20"), SrcPort: 9999, DstPort: 20000}, // 端口不同
	}
	for i, k := range cases {
		if idx := m.match(k); idx != -1 {
			t.Fatalf("case %d: match = %d, want -1", i, idx)
		}
	}
}

// TestMatchIgnoreDSCP 5 元组命中但 DSCP 不匹配：精确匹配不命中，
// 忽略 DSCP 能识别出流（发送端 DSCP 未生效的诊断场景）。
func TestMatchIgnoreDSCP(t *testing.T) {
	m := newMatcher(testConfig())
	// 流 0 的 5 元组 + DSCP=0（未生效）：精确不命中，忽略 DSCP 命中 f0
	k := Key{DSCP: 0, Proto: 17, SrcIP: net.ParseIP("192.168.1.10"), DstIP: net.ParseIP("192.168.1.20"), SrcPort: 10000, DstPort: 20000}
	if idx := m.match(k); idx != -1 {
		t.Fatalf("match = %d, want -1", idx)
	}
	if idx := m.matchIgnoreDSCP(k); idx != 0 {
		t.Fatalf("matchIgnoreDSCP = %d, want 0", idx)
	}
	// 反向 + 流 1
	kr := Key{DSCP: 0, Proto: 17, SrcIP: net.ParseIP("192.168.1.20"), DstIP: net.ParseIP("192.168.1.10"), SrcPort: 20001, DstPort: 10001}
	if idx := m.matchIgnoreDSCP(kr); idx != 1 {
		t.Fatalf("matchIgnoreDSCP(反向) = %d, want 1", idx)
	}
	// 5 元组完全不匹配
	kx := Key{DSCP: 0, Proto: 17, SrcIP: net.ParseIP("9.9.9.9"), DstIP: net.ParseIP("8.8.8.8"), SrcPort: 1, DstPort: 2}
	if idx := m.matchIgnoreDSCP(kx); idx != -1 {
		t.Fatalf("matchIgnoreDSCP(不匹配) = %d, want -1", idx)
	}
}

// TestMatchWithDiag 单趟扫描结果与 match/matchIgnoreDSCP 一致。
func TestMatchWithDiag(t *testing.T) {
	m := newMatcher(testConfig())
	// 精确命中：idx=流下标，dscpOnly 无意义
	k := Key{DSCP: 46, Proto: 17, SrcIP: net.ParseIP("192.168.1.10"), DstIP: net.ParseIP("192.168.1.20"), SrcPort: 10000, DstPort: 20000}
	idx, dscpOnly := m.matchWithDiag(k)
	if idx != 0 || dscpOnly != -1 {
		t.Fatalf("matchWithDiag = (%d, %d), want (0, -1)", idx, dscpOnly)
	}
	// DSCP 不匹配：idx=-1，dscpOnly=流下标
	k2 := Key{DSCP: 0, Proto: 17, SrcIP: net.ParseIP("192.168.1.10"), DstIP: net.ParseIP("192.168.1.20"), SrcPort: 10000, DstPort: 20000}
	idx, dscpOnly = m.matchWithDiag(k2)
	if idx != -1 || dscpOnly != 0 {
		t.Fatalf("matchWithDiag = (%d, %d), want (-1, 0)", idx, dscpOnly)
	}
	// 均不命中
	k3 := Key{DSCP: 0, Proto: 17, SrcIP: net.ParseIP("9.9.9.9"), DstIP: net.ParseIP("8.8.8.8"), SrcPort: 1, DstPort: 2}
	idx, dscpOnly = m.matchWithDiag(k3)
	if idx != -1 || dscpOnly != -1 {
		t.Fatalf("matchWithDiag = (%d, %d), want (-1, -1)", idx, dscpOnly)
	}
}
