// Package sweep 实现吞吐量阶梯扫描：按比例逐档加压，测出每条流"丢包率突破阈值"的极限速率。
// 仅双向模式（本机发送 + 本机抓包）：损耗测量依赖本端抓包。
package sweep

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"qostool/internal/capture"
	"qostool/internal/config"
	"qostool/internal/sender"
	"qostool/internal/stats"
)

// Options 是阶梯扫描参数；零值使用默认值。
type Options struct {
	StepPct         float64       // 每档递增百分比，默认 20
	Hold            time.Duration // 每档保持时长，默认 10s（含 0.5s settle）
	MaxScale        float64       // 最大倍率，默认 10
	LossThresholdPct float64      // 全局丢包率阈值 %，默认 0.5（被每流 max_loss_rate_pct 覆盖）
}

// StepOutcome 是单档的测量结果。
type StepOutcome struct {
	Scale    float64   // 相对 base 的倍率
	RateMbps []float64 // 每流实际 Mbps（纯 pps 流为 0）
	LossPct  []float64 // 每流窗口丢包率 %（窗口无数据 = -1）
	Over     bool      // 任一流超阈值
}

// Result 是扫描的最终结果。
type Result struct {
	Steps      []StepOutcome
	LimitScale float64   // 最后一档全部 PASS 的倍率（0 = 起始档即越限）
	LimitMbps  []float64 // 该档每流速率（全 0 = 低于起始档）
	DoneReason string
}

// ScaleRates 把每流速率按 factor 等比缩放（两速率同乘）。
func ScaleRates(base []config.Flow, factor float64) []sender.RateSpec {
	out := make([]sender.RateSpec, len(base))
	for i, f := range base {
		out[i] = sender.RateSpec{RateMbps: f.RateMbps * factor, RatePPS: f.RatePPS * factor}
	}
	return out
}

// LimitRates 返回极限档（factor 倍率）每流的 Mbps 速率（纯 pps 流为 0，
// 其极限速率由调用方按 RatePPS×factor 换算）。
func LimitRates(base []config.Flow, factor float64) []float64 {
	out := make([]float64, len(base))
	for i, f := range base {
		out[i] = f.RateMbps * factor
	}
	return out
}

// PickLossThresholds 逐流取阈值：配置了 max_loss_rate_pct 用配置值，否则用全局默认。
func PickLossThresholds(cfg *config.Config, def float64) []float64 {
	th := make([]float64, len(cfg.Flows))
	for i, f := range cfg.Flows {
		if f.MaxLossRatePct > 0 {
			th[i] = f.MaxLossRatePct
		} else {
			th[i] = def
		}
	}
	return th
}

// ---------- --json 机器可读汇总（v2.9.0） ----------

type jsonStepOut struct {
	Scale   float64   `json:"scale"`    // 相对 base 的倍率
	LossPct []float64 `json:"loss_pct"` // 每流窗口丢包率 %（-1=窗口无数据）
	Over    bool      `json:"over"`     // 任一流超阈值
}

type jsonFlowOut struct {
	Idx       int     `json:"idx"`
	Name      string  `json:"name"`
	BaseMbps  float64 `json:"base_mbps"`            // 配置的起始速率（纯 pps 流为 0）
	BasePPS   float64 `json:"base_pps"`             // 配置的起始 pps（纯带宽流为 0）
	LimitMbps float64 `json:"limit_mbps"`           // 极限速率（LimitScale 倍；起始档越限时为 0）
	LimitPPS  float64 `json:"limit_pps"`            // 极限 pps（同上）
	Threshold float64 `json:"loss_threshold_pct"`   // 该流生效的丢包阈值 %
}

type jsonOut struct {
	Version      string        `json:"version"`
	StepPct      float64       `json:"step_pct"`
	HoldS        int           `json:"hold_s"`
	MaxScale     float64       `json:"max_scale"`
	LimitScale   float64       `json:"limit_scale"` // 最后一档全部 PASS 的倍率（0=起始档即越限）
	DoneReason   string        `json:"done_reason"`
	Verdict      string        `json:"verdict"` // pass=找到极限或全档通过, fail=起始档即越限
	Flows        []jsonFlowOut `json:"flows"`
	Steps        []jsonStepOut `json:"steps"`
	CSVReport    string        `json:"csv_report,omitempty"` // 明细 CSV 路径（由调用方填）
}

