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

// Table 渲染实时统计表格（纯文本，终端重绘时由调用方清屏）。
func Table(cfg *config.Config, snap []stats.FlowSnapshot, haveSender, haveCapture bool) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%-14s %-8s %-40s %10s %10s %10s %10s %8s\n",
		"流", "DSCP", "5元组", "TX Mbps", "TX pps", "RX Mbps", "RX pps", "丢包%"))
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
		b.WriteString(fmt.Sprintf("%-14s %-8s %-40s %10s %10s %10s %10s %8s\n",
			f.Name, dscpStr(f.DSCP), five, txMbps, txPps, rxMbps, rxPps, loss))
	}
	return b.String()
}

// Summary 渲染运行结束后的汇总报告。
func Summary(cfg *config.Config, snap []stats.FlowSnapshot, txTotal, rxTotal, lost uint64) string {
	var b strings.Builder
	b.WriteString("========== 汇总报告 ==========\n")
	b.WriteString(fmt.Sprintf("%-14s %-8s %14s %14s %14s %14s %10s\n",
		"流", "DSCP", "TX 包", "TX 字节", "RX 包", "RX 字节", "丢包"))
	for i, f := range cfg.Flows {
		s := snap[i]
		b.WriteString(fmt.Sprintf("%-14s %-8s %14d %14d %14d %14d %10d\n",
			f.Name, dscpStr(f.DSCP), s.TxPackets, s.TxBytes, s.RxPackets, s.RxBytes, s.Lost))
	}
	b.WriteString(fmt.Sprintf("总发送 %d 字节, 总接收 %d 字节, 总丢失 %d 包\n", txTotal, rxTotal, lost))
	return b.String()
}
