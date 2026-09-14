package controller

import (
	"context"
	"fmt"
	"math"
	"net"
	"strings"
	"testing"
	"time"

	"qostool/internal/config"
	"qostool/internal/protocol"
	"qostool/internal/report"
	"qostool/internal/stats"
)

// ============ 集成测试基础设施 ============
//
// 模拟接收端（sink）：纯 Go UDP 接收，复刻真实接收端的数据路径——
// 协议头解析（DecodeHeader）→ RecordRx（含时间戳），不依赖 Npcap/pcap。
// 可配置真实网络场景：丢包、乱序、双机时钟偏移/漂移、旧版发送端无时间戳。

type sinkPacket struct {
	flowIdx int
	seq     uint32
	sendTs  int64
	n       int
}

type sink struct {
	conn    *net.UDPConn
	agg     *stats.Aggregator
	skew    time.Duration // 模拟接收端固定时钟偏移（负=比发送端慢）
	latency time.Duration // 模拟单向网络延迟（真实双机：min 法会把最小时延算进偏差，hint 法不受影响）
	drift   time.Duration // 每包额外偏移增量（模拟时钟漂移）
	curSkew time.Duration // 当前累计偏移（drift>0 时变化）
	lossMod uint32        // 每 lossMod 个包丢弃 1 个（0=不丢）
	reorder bool          // 交换相邻两包处理顺序（模拟单包乱序）
	legacy  bool          // 模拟旧版发送端（无 8 字节时间戳）
	seqs    []uint32      // 每流已见计数（丢包模拟）
	pending *sinkPacket   // 乱序缓存
	total   int
	lost    int
	jumpAt  *time.Time // 时钟跳变时刻（模拟 NTP 对时）
	jump    time.Duration // 跳变量（正=时钟前跳）
}

func newSink(nFlows int) (*sink, error) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		return nil, err
	}
	return &sink{conn: conn, seqs: make([]uint32, nFlows)}, nil
}

func (s *sink) port() int { return s.conn.LocalAddr().(*net.UDPAddr).Port }

// run 阻塞读包直到 ctx 取消（取消时先冲刷乱序缓存）。
func (s *sink) run(ctx context.Context) {
	defer s.conn.Close()
	buf := make([]byte, 2048)
	for {
		select {
		case <-ctx.Done():
			if s.pending != nil {
				s.process(s.pending)
			}
			return
		default:
		}
		s.conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		n, _, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil {
				if s.pending != nil {
					s.process(s.pending)
				}
				return
			}
			continue // 读超时（无流量窗口）
		}
		flowID, seq, sendTs, ok := protocol.DecodeHeader(buf[:n])
		if !ok || int(flowID) >= len(s.seqs) {
			continue // 非本工具流量/越界流（与真实抓包路径一致地忽略）
		}
		if s.legacy {
			sendTs = 0 // 旧版发送端载荷无时间戳
		}
		p := &sinkPacket{flowIdx: int(flowID), seq: seq, sendTs: sendTs, n: len(buf[:n])}
		if s.reorder && s.pending != nil {
			// 真正交换相邻两包的处理顺序：先处理当前包（较新）再处理缓存包（较旧），
			// 聚合器看到 seq 倒序到达（2,1 / 4,3 ...）→ 乱序 + 单包空洞被补齐
			s.process(p)
			s.process(s.pending)
			s.pending = nil
		} else if s.reorder {
			s.pending = p
		} else {
			s.process(p)
		}
	}
}

// process 处理一个包：丢包模拟 → 时钟偏移/漂移 → 喂统计聚合器。
func (s *sink) process(p *sinkPacket) {
	s.seqs[p.flowIdx]++
	s.total++
	if s.lossMod > 0 && s.seqs[p.flowIdx]%s.lossMod == 0 {
		s.lost++
		return // 模拟丢包：seq 空洞由聚合器 lastSeq 逻辑检测
	}
	s.curSkew += s.drift
	recv := time.Now().Add(s.skew + s.curSkew + s.latency)
	if s.jumpAt != nil && time.Now().After(*s.jumpAt) {
		recv = recv.Add(s.jump) // 时钟跳变
	}
	s.agg.RecordRx(p.flowIdx, uint64(p.n), p.seq, recv, p.sendTs)
}

