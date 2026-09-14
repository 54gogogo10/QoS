package report

import (
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"time"

	"qostool/internal/config"
	"qostool/internal/stats"
)

// ReportMeta 是 HTML 报告的测试元信息。
type ReportMeta struct {
	Started  time.Time
	Stopped  time.Time
	Iface    string
	Mode     string
	Version  string
}

// HTMLReport 生成自包含（内联 CSS）的测试报告到 destDir，
// 返回报告文件路径。文件名 qostool_report_<停止时间戳>.html。
// withTailDiff 与文本汇总同语义：丢包/丢包率列是否计入停止瞬间尾部差
// （bidir/send 为 true；recv 角色 TX 来自远端轮询快照，取 false）。
// delayHist 为每流全程时延直方图（stats.Aggregator.DelayHistogram()，v2.9.0）：
// 非空时报告附每流时延分布图；流数不符或 nil 时跳过该节。
func HTMLReport(cfg *config.Config, snap []stats.FlowSnapshot, verdicts []Verdict, meta ReportMeta, destDir string, withTailDiff bool, delayHist [][]uint64) (string, error) {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(destDir, fmt.Sprintf("qostool_report_%s.html", meta.Stopped.Format("20060102_150405")))

	var passN, failN int
	for _, v := range verdicts {
		if v.Checked {
			if v.Pass {
				passN++
			} else {
				failN++
			}
		}
	}

	type row struct {
		Name, DSCP string
		TxPkts     uint64
		RxPkts     uint64
		Lost       uint64
		LossPct    string
		Reordered  uint64
		Delay      string
		Jitter     string
		Threshold  string
		Badge      template.HTML // 受控片段：PASS/FAIL/—
		FailMsg    string
	}
	rows := make([]row, 0, len(cfg.Flows))
	for i, f := range cfg.Flows {
		if i >= len(snap) {
			break // 配置更新窗口：聚合器流数可能与配置不一致
		}
		s := snap[i]
		if i >= len(verdicts) {
			break
		}
		v := verdicts[i]
		// 丢包/丢包率与最终判定同口径（withTailDiff=true 时含停止瞬间尾部差），
		// 避免出现"行内 0.00% 但徽标 FAIL（丢包率 5%）"的自相矛盾
		effLost := s.Lost
		if withTailDiff && s.TxPackets > s.RxPackets+s.Lost {
			effLost += s.TxPackets - s.RxPackets - s.Lost
		}
		lossPct := "-"
		if s.RxPackets > 0 || effLost > 0 {
			if exp := s.RxPackets + effLost; exp > 0 {
				lossPct = fmt.Sprintf("%.2f%%", float64(effLost)/float64(exp)*100)
			}
		}
		delay := "—"
		if s.DelayValid {
			// v2.8.0：avg / p95 / p99 / max
			delay = fmt.Sprintf("%.2f / %.2f / %.2f / %.2f", s.DelayTotAvgMs, s.DelayP95Ms, s.DelayP99Ms, s.DelayMaxMs)
		}
		jitter := "—"
		if s.DelayValid {
			jitter = fmt.Sprintf("%.2f", s.JitterMs)
		}
		th := "-"
		if f.MaxLossRatePct > 0 || f.MaxAvgDelayMs > 0 || f.MaxJitterMs > 0 {
			parts := []string{}
			if f.MaxLossRatePct > 0 {
				parts = append(parts, fmt.Sprintf("丢包≤%.2f%%", f.MaxLossRatePct))
			}
			if f.MaxAvgDelayMs > 0 {
				parts = append(parts, fmt.Sprintf("时延≤%.0fms", f.MaxAvgDelayMs))
			}
			if f.MaxJitterMs > 0 {
				parts = append(parts, fmt.Sprintf("抖动≤%.0fms", f.MaxJitterMs))
			}
			th = strings.Join(parts, " ")
		}
		badge := `<span class="badge na">—</span>`
		if v.Checked {
			if v.Pass {
				badge = `<span class="badge pass">PASS</span>`
			} else {
				badge = `<span class="badge fail">FAIL</span>`
			}
		}
		rows = append(rows, row{
			Name: f.Name, DSCP: dscpStr(f.DSCP),
			TxPkts: s.TxPackets, RxPkts: s.RxPackets, Lost: effLost,
			LossPct: lossPct, Reordered: s.Reordered, Delay: delay, Jitter: jitter,
			Threshold: th, Badge: template.HTML(badge), FailMsg: v.FailMsg,
		})
	}

	// 时延分布（v2.9.0）：把 1ms 直方图聚合成 ≤40 个显示桶渲染纯 CSS 柱状图
	hists := buildHistFlows(cfg, snap, delayHist)

	conclusion := "全部通过"
	verdictText := "（未配置阈值，仅记录）"
	if failN > 0 {
		conclusion = fmt.Sprintf("%d/%d 条流未通过阈值", failN, passN+failN)
		verdictText = ""
	} else if passN > 0 {
		verdictText = fmt.Sprintf("%d/%d 条流通过阈值", passN, passN+failN)
	}

	// html/template 自动转义所有非 template.HTML 字段（Badge 为受控片段）
	tmpl := `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<title>qostool 测试报告</title>
<style>
  body { font-family: "Microsoft YaHei", sans-serif; background:#111; color:#ddd; margin:0; padding:20px; }
  h1 { font-size:18px; } h2 { font-size:14px; color:#aaa; font-weight:normal; }
  table { border-collapse:collapse; width:100%; font-size:13px; margin-top:10px; }
  th, td { border:1px solid #333; padding:5px 8px; text-align:right; }
  th { background:#222; font-weight:normal; }
  td:first-child, th:first-child { text-align:left; }
  .badge { padding:2px 8px; border-radius:8px; font-size:12px; }
  .pass { background:#1e5e1e; color:#7dff7d; }
  .fail { background:#5e1e1e; color:#ff7d7d; }
  .na { background:#333; color:#999; }
  .meta td { text-align:left; border:none; padding:2px 8px; }
  .failmsg { color:#ff7d7d; font-size:12px; }
  .hflow { margin:10px 0 14px; }
  .hlabel { font-size:12px; color:#888; margin-bottom:2px; }
  .hist { display:flex; align-items:flex-end; height:64px; gap:1px; background:#161616; border:1px solid #333; padding:2px; }
  .hist .bar { background:#4a9eff; min-width:2px; flex:1 0 auto; }
</style>
</head>
<body>
<h1>qostool 测试汇总报告</h1>
<table class="meta">
  <tr><td>版本</td><td>{{.Version}}</td></tr>
  <tr><td>角色</td><td>{{.Mode}}</td></tr>
  <tr><td>接口</td><td>{{.Iface}}</td></tr>
  <tr><td>开始</td><td>{{.Started}}</td></tr>
  <tr><td>结束</td><td>{{.Stopped}}</td></tr>
  <tr><td>时长</td><td>{{.Duration}}</td></tr>
  {{if .ClockOffset}}<tr><td>时钟偏差估计</td><td>{{.ClockOffset}}</td></tr>{{end}}
</table>
<h2>结论：{{.Conclusion}} {{.VerdictText}}</h2>
<table>
  <tr><th>流</th><th>DSCP</th><th>TX 包</th><th>RX 包</th><th>丢包</th><th>丢包率</th><th>乱序</th><th>时延ms avg/p95/p99/max</th><th>抖动ms</th><th>阈值</th><th>判定</th></tr>
  {{range .Rows}}
  <tr><td>{{.Name}}</td><td>{{.DSCP}}</td><td>{{.TxPkts}}</td><td>{{.RxPkts}}</td><td>{{.Lost}}</td><td>{{.LossPct}}</td><td>{{.Reordered}}</td><td>{{.Delay}}</td><td>{{.Jitter}}</td><td>{{.Threshold}}</td><td>{{.Badge}}</td></tr>
  {{if .FailMsg}}<tr><td colspan="11" class="failmsg">违反: {{.FailMsg}}</td></tr>{{end}}
  {{end}}
</table>
{{if .Hists}}
<h2 style="margin-top:16px">时延分布（每流全程 · 1ms 精度直方图，柱高=桶内包数）</h2>
{{range .Hists}}
<div class="hflow">
  <div class="hlabel">{{.Name}} · 样本 {{.Total}} 包{{if .Overflow}} · 溢出≥2000ms {{.Overflow}} 包{{end}}</div>
  <div class="hist">{{range .Bars}}<div class="bar" style="height:{{.Height}}%" title="{{.LoMs}}-{{.HiMs}}ms: {{.Count}} 包"></div>{{end}}</div>
  {{if .MaxMs}}<div class="hlabel">0ms — {{.MaxMs}}ms（每柱 {{.BinMs}}ms）</div>{{else}}<div class="hlabel">全部样本 ≥2000ms（溢出桶）</div>{{end}}
</div>
{{end}}
{{end}}
</body>
</html>`

	dur := meta.Stopped.Sub(meta.Started).Round(time.Second)
	// v2.8.1 时钟偏差估计（第一条有时延数据的流；双机无同步时≈两机时钟差）
	clockOffset := ""
	for _, s := range snap {
		if s.DelayValid {
			clockOffset = fmt.Sprintf("%.1f ms（时延已按此校正为相对最小时延）", s.ClockOffsetMs)
			break
		}
	}
	data := struct {
		Version, Mode, Iface, Started, Stopped, Duration string
		Conclusion, VerdictText                           string
		ClockOffset                                       string
		Rows                                              []row
		Hists                                             []histFlow
	}{
		Version: meta.Version, Mode: meta.Mode, Iface: meta.Iface,
		Started: meta.Started.Format("2006-01-02 15:04:05"),
		Stopped: meta.Stopped.Format("2006-01-02 15:04:05"),
		Duration: dur.String(), Conclusion: conclusion, VerdictText: verdictText,
		ClockOffset: clockOffset, Rows: rows, Hists: hists,
	}
	t, err := template.New("report").Parse(tmpl)
	if err != nil {
		return "", err
	}
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := t.Execute(f, data); err != nil {
		return "", err
	}
	return path, nil
}

