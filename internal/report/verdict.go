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
// useTotDelay=true 用全程累计平均时延（最终报告/汇总），false 用 1s 窗口平均（Web 实时）。
// 无阈值配置的流 Checked=false；无数据时对应项不判定。
func Verdicts(cfg *config.Config, snap []stats.FlowSnapshot, useTotDelay bool) []Verdict {
	out := make([]Verdict, len(cfg.Flows))
	for i, f := range cfg.Flows {
		s := snap[i]
		v := Verdict{FlowIdx: i, Pass: true}
		if f.MaxLossRatePct > 0 && (s.RxPackets > 0 || s.Lost > 0) {
			v.Checked = true
			if pct := s.LossRate * 100; pct > f.MaxLossRatePct {
				v.Pass = false
				v.FailMsg = fmt.Sprintf("丢包率 %.2f%% > %.2f%%", pct, f.MaxLossRatePct)
			}
		}
		if f.MaxAvgDelayMs > 0 && s.DelayValid {
			v.Checked = true
			avg := s.DelayAvgMs
			if useTotDelay {
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
