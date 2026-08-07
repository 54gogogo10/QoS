// Package report 渲染终端实时表格与汇总报告。
package report

import (
	"fmt"
	"strings"

	"qostool/internal/config"
	"qostool/internal/stats"
)

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
func Table(cfg *config.Config, snap []stats.FlowSnapshot, haveSender, haveCapture bool) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%-14s %-8s %-40s %10s %10s %10s %10s %8s %14s %8s\n",
		"流", "DSCP", "5元组", "TX Mbps", "TX pps", "RX Mbps", "RX pps", "丢包%", "时延ms", "抖动ms"))
	for i, f := range cfg.Flows {
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
// withTailDiff=false 时不计尾部差：recv 模式的 TX 来自远端轮询快照（滞后最多 1s），
// “停止瞬间未确认的尾部包”语义不成立，计入会得到虚假的滞后差。
func Summary(cfg *config.Config, snap []stats.FlowSnapshot, txTotal, rxTotal, lost uint64, withTailDiff bool) string {
	var b strings.Builder
	b.WriteString("========== 汇总报告 ==========\n")
	b.WriteString(fmt.Sprintf("%-14s %-8s %14s %14s %14s %14s %10s %16s %8s\n",
		"流", "DSCP", "TX 包", "TX 字节", "RX 包", "RX 字节", "丢包*", "时延ms(avg/m/m)", "抖动ms"))
	var totalLost uint64
	for i, f := range cfg.Flows {
		s := snap[i]
		l := s.Lost
		if withTailDiff {
			l += tailDiff(s)
		}
		totalLost += l
		delay, jitter := "—", "—"
		if s.DelayValid {
			delay = fmt.Sprintf("%.1f/%.1f/%.1f", s.DelayTotAvgMs, s.DelayMinMs, s.DelayMaxMs)
			jitter = fmt.Sprintf("%.2f", s.JitterMs)
		}
		b.WriteString(fmt.Sprintf("%-14s %-8s %14d %14d %14d %14d %10d %16s %8s\n",
			f.Name, dscpStr(f.DSCP), s.TxPackets, s.TxBytes, s.RxPackets, s.RxBytes, l, delay, jitter))
	}
	b.WriteString(fmt.Sprintf("总发送 %d 字节, 总接收 %d 字节, 总丢失 %d 包\n", txTotal, rxTotal, totalLost))
	b.WriteString("* 丢包 = seq 空洞丢失 + 停止瞬间未确认的尾部差（TX−RX−空洞丢失）\n")
	return b.String()
}
