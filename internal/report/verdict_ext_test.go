package report

// v2.11.0 阈值扩展测试：p95/p99 时延与乱序包数阈值判定。

import (
	"strings"
	"testing"

	"qostool/internal/config"
	"qostool/internal/stats"
)

// extCfg 构造带扩展阈值的单流配置。
func extCfg(p95, p99 float64, reorder uint64) *config.Config {
	return &config.Config{Flows: []config.Flow{{
		Name: "ef", Protocol: "udp", SrcIP: "1.1.1.1", DstIP: "2.2.2.2",
		SrcPort: 1, DstPort: 2, DSCP: 46,
		MaxP95DelayMs: p95, MaxP99DelayMs: p99, MaxReorder: reorder,
	}}}
}

func TestVerdictP95P95Thresholds(t *testing.T) {
	// 有时延数据：p95=25/p99=55，p95 阈值 20 越限、p99 阈值 60 通过
	snap := []stats.FlowSnapshot{{
		DelayValid: true, DelayP95Ms: 25, DelayP99Ms: 55,
	}}
	vs := Verdicts(extCfg(20, 60, 0), snap, true)
	if !vs[0].Checked || vs[0].Pass {
		t.Fatalf("p95 越限应 FAIL: %+v", vs[0])
	}
	if !strings.Contains(vs[0].FailMsg, "p95 时延 25") {
		t.Fatalf("FailMsg 应含 p95 明细: %q", vs[0].FailMsg)
	}
	// p99 越限
	vs = Verdicts(extCfg(30, 50, 0), snap, true)
	if vs[0].Pass || !strings.Contains(vs[0].FailMsg, "p99 时延 55") {
		t.Fatalf("p99 越限应 FAIL 且带明细: %+v", vs[0])
	}
	// 全部通过
	vs = Verdicts(extCfg(30, 60, 0), snap, true)
	if !vs[0].Pass {
		t.Fatalf("阈值内应 PASS: %+v", vs[0])
	}
	// 旧版发送端（无时延数据）：不判定（DelayValid=false）
	vs = Verdicts(extCfg(20, 20, 0), []stats.FlowSnapshot{{}}, true)
	if vs[0].Checked {
		t.Fatalf("无时延数据不应判定 p95/p99: %+v", vs[0])
	}
}

func TestVerdictReorderThreshold(t *testing.T) {
	snap := []stats.FlowSnapshot{{RxPackets: 100, Reordered: 5}}
	// 阈值 3：5 > 3 → FAIL
	vs := Verdicts(extCfg(0, 0, 3), snap, true)
	if !vs[0].Checked || vs[0].Pass || !strings.Contains(vs[0].FailMsg, "乱序 5 > 3") {
		t.Fatalf("乱序越限应 FAIL: %+v", vs[0])
	}
	// 阈值 5：5 > 5 不成立 → PASS
	vs = Verdicts(extCfg(0, 0, 5), snap, true)
	if !vs[0].Pass {
		t.Fatalf("乱序等于阈值应 PASS: %+v", vs[0])
	}
	// 无数据（只发不收）实时不判定；最终口径参与判定但乱序=0 ≤ 阈值 → PASS
	//（"只发不收=全丢"的失败语义由丢包阈值负责，乱序阈值只关心乱序证据）
	vs = Verdicts(extCfg(0, 0, 3), []stats.FlowSnapshot{{TxPackets: 10}}, false)
	if vs[0].Checked {
		t.Fatalf("实时且无接收数据不应判定乱序: %+v", vs[0])
	}
	vs = Verdicts(extCfg(0, 0, 3), []stats.FlowSnapshot{{TxPackets: 10}}, true)
	if !vs[0].Checked || !vs[0].Pass {
		t.Fatalf("最终口径只发不收：参与判定且乱序 0≤3 应 PASS: %+v", vs[0])
	}
}

// TestVerdictExtUnconfigured 未配置扩展阈值（全 0）不参与判定。
func TestVerdictExtUnconfigured(t *testing.T) {
	snap := []stats.FlowSnapshot{{RxPackets: 10, DelayValid: true, DelayP95Ms: 999, Reordered: 999}}
	vs := Verdicts(extCfg(0, 0, 0), snap, true)
	if vs[0].Checked {
		t.Fatalf("未配置不应判定: %+v", vs[0])
	}
}
