package report

import (
	"regexp"
	"strings"
	"testing"
	"time"

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
	// 行格式：流 DSCP TX包 TX字节 RX包 RX字节 丢包 …（%14d 右对齐，丢包列 %10d）
	if !rowMatch(out, `EF-语音\s+EF\(46\)\s+1000\s+1000000\s+998\s+998000\s+2\s`) {
		t.Fatalf("尾部差未计入丢包:\n%s", out)
	}
	// 完全一致 → 丢包 0
	snap2 := []stats.FlowSnapshot{{
		FlowIdx: 0, TxPackets: 1000, TxBytes: 1e6,
		RxPackets: 1000, RxBytes: 1e6, Lost: 0,
	}}
	out2 := Summary(testCfg(), snap2, 1e6, 1e6, 0, true)
	if !rowMatch(out2, `EF-语音\s+EF\(46\)\s+1000\s+1000000\s+1000\s+1000000\s+0\s`) ||
		!strings.Contains(out2, "* 丢包 = seq 空洞丢失") {
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
	if !rowMatch(out, `EF-语音\s+EF\(46\)\s+1000\s+1000000\s+1000\s+1000000\s+0\s`) {
		t.Fatalf("recv 远端 TX 模式丢包应为 0（尾部差不计入）:\n%s", out)
	}
}

// rowMatch 用正则匹配汇总表的流数据行（对列宽变化健壮）。
func rowMatch(out, pattern string) bool {
	re := regexp.MustCompile(pattern)
	return re.MatchString(out)
}

func TestTableDelayColumns(t *testing.T) {
	snap := testSnap(1.25e6, 1.25e6, 1000, 900, 100)
	snap[0].DelayValid = true
	snap[0].DelayAvgMs, snap[0].DelayMinMs, snap[0].DelayMaxMs = 1.25, 0.8, 3.1
	snap[0].JitterMs = 0.42
	out := Table(testCfg(), snap, true, true)
	for _, want := range []string{"时延ms", "抖动ms", "1.2/0.8/3.1", "0.42"} {
		if !strings.Contains(out, want) {
			t.Fatalf("表格缺少 %q:\n%s", want, out)
		}
	}
}

func TestTableDelayNoData(t *testing.T) {
	snap := testSnap(1.25e6, 1.25e6, 1000, 900, 100) // DelayValid=false（旧发送端）
	out := Table(testCfg(), snap, true, true)
	if !strings.Contains(out, "—") {
		t.Fatalf("无时延数据应显示 —:\n%s", out)
	}
}

func TestSummaryDelayColumn(t *testing.T) {
	snap := testSnap(1.25e6, 1.25e6, 1000, 900, 100)
	snap[0].DelayValid = true
	snap[0].DelayTotAvgMs, snap[0].DelayMinMs, snap[0].DelayMaxMs = 1.25, 0.8, 3.1
	snap[0].DelayP95Ms, snap[0].DelayP99Ms = 2.5, 3.0 // v2.8.0 百分位
	snap[0].JitterMs = 0.42
	out := Summary(testCfg(), snap, 1_000_000, 900_000, 100, true)
	for _, want := range []string{"时延ms(avg/p95/p99/max)", "1.2/2.5/3.0/3.1", "0.42"} {
		if !strings.Contains(out, want) {
			t.Fatalf("汇总缺少 %q:\n%s", want, out)
		}
	}
}

// TestJSONSummaryFail JSON 汇总：丢包率超阈值 → verdict=fail、ok=false、含违规明细。
func TestJSONSummaryFail(t *testing.T) {
	cfg := testCfg()
	cfg.Flows[0].MaxLossRatePct = 0.5
	snap := testSnap(1e6, 1e6, 1000, 990, 10) // 1% 丢包 → FAIL
	vs := Verdicts(cfg, snap, true)
	b, err := JSONSummary(cfg, snap, vs, ReportMeta{
		Started: time.Now().Add(-time.Minute), Stopped: time.Now(), Iface: "eth0", Mode: "bidir", Version: "v2.8.0",
	}, true, "logs/x.html")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{`"verdict": "fail"`, `"fail_count": 1`, `"ok": false`, `"loss_rate_pct": 1`, `"html_report": "logs/x.html"`, `"checked": true`, `"pass": false`, "丢包率"} {
		if !strings.Contains(s, want) {
			t.Fatalf("JSON 缺少 %q:\n%s", want, s)
		}
	}
}

// TestJSONSummaryPass JSON 汇总：全部通过 → verdict=pass、ok=true。
func TestJSONSummaryPass(t *testing.T) {
	cfg := testCfg()
	cfg.Flows[0].MaxLossRatePct = 0.5
	snap := testSnap(1e6, 1e6, 1000, 1000, 0)
	vs := Verdicts(cfg, snap, true)
	b, err := JSONSummary(cfg, snap, vs, ReportMeta{Started: time.Now(), Stopped: time.Now().Add(time.Minute)}, true, "")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{`"verdict": "pass"`, `"fail_count": 0`, `"ok": true`, `"pass": true`} {
		if !strings.Contains(s, want) {
			t.Fatalf("JSON 缺少 %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "html_report") {
		t.Fatalf("空报告路径不应输出 html_report 字段:\n%s", s)
	}
}

// TestJSONSummaryNoThreshold 未配置阈值 → verdict=none、全部 checked=false。
func TestJSONSummaryNoThreshold(t *testing.T) {
	snap := testSnap(1e6, 1e6, 1000, 900, 100)
	vs := Verdicts(testCfg(), snap, true)
	b, err := JSONSummary(testCfg(), snap, vs, ReportMeta{}, true, "")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{`"verdict": "none"`, `"checked": false`, `"loss_rate_pct": 10`} {
		if !strings.Contains(s, want) {
			t.Fatalf("JSON 缺少 %q:\n%s", want, s)
		}
	}
}

// TestJSONSummaryTailDiff JSON 丢包口径与文本汇总一致：含停止瞬间尾部差。
func TestJSONSummaryTailDiff(t *testing.T) {
	snap := []stats.FlowSnapshot{{
		FlowIdx: 0, TxPackets: 1000, TxBytes: 1e6,
		RxPackets: 998, RxBytes: 998000, Lost: 0,
	}}
	b, err := JSONSummary(testCfg(), snap, nil, ReportMeta{}, true, "")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, `"lost": 2`) || !strings.Contains(s, `"loss_rate_pct": 0.2`) {
		t.Fatalf("JSON 尾部差口径错误:\n%s", s)
	}
	// 不计尾部差（recv 远端 TX）：lost 保持 seq 空洞 0
	b2, _ := JSONSummary(testCfg(), snap, nil, ReportMeta{}, false, "")
	if !strings.Contains(string(b2), `"lost": 0`) {
		t.Fatalf("不计尾部差时 lost 应为 0:\n%s", string(b2))
	}
}

// TestJSONSummaryDelayFields 时延字段：avg/p95/p99/min/max/jitter 进 JSON。
func TestJSONSummaryDelayFields(t *testing.T) {
	snap := testSnap(1e6, 1e6, 1000, 900, 100)
	snap[0].DelayValid = true
	snap[0].DelayTotAvgMs, snap[0].DelayP95Ms, snap[0].DelayP99Ms = 1.25, 2.5, 3.0
	snap[0].DelayMinMs, snap[0].DelayMaxMs, snap[0].JitterMs = 0.8, 3.1, 0.42
	b, err := JSONSummary(testCfg(), snap, nil, ReportMeta{}, true, "")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{`"delay_avg_ms": 1.25`, `"delay_p95_ms": 2.5`, `"delay_p99_ms": 3`, `"delay_min_ms": 0.8`, `"delay_max_ms": 3.1`, `"jitter_ms": 0.42`} {
		if !strings.Contains(s, want) {
			t.Fatalf("JSON 缺少 %q:\n%s", want, s)
		}
	}
}