// integrationCfg 构造 nFlows 条发往 sink 端口的流（SrcPort=0 随机绑定）。
// DSCP 用 i*4（0~28，均低于 Windows 要求管理员的网络控制类 48/56），
// 非管理员环境可跑；流间隔离只依赖 DSCP+端口不同，不需要高值。
func integrationCfg(nFlows, dstPort int, ratePPS float64) *config.Config {
	cfg := &config.Config{}
	for i := 0; i < nFlows; i++ {
		cfg.Flows = append(cfg.Flows, config.Flow{
			Name: fmt.Sprintf("f%d", i), Protocol: "udp",
			SrcIP: "127.0.0.1", DstIP: "127.0.0.1",
			SrcPort: 0, DstPort: dstPort,
			DSCP: config.DSCP(i * 4), RatePPS: ratePPS, IPLen: 92,
		})
	}
	return cfg
}

// runIntegration 跑一轮完整测试：controller（ModeSend 真发包）→ sink 接收统计，
// dur 后停止（验证 Finalize 路径）。hintNs 非 nil 时在 Start 前注入时钟偏差
// （验证 hint 在创建聚合器时生效的传递链）。
func runIntegration(t *testing.T, cfg *config.Config, s *sink, dur time.Duration, hintNs *int64) *stats.Aggregator {
	t.Helper()
	ctrl := New(cfg, ModeSend)
	if hintNs != nil {
		ctrl.SetClockOffsetHint(*hintNs)
	}
	if err := ctrl.Start(""); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	s.agg = ctrl.Aggregator()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.run(ctx)
	}()
	time.Sleep(dur)
	ctrl.Stop()
	cancel()
	<-done
	return ctrl.Aggregator()
}

// ============ 测试用例 ============

// TestIntegrationFullLoop 全链路基础：真发包→解析→统计，环回无丢包、时延/抖动/百分位/历史齐全。
func TestIntegrationFullLoop(t *testing.T) {
	s, err := newSink(1)
	if err != nil {
		t.Fatal(err)
	}
	agg := runIntegration(t, integrationCfg(1, s.port(), 200), s, 1500*time.Millisecond, nil)
	snap := agg.Current(time.Now())[0]

	if snap.TxPackets == 0 || snap.RxPackets == 0 {
		t.Fatalf("应有收发流量: TX=%d RX=%d", snap.TxPackets, snap.RxPackets)
	}
	if snap.Lost > 5 {
		t.Fatalf("环回不应丢包（停止瞬间在途包容差）: Lost=%d", snap.Lost)
	}
	if math.Abs(snap.LossRate) > 0.02 {
		t.Fatalf("环回 LossRate = %.4f", snap.LossRate)
	}
	if !snap.DelayValid {
		t.Fatal("v2.8 发送端应带时间戳")
	}
	// 环回延迟≈0 且恒定：校正后时延为相对最小时延（可≈0），验证边界合理性
	if snap.DelayMinMs < 0 || snap.DelayMaxMs > 10 {
		t.Fatalf("时延边界异常: min=%.3f max=%.3f", snap.DelayMinMs, snap.DelayMaxMs)
	}
	// 单机（无时钟差）时偏差估计 = 最小时延，应 ≥0 且 <1ms
	if snap.ClockOffsetMs < 0 || snap.ClockOffsetMs >= 1 {
		t.Fatalf("单机偏差估计异常: %.4f ms（应为最小时延量级）", snap.ClockOffsetMs)
	}
	if snap.JitterMs < 0 {
		t.Fatalf("抖动为负: %f", snap.JitterMs)
	}
	if snap.DelayP95Ms < 0 || snap.DelayP99Ms < snap.DelayP95Ms {
		t.Fatalf("百分位异常: p95=%f p99=%f", snap.DelayP95Ms, snap.DelayP99Ms)
	}
	// 历史序列（Web 曲线数据源）
	h := agg.History()
	if len(h.T) == 0 || len(h.TxB[0]) == 0 || len(h.RxB[0]) == 0 || len(h.Dly[0]) == 0 {
		t.Fatalf("历史为空: %+v", h)
	}
	if h.Dly[0][len(h.Dly[0])-1] < 0 {
		t.Fatal("有效时延不应记 -1 哨兵")
	}
	// 停止后 Finalize + Totals 一致性：总丢失 = 各流 Lost 之和
	tx, rx, lost := agg.Totals()
	if tx == 0 || rx == 0 {
		t.Fatalf("Totals 异常: tx=%d rx=%d", tx, rx)
	}
	if lost != snap.Lost {
		t.Fatalf("Totals.Lost=%d 与快照 Lost=%d 不一致", lost, snap.Lost)
	}
}