// JSONSummary 渲染扫描结果的机器可读 JSON（sweep --json 模式，v2.9.0）。
// verdict 与退出码契约一致：limit_scale<=0（起始档即越限）为 fail，否则 pass。
// csvPath 为明细 CSV 路径（可为空）。
func JSONSummary(cfg *config.Config, res *Result, opts Options, version, csvPath string) ([]byte, error) {
	thresholds := PickLossThresholds(cfg, opts.LossThresholdPct)
	// 默认值钳制与 Run 一致（Hold <2s 视为未设置 → 10s），保证 JSON 报告
	// 的 hold_s 与实际执行参数相符
	hold := opts.Hold
	if hold < 2*time.Second {
		hold = 10 * time.Second
	}
	stepPct := opts.StepPct
	if stepPct <= 0 {
		stepPct = 20
	}
	maxScale := opts.MaxScale
	if maxScale <= 1 {
		maxScale = 10
	}
	out := jsonOut{
		Version: version, StepPct: stepPct, HoldS: int(hold.Seconds()), MaxScale: maxScale,
		LimitScale: res.LimitScale, DoneReason: res.DoneReason, Verdict: "pass", CSVReport: csvPath,
	}
	if res.LimitScale <= 0 {
		out.Verdict = "fail"
	}
	for i, f := range cfg.Flows {
		th := opts.LossThresholdPct
		if i < len(thresholds) {
			th = thresholds[i]
		}
		out.Flows = append(out.Flows, jsonFlowOut{
			Idx: i, Name: f.Name, BaseMbps: f.RateMbps, BasePPS: f.RatePPS,
			LimitMbps: f.RateMbps * res.LimitScale, LimitPPS: f.RatePPS * res.LimitScale,
			Threshold: th,
		})
	}
	for _, st := range res.Steps {
		out.Steps = append(out.Steps, jsonStepOut{Scale: st.Scale, LossPct: st.LossPct, Over: st.Over})
	}
	return json.MarshalIndent(out, "", "  ")
}

type stepAction int

const (
	actContinue stepAction = iota // 该档全过，加压到下一档
	actConfirm                    // 首越限：同档再测一个 hold 确认（防抖动误判）
	actStop                       // 确认仍越限 / 达到 maxScale：停止
)

// stepMachine 是档位决策状态机（纯逻辑，可单测）。
type stepMachine struct {
	stepPct      float64
	maxScale     float64
	thresholds   []float64
	nextScale    float64 // 下一档倍率
	limitScale   float64 // 最后一档全 PASS 的倍率
	pendingScale float64 // 待确认的越限档（0=无）
	doneReason   string
}

func newStepMachine(stepPct, maxScale float64, thresholds []float64) *stepMachine {
	return &stepMachine{stepPct: stepPct, maxScale: maxScale, thresholds: thresholds}
}

// next 处理一档测量结果，返回动作与下一档倍率（actStop 时结果字段就绪）。
func (m *stepMachine) next(scale float64, losses []float64) stepAction {
	over := false
	for i, l := range losses {
		if l >= 0 && l > m.thresholds[i] {
			over = true
			break
		}
	}
	// 首次越限：保持同档，要求再测一个 hold 确认
	if over && m.pendingScale == 0 {
		m.pendingScale = scale
		m.nextScale = scale
		return actConfirm
	}
	// 确认档：仍越限 → 停止；恢复 → 解除确认继续加压
	if m.pendingScale != 0 {
		m.pendingScale = 0
		if over {
			m.doneReason = "越限档已确认（丢包率超阈值且同档复测仍超）"
			return actStop
		}
		// 抖动误判恢复：继续
	}
	m.limitScale = scale // 该档全 PASS，暂记极限
	ns := scale * (1 + m.stepPct/100)
	if ns > m.maxScale {
		m.doneReason = "已达到最大倍率且全部通过"
		m.nextScale = ns
		return actStop
	}
	m.nextScale = ns
	return actContinue
}

