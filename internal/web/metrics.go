package web

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"qostool/internal/config"
	"qostool/internal/stats"
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
	b = appendGauge(b, "qostool_running", "Whether a test is currently running (1=yes).",
		boolFloat(st.Running), `mode="`+string(m)+`"`)
	b = appendGauge(b, "qostool_live_scale", "Current live rate scale factor relative to configured rates (v2.10.0).",
		st.LiveScale, `mode="`+string(m)+`"`)

	if agg := ctrl.Aggregator(); agg != nil {
		cfg := ctrl.Config()
		snap := agg.Current(now)
		type flowRow struct {
			labels string
			s      stats.FlowSnapshot
		}
		rows := make([]flowRow, 0, len(cfg.Flows))
		anyDelay := false
		for i := range cfg.Flows {
			if i >= len(snap) {
				break // 配置更新窗口：聚合器流数与配置不一致时跳过多出的流
			}
			rows = append(rows, flowRow{labels: flowLabels(i, &cfg.Flows[i]), s: snap[i]})
			if snap[i].DelayValid {
				anyDelay = true
			}
		}
		// 族级输出：每族一次 HELP/TYPE 头 + 族内全部序列（exposition 标准布局；
		// 此前按流循环会重复输出元数据，8 流时响应体多约 1/3）
		for _, fam := range metricFamilies {
			if fam.delayOnly && !anyDelay {
				continue
			}
			typ := "gauge"
			if fam.counter {
				typ = "counter"
			}
			b = append(b, "# HELP "+fam.name+" "+fam.help+"\n# TYPE "+fam.name+" "+typ+"\n"...)
			for _, row := range rows {
				if fam.delayOnly && !row.s.DelayValid {
					continue
				}
				b = append(b, fam.name+"{"+row.labels+"} "+fam.value(row.s)+"\n"...)
			}
		}
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Write(b)
}

// metricFam 描述一个每流指标族。
type metricFam struct {
	name      string
	help      string
	counter   bool // true=counter，false=gauge
	delayOnly bool // 仅在存在时延数据时输出该族
	value     func(stats.FlowSnapshot) string
}

// metricFamilies 每流指标族清单（顺序即输出顺序）。
// 闭包无捕获，包级初始化一次，请求处理零额外分配。
var metricFamilies = []metricFam{
	{name: "qostool_tx_bps", help: "Current transmit rate in bytes per second.",
		value: func(s stats.FlowSnapshot) string { return formatFloat(s.TxBps) }},
	{name: "qostool_tx_pps", help: "Current transmit rate in packets per second.",
		value: func(s stats.FlowSnapshot) string { return formatFloat(s.TxPps) }},
	{name: "qostool_rx_bps", help: "Current receive rate in bytes per second.",
		value: func(s stats.FlowSnapshot) string { return formatFloat(s.RxBps) }},
	{name: "qostool_rx_pps", help: "Current receive rate in packets per second.",
		value: func(s stats.FlowSnapshot) string { return formatFloat(s.RxPps) }},
	{name: "qostool_tx_packets_total", help: "Total transmitted packets.", counter: true,
		value: func(s stats.FlowSnapshot) string { return strconv.FormatUint(s.TxPackets, 10) }},
	{name: "qostool_tx_bytes_total", help: "Total transmitted bytes.", counter: true,
		value: func(s stats.FlowSnapshot) string { return strconv.FormatUint(s.TxBytes, 10) }},
	{name: "qostool_rx_packets_total", help: "Total received packets.", counter: true,
		value: func(s stats.FlowSnapshot) string { return strconv.FormatUint(s.RxPackets, 10) }},
	{name: "qostool_rx_bytes_total", help: "Total received bytes.", counter: true,
		value: func(s stats.FlowSnapshot) string { return strconv.FormatUint(s.RxBytes, 10) }},
	{name: "qostool_lost_total", help: "Total packets detected lost (seq gaps incl. settled holes).", counter: true,
		value: func(s stats.FlowSnapshot) string { return strconv.FormatUint(s.Lost, 10) }},
	{name: "qostool_reordered_total", help: "Total packets arrived out of order.", counter: true,
		value: func(s stats.FlowSnapshot) string { return strconv.FormatUint(s.Reordered, 10) }},
	{name: "qostool_loss_rate", help: "Loss rate 0..1 (seq holes / expected).",
		value: func(s stats.FlowSnapshot) string { return formatFloat(s.LossRate) }},
	{name: "qostool_delay_avg_ms", help: "Window average one-way delay, relative to min observed (ms).", delayOnly: true,
		value: func(s stats.FlowSnapshot) string { return formatFloat(s.DelayAvgMs) }},
	{name: "qostool_delay_tot_avg_ms", help: "Total average one-way delay over the whole run (ms).", delayOnly: true,
		value: func(s stats.FlowSnapshot) string { return formatFloat(s.DelayTotAvgMs) }},
	{name: "qostool_delay_min_ms", help: "Minimum one-way delay over the whole run (ms).", delayOnly: true,
		value: func(s stats.FlowSnapshot) string { return formatFloat(s.DelayMinMs) }},
	{name: "qostool_delay_max_ms", help: "Maximum one-way delay over the whole run (ms).", delayOnly: true,
		value: func(s stats.FlowSnapshot) string { return formatFloat(s.DelayMaxMs) }},
	{name: "qostool_delay_p95_ms", help: "Total p95 one-way delay (ms, 1ms histogram buckets).", delayOnly: true,
		value: func(s stats.FlowSnapshot) string { return formatFloat(s.DelayP95Ms) }},
	{name: "qostool_delay_p99_ms", help: "Total p99 one-way delay (ms, 1ms histogram buckets).", delayOnly: true,
		value: func(s stats.FlowSnapshot) string { return formatFloat(s.DelayP99Ms) }},
	{name: "qostool_jitter_ms", help: "RFC3550 jitter (ms).", delayOnly: true,
		value: func(s stats.FlowSnapshot) string { return formatFloat(s.JitterMs) }},
	{name: "qostool_clock_offset_ms", help: "Estimated clock offset used for delay correction (ms).", delayOnly: true,
		value: func(s stats.FlowSnapshot) string { return formatFloat(s.ClockOffsetMs) }},
}

// appendGauge 追加一个单序列 gauge（状态类指标，无流标签）。
func appendGauge(b []byte, name, help string, value float64, labels string) []byte {
	b = append(b, "# HELP "+name+" "+help+"\n# TYPE "+name+" gauge\n"...)
	return append(b, name+"{"+labels+"} "+formatFloat(value)+"\n"...)
}

// flowLabels 渲染 Prometheus 标签串（name/IP 等做转义防注入破坏 exposition 格式）。
func flowLabels(idx int, f *config.Flow) string {
	return `flow_idx="` + strconv.Itoa(idx) +
		`",flow="` + escapeLabel(f.Name) +
		`",dscp="` + strconv.Itoa(int(f.DSCP)) +
		`",src="` + escapeLabel(f.SrcIP+":"+strconv.Itoa(f.SrcPort)) +
		`",dst="` + escapeLabel(f.DstIP+":"+strconv.Itoa(f.DstPort)) + `"`
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