// TestIntegrationClockSkewWithHint 双机时钟场景：接收端时钟慢 60s，
// Start 前注入控制通道偏差（-60s）→ 时延校正立即生效（相对最小时延），
// 偏差估计精确等于注入值（验证 hint 传递链在 controller.Start 后生效）。
func TestIntegrationClockSkewWithHint(t *testing.T) {
	s, err := newSink(1)
	if err != nil {
		t.Fatal(err)
	}
	s.skew = -60 * time.Second    // 接收端时钟比发送端慢 60s
	s.latency = 2 * time.Millisecond // 单向网络延迟 2ms
	s.drift = 100 * time.Microsecond // 每包 +0.1ms 漂移 → 校正后时延单调递增可测
	hint := int64(-60) * 1e9
	agg := runIntegration(t, integrationCfg(1, s.port(), 200), s, 1500*time.Millisecond, &hint)
	snap := agg.Current(time.Now())[0]

	if !snap.DelayValid {
		t.Fatal("应有时延数据")
	}
	// hint 生效且不被单向延迟污染：偏差估计精确为注入值
	// （min 法会把 2ms 最小时延算进偏差，无法给出精确值）
	if snap.ClockOffsetMs != -60000 {
		t.Fatalf("ClockOffsetMs = %v, want 精确 -60000（hint 应传递到 Start 后创建的聚合器且不受单向延迟影响）", snap.ClockOffsetMs)
	}
	// 校正后时延 = 单向延迟 2ms + 漂移累积（300 包×0.1ms/2≈15ms）≈ 17ms
	if snap.DelayTotAvgMs <= 1 || snap.DelayTotAvgMs > 50 {
		t.Fatalf("校正后时延异常: %.4f ms（应≈2ms 延迟 + 漂移）", snap.DelayTotAvgMs)
	}
	if snap.JitterMs < 0 {
		t.Fatalf("抖动为负: %f", snap.JitterMs)
	}
}

// TestIntegrationClockSkewNoHint 对照（无控制通道 hint）：
// min(rawD) 把单向延迟算进偏差估计（误差=最小时延），且校正后最小时延被吃掉。
// 与 WithHint 对比体现控制通道测量的精度优势。
func TestIntegrationClockSkewNoHint(t *testing.T) {
	s, err := newSink(1)
	if err != nil {
		t.Fatal(err)
	}
	s.skew = -60 * time.Second
	s.latency = 2 * time.Millisecond
	agg := runIntegration(t, integrationCfg(1, s.port(), 200), s, 1000*time.Millisecond, nil)
	snap := agg.Current(time.Now())[0]

	if !snap.DelayValid {
		t.Fatal("应有时延数据")
	}
	// min 法偏差 = -60s + 最小时延 2ms ≈ -59998（含延迟噪声，> -60000）
	if snap.ClockOffsetMs <= -60000 || snap.ClockOffsetMs > -59997 {
		t.Fatalf("ClockOffsetMs = %v, want (-60000, -59997]（min 法把单向延迟算进偏差）", snap.ClockOffsetMs)
	}
	// 校正后最小时延≈0（2ms 延迟被偏差吸收），min 不出现负值
	if snap.DelayMinMs < 0 || snap.DelayMaxMs > 10 {
		t.Fatalf("时延边界异常: min=%.3f max=%.3f", snap.DelayMinMs, snap.DelayMaxMs)
	}
}

