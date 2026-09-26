package web

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// handleMetrics 输出 Prometheus 文本格式指标（v2.10.0，只读）：
// 每流速率/包数/丢包/乱序/时延/抖动 + 运行状态与调速倍率，
// 便于长跑测试接入现有监控（抓取地址 http://<本机>:16666/metrics）。
// 口径与 /api/stats 完全一致（?mode= 选择角色，缺省 bidir）。
// 只读端点：不走控制门禁（与 stats/status 同级），不暴露比页面更多信息。
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	m := modeFromQuery(r)
	ctrl := s.ctrl(m)
	now := time.Now()
	st := ctrl.Status()
	var b []byte

	gauge := func(name, help string, value float64, labels string) {
		b = append(b, "# HELP "+name+" "+help+"\n# TYPE "+name+" gauge\n"...)
		b = append(b, name+"{"+labels+"} "+formatFloat(value)+"\n"...)
	}
	gauge("qostool_running", "Whether a test is currently running (1=yes).",
		boolFloat(st.Running), `mode="`+string(m)+`"`)
	gauge("qostool_live_scale", "Current live rate scale factor relative to configured rates (v2.10.0).",
		st.LiveScale, `mode="`+string(m)+`"`)

	agg := ctrl.Aggregator()
	if agg != nil {
		cfg := ctrl.Config()
		snap := agg.Current(now)
		for i, f := range cfg.Flows {
			if i >= len(snap) {
				break // 配置更新窗口：聚合器流数与配置不一致时跳过多出的流
			}
			s := snap[i]
			labels := flowLabels(i, f.Name, int(f.DSCP), f.SrcIP, f.SrcPort, f.DstIP, f.DstPort)
			gauge("qostool_tx_bps", "Current transmit rate in bytes per second.", s.TxBps, labels)
			gauge("qostool_tx_pps", "Current transmit rate in packets per second.", s.TxPps, labels)
			gauge("qostool_rx_bps", "Current receive rate in bytes per second.", s.RxBps, labels)
			gauge("qostool_rx_pps", "Current receive rate in packets per second.", s.RxPps, labels)
			counter := func(name, help string, value uint64) {
				b = append(b, "# HELP "+name+" "+help+"\n# TYPE "+name+" counter\n"...)
				b = append(b, name+"{"+labels+"} "+strconv.FormatUint(value, 10)+"\n"...)
			}
			counter("qostool_tx_packets_total", "Total transmitted packets.", s.TxPackets)
			counter("qostool_tx_bytes_total", "Total transmitted bytes.", s.TxBytes)
			counter("qostool_rx_packets_total", "Total received packets.", s.RxPackets)
			counter("qostool_rx_bytes_total", "Total received bytes.", s.RxBytes)
			counter("qostool_lost_total", "Total packets detected lost (seq gaps incl. settled holes).", s.Lost)
			counter("qostool_reordered_total", "Total packets arrived out of order.", s.Reordered)
			gauge("qostool_loss_rate", "Loss rate 0..1 (seq holes / expected).", s.LossRate, labels)
			if s.DelayValid {
				gauge("qostool_delay_avg_ms", "Window average one-way delay, relative to min observed (ms).", s.DelayAvgMs, labels)
				gauge("qostool_delay_tot_avg_ms", "Total average one-way delay over the whole run (ms).", s.DelayTotAvgMs, labels)
				gauge("qostool_delay_min_ms", "Minimum one-way delay over the whole run (ms).", s.DelayMinMs, labels)
				gauge("qostool_delay_max_ms", "Maximum one-way delay over the whole run (ms).", s.DelayMaxMs, labels)
				gauge("qostool_delay_p95_ms", "Total p95 one-way delay (ms, 1ms histogram buckets).", s.DelayP95Ms, labels)
				gauge("qostool_delay_p99_ms", "Total p99 one-way delay (ms, 1ms histogram buckets).", s.DelayP99Ms, labels)
				gauge("qostool_jitter_ms", "RFC3550 jitter (ms).", s.JitterMs, labels)
				gauge("qostool_clock_offset_ms", "Estimated clock offset used for delay correction (ms).", s.ClockOffsetMs, labels)
			}
		}
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Write(b)
}

// flowLabels 渲染 Prometheus 标签串（name/IP 等做转义防注入破坏 exposition 格式）。
func flowLabels(idx int, name string, dscp int, srcIP string, srcPort int, dstIP string, dstPort int) string {
	return `flow_idx="` + strconv.Itoa(idx) +
		`",flow="` + escapeLabel(name) +
		`",dscp="` + strconv.Itoa(dscp) +
		`",src="` + escapeLabel(netJoinLite(srcIP, srcPort)) +
		`",dst="` + escapeLabel(netJoinLite(dstIP, dstPort)) + `"`
}

func netJoinLite(ip string, port int) string {
	return ip + ":" + strconv.Itoa(port)
}

// escapeLabel 转义 Prometheus label value 中的反斜杠、双引号与换行。
func escapeLabel(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	v = strings.ReplaceAll(v, "\n", `\n`)
	return v
}

// formatFloat 输出 Prometheus 可解析的浮点（无 NaN/Inf——stats 输出均为有限值）。
func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'g', -1, 64)
}

func boolFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