// histBar 是直方图的单个显示柱（把 1ms 精度直方图聚合后的显示桶）。
type histBar struct {
	LoMs   int
	HiMs   int // 显示桶上界（ms，开区间右端）
	Count  uint64
	Height int // 柱高：相对最高显示桶的百分比（1-100，避免 0 高不可见）
}

// histFlow 是单条流的时延分布图数据。
type histFlow struct {
	Name     string
	Bars     []histBar
	Overflow uint64 // ≥2000ms 溢出桶样本数（直方图末桶）
	Total    uint64 // 样本总数
	MaxMs    int    // 显示范围上界（ms）
	BinMs    int    // 每显示桶宽度（ms）
}

// buildHistFlows 把聚合器的 1ms 时延直方图聚合成每流的显示桶（v2.9.0）。
// delayHist 与 cfg.Flows 流数不符、对应流无样本（nil）或无时延数据时该流跳过；
// 全部无数据返回 nil（模板不渲染该节）。
func buildHistFlows(cfg *config.Config, snap []stats.FlowSnapshot, delayHist [][]uint64) []histFlow {
	if len(delayHist) == 0 {
		return nil
	}
	const maxDisplayBins = 40
	var out []histFlow
	for i, f := range cfg.Flows {
		if i >= len(delayHist) || i >= len(snap) {
			break
		}
		h := delayHist[i]
		if len(h) == 0 || !snap[i].DelayValid {
			continue
		}
		var total, overflow uint64
		for j, n := range h {
			total += n
			if j >= len(h)-1 { // 末桶为 ≥2000ms 溢出桶
				overflow = n
			}
		}
		if total == 0 {
			continue
		}
		hf := histFlow{Name: f.Name, Overflow: overflow, Total: total}
		// 显示范围：最高非空 1ms 桶的上界（不含溢出桶）
		maxMs := 0
		for j := len(h) - 2; j >= 0; j-- {
			if h[j] > 0 {
				maxMs = j + 1
				break
			}
		}
		if maxMs == 0 {
			// 全部样本在溢出桶：无有效柱，仅显示溢出说明
			hf.MaxMs, hf.BinMs = 0, 0
			out = append(out, hf)
			continue
		}
		binMs := (maxMs + maxDisplayBins - 1) / maxDisplayBins // 向上取整，≤40 桶
		if binMs < 1 {
			binMs = 1
		}
		nBins := (maxMs + binMs - 1) / binMs
		bins := make([]uint64, nBins)
		for j := 0; j < maxMs; j++ {
			bins[j/binMs] += h[j]
		}
		var binMax uint64
		for _, n := range bins {
			if n > binMax {
				binMax = n
			}
		}
		for b, n := range bins {
			height := 1
			if binMax > 0 {
				height = int(float64(n)/float64(binMax)*100) + 1 // 最高桶 100%，其余按比例（≥1 可见）
			}
			hf.Bars = append(hf.Bars, histBar{
				LoMs: b * binMs, HiMs: (b + 1) * binMs,
				Count: n, Height: minInt(height, 100),
			})
		}
		hf.MaxMs, hf.BinMs = nBins*binMs, binMs
		out = append(out, hf)
	}
	return out
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
