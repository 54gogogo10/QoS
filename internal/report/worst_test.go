package report

// v2.11.0 滑动窗口最差统计的输出测试：文本汇总 / HTML 报告节 / --json 字段。

import (
	"os"
	"strings"
	"testing"
	"time"

	"qostool/internal/config"
	"qostool/internal/stats"
)

func worstTestCfg() *config.Config {
	return &config.Config{Flows: []config.Flow{
		{Name: "语音-EF", SrcIP: "1.1.1.1", DstIP: "2.2.2.2", SrcPort: 1, DstPort: 2, DSCP: 46, RateMbps: 1},
		{Name: "背景-BE", SrcIP: "1.1.1.1", DstIP: "2.2.2.2", SrcPort: 3, DstPort: 4, DSCP: 0, RateMbps: 1},
	}}
}

func TestWorstWindowSummary(t *testing.T) {
	worst := []stats.FlowWorst{
		{LossPct: [3]float64{50, 33.33, 10}, DelayMs: [3]float64{15, 12, 8}},
		{LossPct: [3]float64{-1, -1, -1}, DelayMs: [3]float64{-1, -1, -1}}, // 无数据流
	}
	s := WorstWindowSummary(worstTestCfg(), worst)
	for _, want := range []string{"窗口最差统计", "语音-EF", "50.00/33.33/10.00", "15.00/12.00/8.00"} {
		if !strings.Contains(s, want) {
			t.Fatalf("汇总缺少 %q:\n%s", want, s)
		}
	}
	// 无数据流显示 —
	if !strings.Contains(s, "—/—/—") {
		t.Fatalf("无数据流应显示 —:\n%s", s)
	}
	// 全部无数据 → 空串
	empty := []stats.FlowWorst{{LossPct: [3]float64{-1, -1, -1}, DelayMs: [3]float64{-1, -1, -1}}}
	if WorstWindowSummary(worstTestCfg(), empty) != "" {
		t.Fatal("全部无数据应返回空串")
	}
	if WorstWindowSummary(worstTestCfg(), nil) != "" {
		t.Fatal("nil 应返回空串")
	}
}

func TestHTMLReportWorstSection(t *testing.T) {
	cfg := worstTestCfg()
	snap := []stats.FlowSnapshot{
		{FlowIdx: 0, TxPackets: 100, RxPackets: 90, Lost: 10},
		{FlowIdx: 1, TxPackets: 100, RxPackets: 100},
	}
	worst := []stats.FlowWorst{
		{LossPct: [3]float64{50, 33.33, -1}, DelayMs: [3]float64{15, -1, -1}},
		{LossPct: [3]float64{0, 0, 0}, DelayMs: [3]float64{-1, -1, -1}},
	}
	path, err := HTMLReport(cfg, snap, Verdicts(cfg, snap, true), ReportMeta{
		Started: time.Now().Add(-time.Minute), Stopped: time.Now(), Version: "v2.11.0",
	}, t.TempDir(), true, worst, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{"窗口最差统计", "50.00", "15.00", "语音-EF"} {
		if !strings.Contains(s, want) {
			t.Fatalf("HTML 报告缺少 %q", want)
		}
	}
	// 无数据 → 不渲染该节
	path2, err := HTMLReport(cfg, snap, nil, ReportMeta{}, t.TempDir(), true, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := os.ReadFile(path2)
	if strings.Contains(string(b2), "窗口最差统计") {
		t.Fatal("worst=nil 不应渲染窗口最差节")
	}
}

func TestJSONSummaryWorstFields(t *testing.T) {
	cfg := worstTestCfg()
	snap := []stats.FlowSnapshot{
		{FlowIdx: 0, TxPackets: 100, RxPackets: 90, Lost: 10},
		{FlowIdx: 1, TxPackets: 100, RxPackets: 100},
	}
	worst := []stats.FlowWorst{
		{LossPct: [3]float64{50, 33.33, 10}, DelayMs: [3]float64{15, 12, 8}},
		{LossPct: [3]float64{0, 0, 0}, DelayMs: [3]float64{-1, -1, -1}},
	}
	b, err := JSONSummary(cfg, snap, nil, worst, ReportMeta{}, true, "")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		`"worst_loss_pct_1s": 50`, `"worst_loss_pct_10s": 33.33`, `"worst_loss_pct_60s": 10`,
		`"worst_delay_ms_1s": 15`, `"worst_delay_ms_10s": 12`, `"worst_delay_ms_60s": 8`,
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("JSON 缺少 %q:\n%s", want, s)
		}
	}
	// 无时延数据的流：delay 窗口 = -1
	if !strings.Contains(s, `"worst_delay_ms_1s": -1`) {
		t.Fatalf("无时延数据应为 -1:\n%s", s)
	}
}
