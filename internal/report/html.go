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
func HTMLReport(cfg *config.Config, snap []stats.FlowSnapshot, verdicts []Verdict, meta ReportMeta, destDir string) (string, error) {
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
		Delay      string
		Jitter     string
		Threshold  string
		Badge      template.HTML // 受控片段：PASS/FAIL/—
		FailMsg    string
	}
	rows := make([]row, 0, len(cfg.Flows))
	for i, f := range cfg.Flows {
		s := snap[i]
		v := verdicts[i]
		lossPct := "-"
		if s.RxPackets > 0 || s.Lost > 0 {
			lossPct = fmt.Sprintf("%.2f%%", s.LossRate*100)
		}
		delay := "—"
		if s.DelayValid {
			delay = fmt.Sprintf("%.2f / %.2f / %.2f", s.DelayTotAvgMs, s.DelayMinMs, s.DelayMaxMs)
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
			TxPkts: s.TxPackets, RxPkts: s.RxPackets, Lost: s.Lost,
			LossPct: lossPct, Delay: delay, Jitter: jitter,
			Threshold: th, Badge: template.HTML(badge), FailMsg: v.FailMsg,
		})
	}

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
</table>
<h2>结论：{{.Conclusion}} {{.VerdictText}}</h2>
<table>
  <tr><th>流</th><th>DSCP</th><th>TX 包</th><th>RX 包</th><th>丢包</th><th>丢包率</th><th>时延ms avg/min/max</th><th>抖动ms</th><th>阈值</th><th>判定</th></tr>
  {{range .Rows}}
  <tr><td>{{.Name}}</td><td>{{.DSCP}}</td><td>{{.TxPkts}}</td><td>{{.RxPkts}}</td><td>{{.Lost}}</td><td>{{.LossPct}}</td><td>{{.Delay}}</td><td>{{.Jitter}}</td><td>{{.Threshold}}</td><td>{{.Badge}}</td></tr>
  {{if .FailMsg}}<tr><td colspan="10" class="failmsg">违反: {{.FailMsg}}</td></tr>{{end}}
  {{end}}
</table>
</body>
</html>`

	dur := meta.Stopped.Sub(meta.Started).Round(time.Second)
	data := struct {
		Version, Mode, Iface, Started, Stopped, Duration string
		Conclusion, VerdictText                           string
		Rows                                               []row
	}{
		Version: meta.Version, Mode: meta.Mode, Iface: meta.Iface,
		Started: meta.Started.Format("2006-01-02 15:04:05"),
		Stopped: meta.Stopped.Format("2006-01-02 15:04:05"),
		Duration: dur.String(), Conclusion: conclusion, VerdictText: verdictText,
		Rows: rows,
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