// Run 执行阶梯扫描：逐档加压测量，返回结果。
// cfg 必须为发送端校验通过的配置；iface 为监听接口。
func Run(ctx context.Context, cfg *config.Config, iface string, opts Options) (*Result, error) {
	if opts.StepPct <= 0 {
		opts.StepPct = 20
	}
	if opts.Hold < 2*time.Second {
		opts.Hold = 10 * time.Second
	}
	if opts.MaxScale <= 1 {
		opts.MaxScale = 10
	}
	if opts.LossThresholdPct <= 0 || opts.LossThresholdPct > 100 {
		opts.LossThresholdPct = 0.5
	}
	const settle = 500 * time.Millisecond

	agg := stats.NewAggregator(len(cfg.Flows), 10)
	s, err := sender.New(cfg, agg)
	if err != nil {
		return nil, err
	}
	c, err := capture.New(iface, cfg, agg)
	if err != nil {
		s.Close()
		return nil, err
	}

	sCtx, sCancel := context.WithCancel(context.Background())
	cCtx, cCancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); s.Run(sCtx) }()
	go func() { defer wg.Done(); c.Run(cCtx) }()

	// 扫描主体在独立 goroutine：Run 需响应 ctx 取消（Ctrl+C）
	type scanResult struct {
		res *Result
		err error
	}
	done := make(chan scanResult, 1)
	go func() {
		res, err := scan(ctx, cfg, opts, s, agg, settle)
		done <- scanResult{res, err}
	}()

	var sr scanResult
	select {
	case <-ctx.Done():
		sr = scanResult{nil, ctx.Err()}
	case sr = <-done:
	}

	// 收尾顺序同 controller：先停发送 → drain 在途包 → 停抓包（Run 的 defer 关闭句柄）
	sCancel()
	time.Sleep(500 * time.Millisecond)
	cCancel()
	wg.Wait()
	s.Close()
	return sr.res, sr.err
}

// scan 执行档位循环（Run 的 goroutine 内，可被 ctx 取消打断）。
func scan(ctx context.Context, cfg *config.Config, opts Options, s *sender.Sender, agg *stats.Aggregator, settle time.Duration) (*Result, error) {
	m := newStepMachine(opts.StepPct, opts.MaxScale, PickLossThresholds(cfg, opts.LossThresholdPct))
	res := &Result{}
	scale := 1.0
	for {
		if err := s.SetRates(ScaleRates(cfg.Flows, scale)); err != nil {
			return nil, err
		}
		time.Sleep(settle) // 速率切换后等 pacing 稳定
		before := agg.Current(time.Now())
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		case <-time.After(opts.Hold - settle):
		}
		after := agg.Current(time.Now())

		losses := make([]float64, len(cfg.Flows))
		rates := make([]float64, len(cfg.Flows))
		over := false
		for i := range cfg.Flows {
			lostΔ := after[i].Lost - before[i].Lost
			rxΔ := after[i].RxPackets - before[i].RxPackets
			if rxΔ+lostΔ > 0 {
				losses[i] = float64(lostΔ) / float64(rxΔ+lostΔ) * 100
			} else {
				losses[i] = -1 // 窗口无数据
			}
			rates[i] = cfg.Flows[i].RateMbps * scale
			if losses[i] >= 0 && losses[i] > m.thresholds[i] {
				over = true
			}
		}
		res.Steps = append(res.Steps, StepOutcome{Scale: scale, RateMbps: rates, LossPct: losses, Over: over})

		switch m.next(scale, losses) {
		case actStop:
			res.LimitScale = m.limitScale
			res.LimitMbps = LimitRates(cfg.Flows, m.limitScale)
			res.DoneReason = m.doneReason
			return res, nil
		case actConfirm:
			// 同档复测：不记重复档位，直接进入下一轮（scale 不变）
		case actContinue:
			scale = m.nextScale
		}
	}
}
