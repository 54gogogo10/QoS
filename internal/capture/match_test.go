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
