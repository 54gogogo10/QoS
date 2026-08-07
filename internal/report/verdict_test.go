package report

import (
	"strings"
	"testing"

	"qostool/internal/config"
	"qostool/internal/stats"
)

func TestVerdictLossFail(t *testing.T) {
	cfg := &config.Config{Flows: []config.Flow{{Name: "a", MaxLossRatePct: 0.5}}}
	snap := testSnap(1e6, 1e6, 1000, 990, 10) // 1% 丢包
	vs := Verdicts(cfg, snap, false)
	if !vs[0].Checked || vs[0].Pass {
		t.Fatalf("verdict = %+v, want FAIL", vs[0])
	}
	if !strings.Contains(vs[0].FailMsg, "丢包率") {
		t.Fatalf("FailMsg = %q", vs[0].FailMsg)
	}
}

func TestVerdictLossPass(t *testing.T) {
	cfg := &config.Config{Flows: []config.Flow{{Name: "a", MaxLossRatePct: 0.5}}}
	snap := testSnap(1e6, 1e6, 1000, 999, 1) // 0.1%
	if vs := Verdicts(cfg, snap, false); !vs[0].Pass {
		t.Fatalf("verdict = %+v, want PASS", vs[0])
	}
}

func TestVerdictDelayFail(t *testing.T) {
	cfg := &config.Config{Flows: []config.Flow{{Name: "a", MaxAvgDelayMs: 50}}}
	snap := []stats.FlowSnapshot{{FlowIdx: 0, DelayValid: true, DelayAvgMs: 60, DelayTotAvgMs: 55}}
	if vs := Verdicts(cfg, snap, false); vs[0].Pass {
		t.Fatalf("窗口 60ms > 50ms 应 FAIL, got %+v", vs[0])
	}
	snap2 := []stats.FlowSnapshot{{FlowIdx: 0, DelayValid: true, DelayAvgMs: 40, DelayTotAvgMs: 60}}
	if vs := Verdicts(cfg, snap2, true); vs[0].Pass {
		t.Fatalf("累计 60ms > 50ms 应 FAIL（useTotDelay）, got %+v", vs[0])
	}
}

func TestVerdictJitterFail(t *testing.T) {
	cfg := &config.Config{Flows: []config.Flow{{Name: "a", MaxJitterMs: 10}}}
	snap := []stats.FlowSnapshot{{FlowIdx: 0, DelayValid: true, JitterMs: 12}}
	if vs := Verdicts(cfg, snap, false); vs[0].Pass {
		t.Fatal("jitter 12 > 10 应 FAIL")
	}
}

func TestVerdictNoThresholdNotChecked(t *testing.T) {
	cfg := &config.Config{Flows: []config.Flow{{Name: "a"}}}
	if vs := Verdicts(cfg, testSnap(1e6, 0, 1000, 0, 0), false); vs[0].Checked {
		t.Fatal("无阈值不应 Checked")
	}
}

func TestVerdictNoDelayDataSkipsDelayCheck(t *testing.T) {
	cfg := &config.Config{Flows: []config.Flow{{Name: "a", MaxAvgDelayMs: 50}}}
	snap := []stats.FlowSnapshot{{FlowIdx: 0, DelayValid: false}} // 旧发送端
	if vs := Verdicts(cfg, snap, false); !vs[0].Pass {
		t.Fatal("无时延数据不应判时延 FAIL")
	}
}

func TestVerdictNoDataSkipsLossCheck(t *testing.T) {
	cfg := &config.Config{Flows: []config.Flow{{Name: "a", MaxLossRatePct: 0.5}}}
	snap := []stats.FlowSnapshot{{FlowIdx: 0}} // 未收到任何包
	if vs := Verdicts(cfg, snap, false); !vs[0].Pass {
		t.Fatal("无收发数据不应判丢包 FAIL")
	}
}

// TestVerdictFinalBlackHole 最终判定：只发不收（尾部差 100%）必须 FAIL。
func TestVerdictFinalBlackHole(t *testing.T) {
	cfg := &config.Config{Flows: []config.Flow{{Name: "a", MaxLossRatePct: 1.0}}}
	snap := []stats.FlowSnapshot{{FlowIdx: 0, TxPackets: 2000, RxPackets: 0, Lost: 0}}
	if vs := Verdicts(cfg, snap, true); vs[0].Pass {
		t.Fatalf("只发不收最终判定应 FAIL, got %+v", vs[0])
	}
	// 实时判定：未收到任何包不判（避免启动瞬间误报）
	if vs := Verdicts(cfg, snap, false); !vs[0].Pass {
		t.Fatalf("实时判定无数据不应 FAIL, got %+v", vs[0])
	}
}

// TestVerdictFinalTailDiff 最终判定丢包口径 = seq 空洞 + 尾部差。
func TestVerdictFinalTailDiff(t *testing.T) {
	cfg := &config.Config{Flows: []config.Flow{{Name: "a", MaxLossRatePct: 0.5}}}
	// TX 1000, RX 998, 空洞 0 → 尾部差 2 → 0.2% < 0.5% → PASS
	snap := []stats.FlowSnapshot{{FlowIdx: 0, TxPackets: 1000, RxPackets: 998, Lost: 0}}
	if vs := Verdicts(cfg, snap, true); !vs[0].Pass {
		t.Fatalf("尾部差 2 包应 PASS, got %+v", vs[0])
	}
	// TX 1000, RX 990, 空洞 0 → 1% > 0.5% → FAIL（实时口径 0% 会误判 PASS）
	snap2 := []stats.FlowSnapshot{{FlowIdx: 0, TxPackets: 1000, RxPackets: 990, Lost: 0}}
	if vs := Verdicts(cfg, snap2, true); vs[0].Pass {
		t.Fatalf("尾部差 10 包最终应 FAIL, got %+v", vs[0])
	}
	if vs := Verdicts(cfg, snap2, false); !vs[0].Pass {
		t.Fatalf("实时口径无 seq 空洞应 PASS, got %+v", vs[0])
	}
}
