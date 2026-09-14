package sweep

import (
	"math"
	"strings"
	"testing"
	"time"

	"qostool/internal/config"
	"qostool/internal/sender"
)

func TestScaleRates(t *testing.T) {
	base := []config.Flow{
		{RateMbps: 10, RatePPS: 0},
		{RateMbps: 0, RatePPS: 500},
		{RateMbps: 2, RatePPS: 100},
	}
	rs := ScaleRates(base, 1.5)
	want := []sender.RateSpec{{RateMbps: 15, RatePPS: 0}, {RateMbps: 0, RatePPS: 750}, {RateMbps: 3, RatePPS: 150}}
	for i := range want {
		if rs[i] != want[i] {
			t.Fatalf("ScaleRates[%d] = %+v, want %+v", i, rs[i], want[i])
		}
	}
	// 倍率 1.0 应恒等
	rs1 := ScaleRates(base, 1.0)
	for i := range base {
		if rs1[i].RateMbps != base[i].RateMbps || rs1[i].RatePPS != base[i].RatePPS {
			t.Fatalf("1.0 倍率应恒等: %+v", rs1)
		}
	}
}

func TestPickLossThresholds(t *testing.T) {
	cfg := &config.Config{Flows: []config.Flow{{MaxLossRatePct: 1.0}, {}}}
	th := PickLossThresholds(cfg, 0.5)
	if len(th) != 2 || th[0] != 1.0 || th[1] != 0.5 {
		t.Fatalf("阈值 = %v, want [1 0.5]", th)
	}
}

// 步进决策机：首越限 → 同档确认；确认仍越限 → 停止；恢复 → 继续加压。
func TestStepMachineConfirmThenStop(t *testing.T) {
	m := newStepMachine(20, 10, []float64{0.5, 0.5}) // step 20%, maxScale 10
	// 档1 (1.0x)：全过 → 继续
	if act := m.next(1.0, []float64{0, 0.1}); act != actContinue {
		t.Fatalf("档1 应继续, got %v", act)
	}
	// 档2 (1.2x)：越限 → 确认（同档再测）
	if act := m.next(1.2, []float64{0.9, 1.5}); act != actConfirm {
		t.Fatalf("档2 应确认, got %v", act)
	}
	// 确认（同档 1.2x）仍越限 → 停止，极限 = 档1 (1.0x)
	act := m.next(1.2, []float64{1.2, 0.7})
	if act != actStop {
		t.Fatalf("确认后应停止, got %v", act)
	}
	if m.limitScale != 1.0 {
		t.Fatalf("limitScale = %v, want 1.0", m.limitScale)
	}
}

func TestStepMachineRecoverAfterOverLimit(t *testing.T) {
	m := newStepMachine(20, 10, []float64{0.5, 0.5})
	m.next(1.0, []float64{0, 0})
	// 档2 越限 → 确认
	if act := m.next(1.2, []float64{1.5, 0}); act != actConfirm {
		t.Fatalf("应确认, got %v", act)
	}
	// 同档恢复（抖动误判）→ 继续到 1.44x
	if act := m.next(1.2, []float64{0, 0}); act != actContinue {
		t.Fatalf("恢复应继续, got %v", act)
	}
	if m.nextScale != 1.44 {
		t.Fatalf("nextScale = %v, want 1.44", m.nextScale)
	}
}

func TestStepMachineFirstStepOverLimit(t *testing.T) {
	m := newStepMachine(20, 10, []float64{0.5, 0.5})
	m.next(1.0, []float64{5.0, 3.0}) // 起始档即越限 → 确认
	if act := m.next(1.0, []float64{4.0, 2.0}); act != actStop {
		t.Fatalf("确认仍越限应停止, got %v", act)
	}
	if m.limitScale != 0 {
		t.Fatalf("起始档越限 limitScale 应为 0, got %v", m.limitScale)
	}
}

func TestStepMachineMaxScaleAllPass(t *testing.T) {
	m := newStepMachine(20, 2.5, []float64{0.5, 0.5})
	// 1.0 → 1.2 → 1.44 → 1.728 → 2.0736 → 下一档 2.488 > maxScale 2.5 → 停止
	scale := 1.0
	for {
		act := m.next(scale, []float64{0, 0})
		if act == actStop {
			break
		}
		if act != actContinue && act != actConfirm {
			t.Fatalf("意外动作 %v", act)
		}
		if act == actConfirm {
			t.Fatal("全过不应要求确认")
		}
		scale = m.nextScale
		if scale > 10 {
			t.Fatal("循环未终止")
		}
	}
	if m.limitScale < 2.0 {
		t.Fatalf("全过到 maxScale 极限应为最后一档, got %v", m.limitScale)
	}
	if m.doneReason == "" {
		t.Fatal("应有结束原因")
	}
}

// TestLimitRates 极限档每流 Mbps 速率 = base × 倍率（纯 pps 流为 0，由调用方换算 pps）。
// 回归：Result.LimitMbps 之前恒为零导致"极限 0.00 Mbps"输出。
func TestLimitRates(t *testing.T) {
	base := []config.Flow{
		{RateMbps: 10, RatePPS: 0},
		{RateMbps: 0, RatePPS: 500},
		{RateMbps: 2, RatePPS: 100},
	}
	got := LimitRates(base, 1.44)
	want := []float64{14.4, 0, 2.88}
	for i := range want {
		if math.Abs(got[i]-want[i]) > 1e-9 {
			t.Fatalf("LimitRates[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

// TestJSONSummary sweep --json 机器可读汇总（v2.9.0）：verdict 与退出码契约一致。
func TestJSONSummary(t *testing.T) {
	cfg := &config.Config{Flows: []config.Flow{
		{Name: "f1", RateMbps: 10, RatePPS: 0, MaxLossRatePct: 1},
		{Name: "f2", RateMbps: 0, RatePPS: 1000},
	}}
	res := &Result{
		Steps: []StepOutcome{
			{Scale: 1.0, LossPct: []float64{0.1, -1}, Over: false},
			{Scale: 1.2, LossPct: []float64{0.2, 0.3}, Over: false},
			{Scale: 1.44, LossPct: []float64{2.0, 0.4}, Over: true},
		},
		LimitScale: 1.2, LimitMbps: []float64{12, 0}, DoneReason: "越限档已确认",
	}
	b, err := JSONSummary(cfg, res, Options{StepPct: 20, Hold: 10 * time.Second, MaxScale: 10, LossThresholdPct: 0.5}, "v2.9.0", "logs/x.csv")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		`"verdict": "pass"`, `"limit_scale": 1.2`, `"csv_report": "logs/x.csv"`,
		`"loss_threshold_pct": 1`, // f1 用配置阈值
		`"loss_threshold_pct": 0.5`, // f2 用全局默认
		`"limit_mbps": 12`, `"limit_pps": 1200`,
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("JSON 缺少 %q:\n%s", want, s)
		}
	}

	// 起始档越限：verdict=fail
	res2 := &Result{Steps: []StepOutcome{{Scale: 1.0, LossPct: []float64{9}, Over: true}}, LimitScale: 0}
	b2, _ := JSONSummary(cfg, res2, Options{}, "v2.9.0", "")
	if !strings.Contains(string(b2), `"verdict": "fail"`) {
		t.Fatalf("起始档越限应为 fail:\n%s", string(b2))
	}
}