// TestIntegrationLossAndReorder 丢包+乱序混合：每 10 包丢 1（10%）+ 相邻包乱序。
// 乱序不应产生额外丢包（宽限期补齐），丢包率应≈10%。
func TestIntegrationLossAndReorder(t *testing.T) {
	s, err := newSink(1)
	if err != nil {
		t.Fatal(err)
	}
	s.lossMod = 10
	s.reorder = true
	agg := runIntegration(t, integrationCfg(1, s.port(), 200), s, 2000*time.Millisecond, nil)
	snap := agg.Current(time.Now())[0]

	if s.total == 0 {
		t.Fatal("接收端未收到包")
	}
	// 丢包率 ≈ 10%（乱序被宽限期吸收，不产生额外丢失）
	got := snap.LossRate * 100
	if math.Abs(got-10) > 4 {
		t.Fatalf("丢包率 = %.2f%%, want ≈10%%（乱序不应产生额外丢包）", got)
	}
	if snap.RxPackets+snap.Lost != snap.TxPackets && snap.Lost > uint64(s.total/10)+10 {
		t.Fatalf("丢失数异常: Lost=%d total=%d", snap.Lost, s.total)
	}
	// v2.9.0：乱序包应被单独计数（相邻包交换产生大量乱序到达）
	if snap.Reordered < uint64(s.total)/4 {
		t.Fatalf("乱序计数异常: Reordered=%d total=%d（相邻交换应≈半数包乱序）", snap.Reordered, s.total)
	}
	// 乱序+丢包混合下时延统计仍有效
	if !snap.DelayValid || snap.DelayTotAvgMs <= 0 || snap.DelayTotAvgMs > 10 {
		t.Fatalf("时延统计异常: valid=%v avg=%.3f", snap.DelayValid, snap.DelayTotAvgMs)
	}
}

// TestIntegrationMultiFlowIsolation 8 流并发：每流独立计数、互不串扰。
func TestIntegrationMultiFlowIsolation(t *testing.T) {
	const n = 8
	s, err := newSink(n)
	if err != nil {
		t.Fatal(err)
	}
	agg := runIntegration(t, integrationCfg(n, s.port(), 100), s, 1500*time.Millisecond, nil)
	snap := agg.Current(time.Now())

	for i := 0; i < n; i++ {
		sf := snap[i]
		if sf.RxPackets == 0 {
			t.Fatalf("流 %d 无接收", i)
		}
		if sf.Lost > 5 {
			t.Fatalf("流 %d 异常丢包: %d", i, sf.Lost)
		}
		if sf.DelayValid && sf.DelayTotAvgMs > 10 {
			t.Fatalf("流 %d 时延异常: %.2f", i, sf.DelayTotAvgMs)
		}
	}
	// 流间不串扰：每流 RxPackets 应接近（同为 100pps 同时长）
	for i := 1; i < n; i++ {
		diff := int64(snap[0].RxPackets) - int64(snap[i].RxPackets)
		if diff < -20 || diff > 20 {
			t.Fatalf("流 0 与流 %d 计数差异过大: %d vs %d", i, snap[0].RxPackets, snap[i].RxPackets)
		}
	}
}

// TestIntegrationLegacySenderNoTimestamp 旧版发送端兼容：无时间戳 → 时延不可用
// 但丢包/速率统计不受影响。
func TestIntegrationLegacySenderNoTimestamp(t *testing.T) {
	s, err := newSink(1)
	if err != nil {
		t.Fatal(err)
	}
	s.legacy = true
	s.lossMod = 20 // 5% 丢包，验证丢包统计不依赖时间戳
	agg := runIntegration(t, integrationCfg(1, s.port(), 200), s, 1500*time.Millisecond, nil)
	snap := agg.Current(time.Now())[0]

	if snap.DelayValid {
		t.Fatal("旧版发送端不应有时延数据")
	}
	if snap.DelayAvgMs != 0 || snap.JitterMs != 0 || snap.DelayP95Ms != 0 {
		t.Fatalf("旧版时延字段应全 0: %+v", snap)
	}
	if snap.RxPackets == 0 {
		t.Fatal("丢包统计不应受时间戳影响")
	}
	if math.Abs(snap.LossRate*100-5) > 3 {
		t.Fatalf("丢包率 = %.2f%%, want ≈5%%", snap.LossRate*100)
	}
}

