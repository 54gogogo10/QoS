package report

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestHTMLReport(t *testing.T) {
	dir := t.TempDir()
	cfg := testCfg()
	cfg.Flows[0].MaxLossRatePct = 0.5
	snap := testSnap(1e6, 1e6, 1000, 990, 10) // 1% 丢包 → FAIL
	vs := Verdicts(cfg, snap, true)
	meta := ReportMeta{Started: time.Now().Add(-time.Minute), Stopped: time.Now(), Iface: "eth0", Mode: "bidir", Version: "v2.7.0"}
	path, err := HTMLReport(cfg, snap, vs, meta, dir)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, want := range []string{"汇总报告", "EF-语音", "FAIL", "0.5", "eth0", "v2.7.0", "丢包率 1.00% > 0.50%"} {
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
	path, err := HTMLReport(cfg, snap, vs, ReportMeta{}, dir)
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
