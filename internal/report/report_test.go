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
	out := Summary(testCfg(), testSnap(1e6, 1e6, 1000, 900, 100), 1e6, 9e5, 100, true)
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

// TestSummaryTailDiff 回归：收发不一致必须反映在丢包列（尾部差补全）。
func TestSummaryTailDiff(t *testing.T) {
	// TX 1000, RX 998, seq 空洞 0 → 丢包应显示 2
	snap := []stats.FlowSnapshot{{
		FlowIdx: 0, TxPackets: 1000, TxBytes: 1e6,
		RxPackets: 998, RxBytes: 998000, Lost: 0,
	}}
	out := Summary(testCfg(), snap, 1e6, 998000, 0, true)
	if !strings.Contains(out, "2\n") && !strings.Contains(out, "         2\n") {
		t.Fatalf("尾部差未计入丢包:\n%s", out)
	}
	// 完全一致 → 丢包 0
	snap2 := []stats.FlowSnapshot{{
		FlowIdx: 0, TxPackets: 1000, TxBytes: 1e6,
		RxPackets: 1000, RxBytes: 1e6, Lost: 0,
	}}
	out2 := Summary(testCfg(), snap2, 1e6, 1e6, 0, true)
	if !strings.Contains(out2, "0\n") || !strings.Contains(out2, "* 丢包 = seq 空洞丢失") {
		t.Fatalf("一致时丢包应为 0 且含口径说明:\n%s", out2)
	}
}

// TestSummaryNoTailDiffWithRemote 回归：远端轮询 TX（recv 模式）时尾部差不计入丢包，
// 否则轮询滞后（TX 累计值滞后最多 1s）会产生虚假丢包。
func TestSummaryNoTailDiffWithRemote(t *testing.T) {
	// 远端 TX 滞后快照 1000 < 本地 RX 1000，无 seq 空洞：丢包必须为 0
	snap := []stats.FlowSnapshot{{
		FlowIdx: 0, TxPackets: 1000, TxBytes: 1e6,
		RxPackets: 1000, RxBytes: 1e6, Lost: 0,
	}}
	out := Summary(testCfg(), snap, 1e6, 1e6, 0, false)
	if !strings.Contains(out, "0\n") {
		t.Fatalf("recv 远端 TX 模式丢包应为 0（尾部差不计入）:\n%s", out)
	}
}
