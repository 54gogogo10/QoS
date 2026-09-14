package report

import (
	"fmt"

	"qostool/internal/config"
	"qostool/internal/stats"
)

// Verdict 是单条流的阈值判定结果。
type Verdict struct {
	FlowIdx int
	Checked bool   // 是否配置了任何阈值
	Pass    bool
	FailMsg string // 违反的阈值描述（多项用 "; " 连接）
}

// Verdicts 判定全部流。
// final=true 为最终判定（报告/汇总）：时延用全程累计平均，丢包与汇总口径一致（seq 空洞 + 尾部差）；
// final=false 为实时判定（Web）：时延用 100ms 窗口平均，丢包仅 seq 空洞（避免测试启动瞬间误报）。
// 无阈值配置的流 Checked=false；无数据时对应项不判定（最终判定下"只发不收"视为 100% 丢包）。
// snap 比 cfg.Flows 短时（配置更新与聚合器替换之间的短暂窗口）多出的流不判定，不越界。
func Verdicts(cfg *config.Config, snap []stats.FlowSnapshot, final bool) []Verdict {
	out := make([]Verdict, len(cfg.Flows))
	for i, f := range cfg.Flows {
		if i >= len(snap) {
			continue // 聚合器仍是上一轮流数较少的运行：该流暂无数据
		}
		s := snap[i]
		v := Verdict{FlowIdx: i, Pass: true}
		if f.MaxLossRatePct > 0 && (s.RxPackets > 0 || s.Lost > 0 || (final && s.TxPackets > 0)) {
			v.Checked = true
			pct := s.LossRate * 100
			if final {
				// 最终口径 = 汇总口径：seq 空洞 + 尾部差（发而未收）
				effLost := s.Lost
				if s.TxPackets > s.RxPackets+s.Lost {
					effLost += s.TxPackets - s.RxPackets - s.Lost
				}
				if exp := s.RxPackets + effLost; exp > 0 {
					pct = float64(effLost) / float64(exp) * 100
				}
			}
			if pct > f.MaxLossRatePct {
				v.Pass = false
				v.FailMsg = fmt.Sprintf("丢包率 %.2f%% > %.2f%%", pct, f.MaxLossRatePct)
			}
		}
		if f.MaxAvgDelayMs > 0 && s.DelayValid {
			v.Checked = true
			avg := s.DelayAvgMs
			if final {
				avg = s.DelayTotAvgMs
			}
			if avg > f.MaxAvgDelayMs {
				if !v.Pass {
					v.FailMsg += "; "
				}
				v.Pass = false
				v.FailMsg += fmt.Sprintf("平均时延 %.2fms > %.2fms", avg, f.MaxAvgDelayMs)
			}
		}
		if f.MaxJitterMs > 0 && s.DelayValid {
			v.Checked = true
			if s.JitterMs > f.MaxJitterMs {
				if !v.Pass {
					v.FailMsg += "; "
				}
				v.Pass = false
				v.FailMsg += fmt.Sprintf("抖动 %.2fms > %.2fms", s.JitterMs, f.MaxJitterMs)
			}
		}
		out[i] = v
	}
	return out
}
