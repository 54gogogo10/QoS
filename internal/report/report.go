// Package report 渲染终端实时表格与汇总报告。
package report

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"qostool/internal/config"
	"qostool/internal/stats"
)

// Version 当前工具版本号：HTML 报告与 --json 汇总的 Version 字段统一取自这里，
// 避免多调用点各自硬编码出现不一致。
const Version = "v2.9.0"

func dscpStr(d config.DSCP) string {
	if n := d.Name(); n != "" {
		return fmt.Sprintf("%s(%d)", n, d)
	}
	return fmt.Sprintf("%d", d)
}

// delayCell 渲染时延列：avg/min/max ms；无数据（旧发送端）显示 —。
func delayCell(s stats.FlowSnapshot) string {
	if !s.DelayValid {
		return "—"
	}
	return fmt.Sprintf("%.1f/%.1f/%.1f", s.DelayAvgMs, s.DelayMinMs, s.DelayMaxMs)
}

// Table 渲染实时统计表格（纯文本，终端重绘时由调用方清屏）。
// snap 比 cfg.Flows 短时（配置更新窗口）多出的流跳过，不越界。
func Table(cfg *config.Config, snap []stats.FlowSnapshot, haveSender, haveCapture bool) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%-14s %-8s %-40s %10s %10s %10s %10s %8s %14s %8s\n",
		"流", "DSCP", "5元组", "TX Mbps", "TX pps", "RX Mbps", "RX pps", "丢包%", "时延ms", "抖动ms"))
	for i, f := range cfg.Flows {
		if i >= len(snap) {
			break
		}
		s := snap[i]
		five := fmt.Sprintf("%s:%d->%s:%d", f.SrcIP, f.SrcPort, f.DstIP, f.DstPort)
		txMbps, txPps := "-", "-"
		if haveSender {
			txMbps = fmt.Sprintf("%.2f", s.TxBps*8/1e6)
			txPps = fmt.Sprintf("%.0f", s.TxPps)
		}
		rxMbps, rxPps, loss := "-", "-", "-"
		if haveCapture {
			if s.RxPackets > 0 || s.RxBps > 0 {
				rxMbps = fmt.Sprintf("%.2f", s.RxBps*8/1e6)
				rxPps = fmt.Sprintf("%.0f", s.RxPps)
			}
			if s.RxPackets > 0 || s.Lost > 0 {
				loss = fmt.Sprintf("%.2f%%", s.LossRate*100)
			}
		}
		delay, jitter := "—", "—"
		if haveCapture && s.DelayValid {
			delay = delayCell(s)
			jitter = fmt.Sprintf("%.2f", s.JitterMs)
		}
		b.WriteString(fmt.Sprintf("%-14s %-8s %-40s %10s %10s %10s %10s %8s %14s %8s\n",
			f.Name, dscpStr(f.DSCP), five, txMbps, txPps, rxMbps, rxPps, loss, delay, jitter))
	}
	return b.String()
}

// tailDiff 返回停止瞬间未确认的尾部包数：发送已发出、接收未确认（含在途包）。
// seq 跳号检测只能发现已收到包之间的空洞，末尾缺失必须靠 TX-RX 差值补全。
func tailDiff(s stats.FlowSnapshot) uint64 {
	if s.TxPackets > s.RxPackets+s.Lost {
		return s.TxPackets - s.RxPackets - s.Lost
	}
	return 0
}

// Summary 渲染运行结束后的汇总报告。
// 丢包列 = seq 空洞丢包 + 尾部差（收发不一致如实呈现）。
// 乱序列 = 乱序到达包数（到达时 seq 已小于当前最大已见 seq，不含孔内重复包，v2.9.0）。
// 时延列 = 全程 avg / p95 / p99 / max（v2.8.0 起含百分位）。
// withTailDiff=false 时不计尾部差：recv 模式的 TX 来自远端轮询快照（滞后最多 1s），
// “停止瞬间未确认的尾部包”语义不成立，计入会得到虚假的滞后差。
func Summary(cfg *config.Config, snap []stats.FlowSnapshot, txTotal, rxTotal, lost uint64, withTailDiff bool) string {
	var b strings.Builder
	b.WriteString("========== 汇总报告 ==========\n")
	b.WriteString(fmt.Sprintf("%-14s %-8s %14s %14s %14s %14s %10s %8s %-22s %8s\n",
		"流", "DSCP", "TX 包", "TX 字节", "RX 包", "RX 字节", "丢包*", "乱序", "时延ms(avg/p95/p99/max)", "抖动ms"))
	var totalLost, totalReordered uint64
	for i, f := range cfg.Flows {
		if i >= len(snap) {
			break // 配置更新窗口：聚合器流数可能与配置不一致
		}
		s := snap[i]
		l := s.Lost
		if withTailDiff {
			l += tailDiff(s)
		}
		totalLost += l
		totalReordered += s.Reordered
		delay, jitter := "—", "—"
		if s.DelayValid {
			delay = fmt.Sprintf("%.1f/%.1f/%.1f/%.1f", s.DelayTotAvgMs, s.DelayP95Ms, s.DelayP99Ms, s.DelayMaxMs)
			jitter = fmt.Sprintf("%.2f", s.JitterMs)
		}
		b.WriteString(fmt.Sprintf("%-14s %-8s %14d %14d %14d %14d %10d %8d %-22s %8s\n",
			f.Name, dscpStr(f.DSCP), s.TxPackets, s.TxBytes, s.RxPackets, s.RxBytes, l, s.Reordered, delay, jitter))
	}
	b.WriteString(fmt.Sprintf("总发送 %d 字节, 总接收 %d 字节, 总丢失 %d 包, 总乱序 %d 包\n", txTotal, rxTotal, totalLost, totalReordered))
	b.WriteString("* 丢包 = seq 空洞丢失 + 停止瞬间未确认的尾部差（TX−RX−空洞丢失）；乱序不含孔内重复包\n")
	// v2.8.1 时钟偏差说明：双机无时钟同步时，时延按最小观测差值校正（相对最小时延）
	for _, s := range snap {
		if s.DelayValid {
			b.WriteString(fmt.Sprintf("时钟偏差估计: %.1f ms（接收端−发送端，接收端快为正；时延已按此校正为相对最小时延，抖动不受影响）\n", s.ClockOffsetMs))
			break
		}
	}
	return b.String()
}