// TestIntegrationVerdictsAndJSON 阈值判定 + 汇总 + JSON 全链路：
// 流0 阈值宽松（PASS）、流1 阈值严格（FAIL），验证 Verdicts/Summary/JSONSummary 一致。
func TestIntegrationVerdictsAndJSON(t *testing.T) {
	s, err := newSink(2)
	if err != nil {
		t.Fatal(err)
	}
	s.lossMod = 5 // 20% 丢包（两流相同）
	cfg := integrationCfg(2, s.port(), 150)
	cfg.Flows[0].MaxLossRatePct = 50 // 20% < 50% → PASS
	cfg.Flows[1].MaxLossRatePct = 1  // 20% > 1% → FAIL
	agg := runIntegration(t, cfg, s, 2000*time.Millisecond, nil)
	snap := agg.Current(time.Now())

	vs := report.Verdicts(cfg, snap, true)
	if !vs[0].Checked || !vs[0].Pass {
		t.Fatalf("流0 应 PASS: %+v", vs[0])
	}
	if !vs[1].Checked || vs[1].Pass || vs[1].FailMsg == "" {
		t.Fatalf("流1 应 FAIL 且含原因: %+v", vs[1])
	}

	// 文本汇总
	sum := report.Summary(cfg, snap, 0, 0, 0, true)
	for _, want := range []string{"汇总报告", "f0", "f1", "丢包"} {
		if !strings.Contains(sum, want) {
			t.Fatalf("汇总缺少 %q:\n%s", want, sum)
		}
	}
	if !strings.Contains(vs[1].FailMsg, "丢包率") {
		t.Fatalf("FAIL 原因应含丢包率: %s", vs[1].FailMsg)
	}

	// JSON 汇总
	b, err := report.JSONSummary(cfg, snap, vs, report.ReportMeta{
		Started: time.Now().Add(-time.Minute), Stopped: time.Now(), Mode: "bidir", Version: "v2.8.1",
	}, true, "logs/x.html")
	if err != nil {
		t.Fatal(err)
	}
	js := string(b)
	for _, want := range []string{`"verdict": "fail"`, `"fail_count": 1`, `"checked": true`, `"pass": false`} {
		if !strings.Contains(js, want) {
			t.Fatalf("JSON 缺少 %q:\n%s", want, js)
		}
	}
}

// TestIntegrationClockDrift 时钟漂移：接收端时钟以每包 0.1ms 速率漂移
// （300 包 ≈ 30ms），min 基准校正后残余漂移使部分包校正为负 → 钳 0，
// 统计不 panic、抖动仍有效。
func TestIntegrationClockDrift(t *testing.T) {
	s, err := newSink(1)
	if err != nil {
		t.Fatal(err)
	}
	s.drift = 100 * time.Microsecond // 每包 +0.1ms
	agg := runIntegration(t, integrationCfg(1, s.port(), 300), s, 1500*time.Millisecond, nil)
	snap := agg.Current(time.Now())[0]

	if !snap.DelayValid {
		t.Fatal("漂移场景应有时延数据")
	}
	if snap.JitterMs < 0 {
		t.Fatalf("漂移下抖动为负: %f", snap.JitterMs)
	}
	if snap.DelayMinMs < 0 || snap.DelayMaxMs < snap.DelayMinMs {
		t.Fatalf("漂移下 min/max 异常: %+v", snap)
	}
	if snap.RxPackets == 0 || snap.Lost > 5 {
		t.Fatalf("漂移不应影响收发统计: RX=%d Lost=%d", snap.RxPackets, snap.Lost)
	}
}

// TestIntegrationClockJump 时钟跳变：接收端时钟中途突跳 +10s（模拟 NTP 对时），
// min 基准随跳变更新，统计不 panic 且后续时延仍校正合理。
func TestIntegrationClockJump(t *testing.T) {
	s, err := newSink(1)
	if err != nil {
		t.Fatal(err)
	}
	jumpAfter := time.Now().Add(700 * time.Millisecond)
	s.jumpAt = &jumpAfter
	s.jump = 10 * time.Second
	agg := runIntegration(t, integrationCfg(1, s.port(), 200), s, 1500*time.Millisecond, nil)
	snap := agg.Current(time.Now())[0]

	if !snap.DelayValid {
		t.Fatal("跳变场景应有时延数据")
	}
	if snap.ClockOffsetMs > 10*1000 { // 跳变后 min 基准更新，偏差估计不应残留 +10s
		t.Fatalf("跳变后偏差估计异常: %.1f ms", snap.ClockOffsetMs)
	}
	if snap.JitterMs < 0 {
		t.Fatalf("跳变下抖动为负: %f", snap.JitterMs)
	}
}
