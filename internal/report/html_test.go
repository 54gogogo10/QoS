package report

import (
	"os"
	"strings"
	"testing"
	"time"

	"qostool/internal/config"
	"qostool/internal/stats"
)

func TestHTMLReport(t *testing.T) {
	dir := t.TempDir()
	cfg := testCfg()
	cfg.Flows[0].MaxLossRatePct = 0.5
	snap := testSnap(1e6, 1e6, 1000, 990, 10) // 1% 丢包 → FAIL
	vs := Verdicts(cfg, snap, true)
	meta := ReportMeta{Started: time.Now().Add(-time.Minute), Stopped: time.Now(), Iface: "eth0", Mode: "bidir", Version: "v2.8.1"}
	path, err := HTMLReport(cfg, snap, vs, meta, dir, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, want := range []string{"汇总报告", "EF-语音", "FAIL", "0.5", "eth0", "v2.8.1", "违反: 丢包率 1.00%"} {
		if !strings.Contains(s, want) {
			t.Fatalf("报告缺少 %q", want)
		}
	}
}

func TestHTMLReportAllPass(t *testing.T) {
	dir := t.TempDir()
	cfg := testCfg()
	snap := testSnap(1e6, 1e6, 1000, 1000, 0)
	vs := Verdicts(cfg, snap, true)
	path, err := HTMLReport(cfg, snap, vs, ReportMeta{}, dir, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "通过") {
		t.Fatalf("全部通过应有结论文本")
	}
	if strings.Contains(string(data), "FAIL") {
		t.Fatalf("无阈值不应出现 FAIL")
	}
}

// TestHTMLReportTailDiff 显示的丢包/丢包率应与最终判定同口径：
// withTailDiff=true 时计入停止瞬间尾部差，避免"0.00% 但 FAIL"自相矛盾。
func TestHTMLReportTailDiff(t *testing.T) {
	dir := t.TempDir()
	cfg := testCfg()
	cfg.Flows[0].MaxLossRatePct = 5
	// TX=1000、RX=900、空洞丢包=0：全部丢包来自尾部差（发而未收）
	snap := testSnap(1e6, 1e6, 1000, 900, 0)
	vs := Verdicts(cfg, snap, true)
	if !vs[0].Checked || vs[0].Pass {
		t.Fatalf("10%% 尾部差应 FAIL: %+v", vs[0])
	}
	path, err := HTMLReport(cfg, snap, vs, ReportMeta{}, dir, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	s := string(data)
	if !strings.Contains(s, "10.00%") {
		t.Fatalf("丢包率列应含尾部差（10.00%%）: %s", s)
	}
	// withTailDiff=false（recv 角色）：只显示 seq 空洞丢包（0.00%）
	path2, err := HTMLReport(cfg, snap, vs, ReportMeta{}, dir, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	data2, _ := os.ReadFile(path2)
	if !strings.Contains(string(data2), "0.00%") {
		t.Fatalf("不计尾部差时丢包率应为 0.00%%")
	}
}

// TestHTMLReportShortSnap snap 比 cfg.Flows 短（配置更新窗口）不越界 panic。
func TestHTMLReportShortSnap(t *testing.T) {
	cfg := testCfg()
	cfg.Flows = append(cfg.Flows, cfg.Flows[0])
	snap := testSnap(1e6, 1e6, 100, 100, 0)
	vs := Verdicts(cfg, snap, true)
	if _, err := HTMLReport(cfg, snap, vs, ReportMeta{}, t.TempDir(), true, nil); err != nil {
		t.Fatal(err)
	}
}

// TestHTMLReportReorderColumn 乱序列（v2.9.0）：快照含乱序数时报告应显示。
func TestHTMLReportReorderColumn(t *testing.T) {
	cfg := testCfg()
	snap := testSnap(1e6, 1e6, 1000, 990, 10)
	snap[0].Reordered = 7
	vs := Verdicts(cfg, snap, true)
	path, err := HTMLReport(cfg, snap, vs, ReportMeta{}, t.TempDir(), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	s := string(data)
	if !strings.Contains(s, "<th>乱序</th>") || !strings.Contains(s, ">7</td>") {
		t.Fatalf("报告缺少乱序列:\n%s", s)
	}
}

// TestHTMLReportDelayHist 时延分布直方图（v2.9.0）：
// 有直方图数据时渲染分布节；无数据（nil / DelayValid=false）不渲染。
func TestHTMLReportDelayHist(t *testing.T) {
	cfg := testCfg()
	snap := testSnap(1e6, 1e6, 1000, 990, 10)
	snap[0].DelayValid = true
	snap[0].DelayTotAvgMs, snap[0].DelayMaxMs = 1.2, 5.0
	vs := Verdicts(cfg, snap, true)
	// 直方图：1ms 桶 ×2001（末桶溢出）；样本集中在 0-5ms
	hist := make([][]uint64, 1)
	hist[0] = make([]uint64, 2001)
	hist[0][0], hist[0][1], hist[0][2], hist[0][5] = 100, 200, 50, 10
	path, err := HTMLReport(cfg, snap, vs, ReportMeta{}, t.TempDir(), true, hist)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	s := string(data)
	for _, want := range []string{"时延分布", "样本 360 包", "height:"} {
		if !strings.Contains(s, want) {
			t.Fatalf("直方图节缺少 %q:\n%s", want, s)
		}
	}
	// 无直方图数据：不渲染分布节
	path2, err := HTMLReport(cfg, snap, vs, ReportMeta{}, t.TempDir(), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	data2, _ := os.ReadFile(path2)
	if strings.Contains(string(data2), "时延分布") {
		t.Fatalf("无直方图数据不应渲染分布节")
	}
	// DelayValid=false（旧发送端）：同样不渲染
	snap[0].DelayValid = false
	path3, err := HTMLReport(cfg, snap, vs, ReportMeta{}, t.TempDir(), true, hist)
	if err != nil {
		t.Fatal(err)
	}
	data3, _ := os.ReadFile(path3)
	if strings.Contains(string(data3), "时延分布") {
		t.Fatalf("旧发送端（无时延数据）不应渲染分布节")
	}
}

// TestBuildHistFlows 直方图聚合逻辑：桶聚合、溢出、全溢出边界。
func TestBuildHistFlows(t *testing.T) {
	cfg := testCfg()
	cfg.Flows = append(cfg.Flows, config.Flow{Name: "f2"}) // 第二流无数据
	snap := testSnap(1e6, 1e6, 10, 10, 0)
	snap[0].DelayValid = true
	snap = append(snap, stats.FlowSnapshot{FlowIdx: 1})

	hist := make([][]uint64, 2)
	hist[0] = make([]uint64, 2001)
	for i := 0; i < 100; i++ { // 0-99ms 各 1 包 → maxMs=100，40 桶 → binMs=ceil(100/40)=3
		hist[0][i] = 1
	}
	hist[0][2000] = 5 // 溢出桶
	hist[1] = nil      // 无样本

	out := buildHistFlows(cfg, snap, hist)
	if len(out) != 1 {
		t.Fatalf("应只渲染 1 条流（第二流无样本）: %d", len(out))
	}
	hf := out[0]
	if hf.Total != 105 || hf.Overflow != 5 {
		t.Fatalf("样本/溢出数错误: total=%d overflow=%d", hf.Total, hf.Overflow)
	}
	if hf.MaxMs != 102 || hf.BinMs != 3 || len(hf.Bars) != 34 {
		t.Fatalf("桶参数错误: maxMs=%d binMs=%d bars=%d", hf.MaxMs, hf.BinMs, len(hf.Bars))
	}
	var sum uint64
	for _, b := range hf.Bars {
		sum += b.Count
	}
	if sum != 100 {
		t.Fatalf("显示桶样本和应等于非溢出样本 100: %d", sum)
	}

	// 全部样本在溢出桶：无柱、仅溢出说明
	hist2 := make([][]uint64, 1)
	hist2[0] = make([]uint64, 2001)
	hist2[0][2000] = 9
	out2 := buildHistFlows(cfg, snap, hist2)
	if len(out2) != 1 || len(out2[0].Bars) != 0 || out2[0].Overflow != 9 {
		t.Fatalf("全溢出边界错误: %+v", out2)
	}
}