// jsonFlowOut / jsonSummaryOut 是 --json 模式机器可读汇总的结构。
// 字段语义与文本汇总一致（v2.8.0）。
type jsonFlowOut struct {
	Idx           int     `json:"idx"`
	Name          string  `json:"name"`
	DSCP          string  `json:"dscp"`
	TxPackets     uint64  `json:"tx_packets"`
	RxPackets     uint64  `json:"rx_packets"`
	Lost          uint64  `json:"lost"`
	LossRatePct   float64 `json:"loss_rate_pct"`
	Reordered     uint64  `json:"reordered"` // 乱序到达包数（v2.9.0，不含孔内重复包）
	DelayValid    bool    `json:"delay_valid"`
	DelayAvgMs    float64 `json:"delay_avg_ms"` // 全程累计平均（时钟偏差校正后）
	DelayP95Ms    float64 `json:"delay_p95_ms"`
	DelayP99Ms    float64 `json:"delay_p99_ms"`
	DelayMinMs    float64 `json:"delay_min_ms"`
	DelayMaxMs    float64 `json:"delay_max_ms"`
	JitterMs      float64 `json:"jitter_ms"`
	ClockOffsetMs float64 `json:"clock_offset_ms"` // v2.8.1 时钟偏差估计（毫秒，接收端−发送端，接收端快为正）
	Checked       bool    `json:"checked"`         // 是否配置了阈值
	Pass          bool    `json:"pass"`
	FailMsg       string  `json:"fail_msg,omitempty"`
}

type jsonSummaryOut struct {
	OK         bool          `json:"ok"`
	Mode       string        `json:"mode"`
	Iface      string        `json:"iface"`
	Started    string        `json:"started"`
	Stopped    string        `json:"stopped"`
	DurationS  float64       `json:"duration_s"`
	Version    string        `json:"version"`
	HTMLReport string        `json:"html_report,omitempty"`
	Verdict    string        `json:"verdict"` // pass / fail / none（未配置阈值或发送端无判定）
	FailCount  int           `json:"fail_count"`
	Flows      []jsonFlowOut `json:"flows"`
}

// JSONSummary 渲染 --json 模式的机器可读汇总（v2.8.0）。
// 丢包口径与 Summary 一致：withTailDiff=true 计入停止瞬间未确认的尾部差；
// 判定用最终口径（final=true，由调用方传入 Verdicts 结果）。
// send 模式传入空 verdicts：不判定（无接收数据），verdict="none"。
func JSONSummary(cfg *config.Config, snap []stats.FlowSnapshot, verdicts []Verdict, meta ReportMeta, withTailDiff bool, htmlReportPath string) ([]byte, error) {
	var failCount, checkedCount int
	flows := make([]jsonFlowOut, 0, len(cfg.Flows))
	for i, f := range cfg.Flows {
		if i >= len(snap) {
			break // 配置更新窗口：聚合器流数可能与配置不一致
		}
		s := snap[i]
		effLost := s.Lost
		if withTailDiff {
			effLost += tailDiff(s)
		}
		lossPct := 0.0
		if exp := s.RxPackets + effLost; exp > 0 {
			lossPct = float64(effLost) / float64(exp) * 100
		}
		jf := jsonFlowOut{
			Idx: i, Name: f.Name, DSCP: dscpStr(f.DSCP),
			TxPackets: s.TxPackets, RxPackets: s.RxPackets,
			Lost: effLost, LossRatePct: lossPct, Reordered: s.Reordered,
			DelayValid: s.DelayValid,
		}
		if s.DelayValid {
			jf.DelayAvgMs = s.DelayTotAvgMs
			jf.DelayP95Ms = s.DelayP95Ms
			jf.DelayP99Ms = s.DelayP99Ms
			jf.DelayMinMs = s.DelayMinMs
			jf.DelayMaxMs = s.DelayMaxMs
			jf.JitterMs = s.JitterMs
			jf.ClockOffsetMs = s.ClockOffsetMs
		}
		if i < len(verdicts) {
			v := verdicts[i]
			jf.Checked, jf.Pass, jf.FailMsg = v.Checked, v.Pass, v.FailMsg
			if v.Checked {
				checkedCount++
				if !v.Pass {
					failCount++
				}
			}
		}
		flows = append(flows, jf)
	}
	verdict := "none"
	if checkedCount > 0 {
		verdict = "pass"
		if failCount > 0 {
			verdict = "fail"
		}
	}
	out := jsonSummaryOut{
		OK: failCount == 0, Mode: meta.Mode, Iface: meta.Iface,
		Started: meta.Started.Format(time.RFC3339), Stopped: meta.Stopped.Format(time.RFC3339),
		DurationS: meta.Stopped.Sub(meta.Started).Seconds(),
		Version:   meta.Version, HTMLReport: htmlReportPath,
		Verdict: verdict, FailCount: failCount, Flows: flows,
	}
	return json.MarshalIndent(out, "", "  ")
}
