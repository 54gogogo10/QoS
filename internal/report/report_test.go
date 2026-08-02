package report

import (
	"strings"
	"testing"

	"qostool/internal/config"
	"qostool/internal/stats"
)

func testCfg() *config.Config {
	return &config.Config{Flows: []config.Flow{
		{Name: "EF-语音", SrcIP: "192.168.1.10", DstIP: "192.168.1.20", SrcPort: 10000, DstPort: 20000, DSCP: 46},
	}}
}

func testSnap(txBps, rxBps float64, txPkts, rxPkts, lost uint64) []stats.FlowSnapshot {
	lossRate := 0.0
	if rxPkts+lost > 0 {
		lossRate = float64(lost) / float64(rxPkts+lost)
	}
	return []stats.FlowSnapshot{{
		FlowIdx: 0, TxBps: txBps, TxPps: txBps / 1000, RxBps: rxBps, RxPps: rxBps / 1000,
		TxPackets: txPkts, TxBytes: txPkts * 1000,
		RxPackets: rxPkts, RxBytes: rxPkts * 1000,
		Lost: lost, LossRate: lossRate,
	}}
}

func TestTableColumns(t *testing.T) {
	// 1.25e6 B/s = 10 Mbps；丢包 100/1000 = 10.00%
	out := Table(testCfg(), testSnap(1.25e6, 1.25e6, 1000, 900, 100), true, true)
	for _, want := range []string{"流", "DSCP", "TX Mbps", "RX Mbps", "丢包", "EF-语音", "EF(46)", "10.00", "10.00%"} {
		if !strings.Contains(out, want) {
			t.Fatalf("表格缺少 %q:\n%s", want, out)
		}
	}
}

func TestTableNoCapture(t *testing.T) {
	out := Table(testCfg(), testSnap(1e6, 0, 1000, 0, 0), true, false)
	if !strings.Contains(out, "-") {
		t.Fatalf("无接收时应显示 -:\n%s", out)
	}
	if strings.Contains(out, "10.00%") {
		t.Fatalf("无接收时丢包率应为 -:\n%s", out)
	}
}

func TestTableNoSender(t *testing.T) {
	// 无发送端：TX 两列显示 -，RX 列与丢包率正常
	out := Table(testCfg(), testSnap(1.25e6, 1.25e6, 1000, 900, 100), false, true)
	if strings.Count(out, "10.00") != 2 { // RX Mbps 与丢包率 10.00% 各含一处
		t.Fatalf("RX Mbps 与丢包率应显示 10.00:\n%s", out)
	}
	if strings.Count(out, "1250") != 1 {
		t.Fatalf("RX pps 应显示 1250:\n%s", out)
	}
	// 剔除流名连字符与 5 元组 "->" 后，仅剩 TX Mbps/TX pps 两个 "-"
	body := strings.NewReplacer("EF-语音", "", "->", "").Replace(out)
	if strings.Count(body, "-") != 2 {
		t.Fatalf("无发送端时 TX Mbps/TX pps 应为 -:\n%s", out)
	}
}

func TestTableNoRxPackets(t *testing.T) {
	// 全部 RX 为零：RX 两列与丢包率显示 -，丢包率不显示百分比
	out := Table(testCfg(), testSnap(1.25e6, 0, 1000, 0, 0), true, true)
	body := strings.NewReplacer("EF-语音", "", "->", "").Replace(out)
	if strings.Count(body, "-") != 3 {
		t.Fatalf("RX Mbps/RX pps/丢包率 应为 -:\n%s", out)
	}
	if strings.Contains(out, "10.00%") {
		t.Fatalf("无 RX 数据时丢包率应为 -:\n%s", out)
	}
}

func TestSummary(t *testing.T) {
	out := Summary(testCfg(), testSnap(1e6, 1e6, 1000, 900, 100), 1e6, 9e5, 100)
	for _, want := range []string{"汇总", "1000", "900", "100"} {
		if !strings.Contains(out, want) {
			t.Fatalf("汇总缺少 %q:\n%s", want, out)
		}
	}
}

func TestTableEightRows(t *testing.T) {
	flows := make([]config.Flow, 8)
	for i := range flows {
		flows[i] = config.Flow{Name: "f", SrcIP: "1.1.1.1", DstIP: "2.2.2.2", SrcPort: 1, DstPort: 2, DSCP: config.DSCP(i * 8)}
	}
	snap := make([]stats.FlowSnapshot, 8)
	out := Table(&config.Config{Flows: flows}, snap, true, true)
	if strings.Count(out, "\n") != 9 { // 1 表头 + 8 行
		t.Fatalf("行数 = %d, want 9:\n%s", strings.Count(out, "\n"), out)
	}
}
