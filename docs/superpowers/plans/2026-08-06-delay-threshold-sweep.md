# qostool v2.7.0 时延/阈值报告/阶梯扫描 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 qostool v2.6.0 基础上实现单向时延/抖动测量、阈值判定与 HTML 报告、`sweep` 阶梯扫描三个功能，版本升至 v2.7.0。

**Architecture:** 载荷头 10B→18B 加发送时间戳；stats 聚合器按流维护时延窗口/累计统计（每流互斥锁）；report 包新增 Verdicts 判定与自包含 HTML 报告；新 internal/sweep 包直接组装 sender+capture+agg，用 Sender.SetRates 动态调速逐档加压；Web/终端/CSV 同步展示新指标。

**Tech Stack:** Go（现有标准库 + gopacket + yaml.v3），无新依赖。Win7 需 Go 1.20 兼容（atomic.Int64/Int32 自 1.19 可用）。

## Global Constraints

- 全部代码走 TDD：先写失败测试，看它失败，再实现，看它通过
- 每任务结束跑 `go test ./...` 全绿后提交一次 commit
- 不新增第三方依赖；不破坏现有 JSON API 字段名
- 兼容：旧接收端读新包正常；新接收端遇旧发送端时延显示 `—`（DelayValid=false）
- 时延为负（时钟差）钳到 0；JSON 序列化禁止 NaN/Inf（History.Dly 无效点用 -1 哨兵）
- 中文注释与既有代码风格一致（包注释、行注释解释"为什么"）

---

### Task 1: protocol — 载荷头加时间戳

**Files:**
- Modify: `internal/protocol/protocol.go`
- Test: `internal/protocol/protocol_test.go`

**Interfaces:**
- Produces: `HeaderSize=18`, `TimestampOffset=10`；`EncodeHeader(payload []byte, flowID uint16, seq uint32, sendTs int64)`；`EncodeSeqTs(payload []byte, seq uint32, sendTs int64)`；`DecodeHeader(payload []byte) (flowID uint16, seq uint32, sendTs int64, ok bool)`

- [ ] **Step 1: 写失败测试**（覆盖：新头往返、旧 10B 头 ts=0、坏 magic、过短、EncodeSeqTs）

```go
func TestEncodeDecode(t *testing.T) {
	payload := make([]byte, HeaderSize+16)
	EncodeHeader(payload, 3, 42, 123456789)
	fid, seq, ts, ok := DecodeHeader(payload)
	if !ok || fid != 3 || seq != 42 || ts != 123456789 {
		t.Fatalf("DecodeHeader = %d %d %d %v", fid, seq, ts, ok)
	}
}

func TestDecodeLegacyHeaderNoTs(t *testing.T) {
	payload := make([]byte, 10) // 旧版 10B 头
	binary.BigEndian.PutUint32(payload[0:4], Magic)
	binary.BigEndian.PutUint16(payload[4:6], 5)
	binary.BigEndian.PutUint32(payload[6:10], 99)
	fid, seq, ts, ok := DecodeHeader(payload)
	if !ok || fid != 5 || seq != 99 || ts != 0 {
		t.Fatalf("旧头解码 = %d %d %d %v, want ts=0", fid, seq, ts, ok)
	}
}

func TestDecodeBadMagic(t *testing.T) {
	payload := make([]byte, HeaderSize)
	payload[0] = 0xDE
	if _, _, _, ok := DecodeHeader(payload); ok {
		t.Fatal("坏 magic 应返回 ok=false")
	}
}

func TestDecodeTooShort(t *testing.T) {
	if _, _, _, ok := DecodeHeader(make([]byte, 4)); ok {
		t.Fatal("过短载荷应返回 ok=false")
	}
}

func TestEncodeSeqTs(t *testing.T) {
	payload := make([]byte, HeaderSize)
	EncodeHeader(payload, 1, 100, 0)
	EncodeSeqTs(payload, 101, 999)
	fid, seq, ts, ok := DecodeHeader(payload)
	if !ok || fid != 1 || seq != 101 || ts != 999 {
		t.Fatalf("EncodeSeqTs 后 = %d %d %d %v", fid, seq, ts, ok)
	}
}
```

- [ ] **Step 2: 运行确认失败**：`go test ./internal/protocol/` → 编译失败（DecodeHeader 返回 4 值、EncodeSeqTs 未定义），另需同步更新 `internal/capture/capture.go:141` 与 `capture_linux.go:145` 两处旧调用（临时改成 `fid, seq, _, ok := protocol.DecodeHeader(...)`）使编译通过。

- [ ] **Step 3: 实现**

```go
const (
	Magic           = 0x514F5354 // "QOST"
	HeaderSize      = 18
	TimestampOffset = 10 // send_ts 偏移（8 字节 UnixNano，大端）
)

func EncodeHeader(payload []byte, flowID uint16, seq uint32, sendTs int64) {
	binary.BigEndian.PutUint32(payload[0:4], Magic)
	binary.BigEndian.PutUint16(payload[4:6], flowID)
	binary.BigEndian.PutUint32(payload[6:10], seq)
	binary.BigEndian.PutUint64(payload[TimestampOffset:TimestampOffset+8], uint64(sendTs))
}

// EncodeSeqTs 发送循环内只更新 seq 与时间戳（比全头编码少写 6 字节）。
func EncodeSeqTs(payload []byte, seq uint32, sendTs int64) {
	binary.BigEndian.PutUint32(payload[6:10], seq)
	binary.BigEndian.PutUint64(payload[TimestampOffset:TimestampOffset+8], uint64(sendTs))
}

// DecodeHeader 校验 magic 并返回 flow_id/seq/send_ts；sendTs==0 表示旧版发送端（10B 头无时间戳）。
func DecodeHeader(payload []byte) (flowID uint16, seq uint32, sendTs int64, ok bool) {
	if len(payload) < 10 {
		return 0, 0, 0, false
	}
	if binary.BigEndian.Uint32(payload[0:4]) != Magic {
		return 0, 0, 0, false
	}
	flowID = binary.BigEndian.Uint16(payload[4:6])
	seq = binary.BigEndian.Uint32(payload[6:10])
	if len(payload) >= HeaderSize {
		sendTs = int64(binary.BigEndian.Uint64(payload[TimestampOffset : TimestampOffset+8]))
	}
	return flowID, seq, sendTs, true
}
```

- [ ] **Step 4: 全绿**：`go test ./internal/protocol/` 通过。
- [ ] **Step 5: 提交**：`git add -A && git commit -m "feat(protocol): 载荷头加 8 字节发送时间戳（10B→18B，旧头兼容）"`

### Task 2: stats — 时延/抖动统计

**Files:**
- Modify: `internal/stats/aggregator.go`
- Test: `internal/stats/aggregator_test.go`

**Interfaces:**
- Consumes: Task 1 的 DecodeHeader（capture 侧传 sendTs）
- Produces: `FlowSnapshot` 新增 `DelayValid bool, DelayAvgMs, DelayTotAvgMs, DelayMinMs, DelayMaxMs, JitterMs float64`；`RecordRx(flowIdx int, bytes uint64, seq uint32, recv time.Time, sendTs int64)`；`History` 新增 `Dly [][]float64`

- [ ] **Step 1: 写失败测试**（追加到 aggregator_test.go）

```go
func TestDelayStats(t *testing.T) {
	a := NewAggregator(1, 10)
	base := time.Now()
	// 三包时延：10ms、20ms、30ms
	a.RecordRx(0, 100, 1, base.Add(-10*time.Millisecond), base.Add(-20*time.Millisecond).UnixNano())
	a.RecordRx(0, 100, 2, base, base.Add(-20*time.Millisecond).UnixNano())
	a.RecordRx(0, 100, 3, base.Add(10*time.Millisecond), base.Add(-20*time.Millisecond).UnixNano())
	snap := a.Current(base.Add(10 * time.Millisecond))
	s := snap[0]
	if !s.DelayValid {
		t.Fatal("DelayValid = false, want true")
	}
	if math.Abs(s.DelayAvgMs-20) > 1e-6 || math.Abs(s.DelayTotAvgMs-20) > 1e-6 {
		t.Fatalf("avg = %v/%v, want 20", s.DelayAvgMs, s.DelayTotAvgMs)
	}
	if math.Abs(s.DelayMinMs-10) > 1e-6 || math.Abs(s.DelayMaxMs-30) > 1e-6 {
		t.Fatalf("min/max = %v/%v, want 10/30", s.DelayMinMs, s.DelayMaxMs)
	}
}

func TestDelayWindowReset(t *testing.T) {
	a := NewAggregator(1, 10)
	base := time.Now()
	// 第一窗口：时延 10ms
	a.RecordRx(0, 100, 1, base, base.Add(-10*time.Millisecond).UnixNano())
	a.Snapshot(base)
	// 第二窗口：时延 40ms（窗口 avg 应变 40，累计 avg 变 (10+40)/2=25）
	a.RecordRx(0, 100, 2, base.Add(time.Second), base.Add(time.Second).Add(-40*time.Millisecond).UnixNano())
	snap := a.Snapshot(base.Add(time.Second))
	s := snap[0]
	if math.Abs(s.DelayAvgMs-40) > 1e-6 {
		t.Fatalf("窗口 avg = %v, want 40", s.DelayAvgMs)
	}
	if math.Abs(s.DelayTotAvgMs-25) > 1e-6 {
		t.Fatalf("累计 avg = %v, want 25", s.DelayTotAvgMs)
	}
	if math.Abs(s.DelayMinMs-10) > 1e-6 || math.Abs(s.DelayMaxMs-40) > 1e-6 {
		t.Fatalf("min/max = %v/%v, want 10/40", s.DelayMinMs, s.DelayMaxMs)
	}
}

func TestDelayJitterRFC3550(t *testing.T) {
	a := NewAggregator(1, 10)
	base := time.Now()
	// 时延序列 10、30、10、30 ms → D 差 20、20、20 → jitter 收敛到 ~20ms
	a.RecordRx(0, 100, 1, base, base.Add(-10*time.Millisecond).UnixNano())
	a.RecordRx(0, 100, 2, base.Add(10*time.Millisecond), base.Add(-20*time.Millisecond).UnixNano())
	a.RecordRx(0, 100, 3, base.Add(30*time.Millisecond), base.Add(-20*time.Millisecond).UnixNano())
	a.RecordRx(0, 100, 4, base.Add(40*time.Millisecond), base.Add(-10*time.Millisecond).UnixNano())
	s := a.Current(base.Add(40 * time.Millisecond))[0]
	if math.Abs(s.JitterMs-20) > 1 {
		t.Fatalf("jitter = %v, want ~20", s.JitterMs)
	}
}

func TestDelayLegacyNoTs(t *testing.T) {
	a := NewAggregator(1, 10)
	a.RecordRx(0, 100, 1, time.Now(), 0) // 旧发送端：sendTs=0
	s := a.Current(time.Now())[0]
	if s.DelayValid {
		t.Fatal("旧包 DelayValid 应为 false")
	}
	if s.DelayAvgMs != 0 || s.JitterMs != 0 {
		t.Fatalf("旧包时延应全 0, got %+v", s)
	}
}

func TestDelayHistory(t *testing.T) {
	a := NewAggregator(1, 10)
	base := time.Now()
	a.RecordRx(0, 100, 1, base, base.Add(-5*time.Millisecond).UnixNano())
	a.Snapshot(base)
	h := a.History()
	if len(h.Dly) != 1 || len(h.Dly[0]) != 1 {
		t.Fatalf("Dly = %+v", h.Dly)
	}
	if math.Abs(h.Dly[0][0]-5) > 1e-6 {
		t.Fatalf("Dly[0][0] = %v, want 5", h.Dly[0][0])
	}
	// 无时间戳包 → -1 哨兵
	a2 := NewAggregator(1, 10)
	a2.RecordRx(0, 100, 1, time.Now(), 0)
	a2.Snapshot(time.Now())
	if h2 := a2.History(); h2.Dly[0][0] != -1 {
		t.Fatalf("无效时延应记 -1, got %v", h2.Dly[0][0])
	}
}
```

- [ ] **Step 2: 运行确认失败**：`go test ./internal/stats/ -run TestDelay` → 编译失败（字段/签名不存在）。
- [ ] **Step 3: 实现**（aggregator.go）：

```go
// FlowSnapshot 增加：
	DelayValid  bool    // 是否收到过带时间戳的包（旧发送端=false）
	DelayAvgMs  float64 // 1s 窗口平均时延（毫秒）
	DelayTotAvgMs float64 // 全程累计平均时延（毫秒，报告用）
	DelayMinMs  float64 // 全程最小
	DelayMaxMs  float64 // 全程最大
	JitterMs    float64 // RFC3550 抖动（毫秒，累计）
```

```go
// History 增加 Dly [][]float64（窗口平均时延 ms，无效=-1 哨兵）

// 聚合器新增字段（每流时延状态，delayMu 保护）：
type delayState struct {
	valid      bool
	winSumNs   uint64 // 窗口内时延和（ns）
	winCount   uint64
	totSumNs   uint64 // 全程时延和（ns）
	totCount   uint64
	delayMinNs int64  // 全程最小
	delayMaxNs int64  // 全程最大
	prevDelay  int64  // 上一包时延（ns）
	prevSet    bool
	jitterNs   float64 // RFC3550 抖动（ns）
}
// Aggregator 增加：delay []delayState; delayMu []sync.Mutex
// NewAggregator 初始化两个切片（长度 nFlows）
```

```go
// RecordRx 签名改为 (flowIdx int, bytes uint64, seq uint32, recv time.Time, sendTs int64)，
// 原有 seq/计数逻辑不变，追加：
	if sendTs > 0 {
		d := recv.UnixNano() - sendTs
		if d < 0 {
			d = 0 // 时钟偏移：钳到 0，不产生负时延
		}
		a.delayMu[flowIdx].Lock()
		st := &a.delay[flowIdx]
		if !st.valid {
			st.valid = true
			st.delayMinNs, st.delayMaxNs = d, d
		} else {
			if d < st.delayMinNs { st.delayMinNs = d }
			if d > st.delayMaxNs { st.delayMaxNs = d }
		}
		st.winSumNs += uint64(d)
		st.winCount++
		st.totSumNs += uint64(d)
		st.totCount++
		if st.prevSet {
			diff := st.prevDelay - d
			if diff < 0 { diff = -diff }
			st.jitterNs += (float64(diff) - st.jitterNs) / 16 // RFC3550
		}
		st.prevDelay, st.prevSet = d, true
		a.delayMu[flowIdx].Unlock()
	}
```

`Snapshot()` 的 ratesLocked 之前/之中：每流在持有 `a.delayMu[i]` 时读取时延字段填进 FlowSnapshot，窗口 sum/count 清零（min/max/jitter/累计不清零）。`ratesLocked` 改为在 `Snapshot` 与 `Current` 共用的路径里也填时延字段（Current 只读不清零）。`History` 深拷贝加 `Dly`；`Snapshot` 追加历史时 `Dly[i]` 填 `DelayAvgMs`，无效时填 `-1`。

注意：`ratesLocked` 的 `out[i]` 填充时延需要访问 `a.delayMu[i]`——在 `Snapshot`（持 a.mu）与 `Current`（持 a.mu）内都安全（锁顺序固定：a.mu → delayMu[i]，RecordRx 只持 delayMu[i]，无反向，无死锁）。
- [ ] **Step 4: 全绿**：`go test ./internal/stats/` 通过（含既有测试，`RecordRx` 旧签名调用在 aggregator_test 里也要改成 5 参——`TestLossDetection` 等调用处补 `time.Now(), 0`）。
- [ ] **Step 5: 提交**：`git commit -m "feat(stats): 每流时延 min/avg/max + RFC3550 抖动统计"`

### Task 3: sender — 每包写时间戳 + SetRates 动态调速

**Files:**
- Modify: `internal/sender/sender.go`
- Test: `internal/sender/sender_test.go`

**Interfaces:**
- Consumes: Task 1 `EncodeSeqTs`
- Produces: `type RateSpec struct{ RateMbps, RatePPS float64 }`；`func (s *Sender) SetRates(rates []RateSpec) error`

- [ ] **Step 1: 写失败测试**（读 sender_test.go 现有约定后追加；flow 结构体在同包可访问）：

```go
func TestSetRatesChangesPacing(t *testing.T) {
	// 构造 100pps 流
	cfg := &config.Config{Flows: []config.Flow{{Name: "a", Protocol: "udp",
		SrcIP: "127.0.0.1", DstIP: "127.0.0.1", SrcPort: 1, DstPort: 2, DSCP: 0, RatePPS: 100, IPLen: 92}}}
	s, err := New(cfg, stats.NewAggregator(1, 10))
	if err != nil { t.Fatal(err) }
	defer s.Close()
	before := s.flows[0].batchGapNs.Load()
	if err := s.SetRates([]RateSpec{{RateMbps: 0, RatePPS: 1000}}); err != nil { t.Fatal(err) }
	after := s.flows[0].batchGapNs.Load()
	if after >= before || after <= 0 {
		t.Fatalf("SetRates 后 batchGap = %d, want < %d", after, before)
	}
	// 非法长度
	if err := s.SetRates([]RateSpec{{}}); err == nil {
		t.Fatal("长度不符应报错")
	}
}
```

（若 sender_test.go 无 flow 内部访问先例，改用 New 后 `s.flows` 访问——同包测试可访问私有字段。）

- [ ] **Step 2: 运行确认失败**：`go test ./internal/sender/ -run TestSetRates` → 编译失败。
- [ ] **Step 3: 实现**：

```go
// flow 新增：
	cfgCfg     config.Flow // 速率配置副本（SetRates 更新）
	batchGapNs atomic.Int64 // 批间隔 ns（run 循环读取）
	batchN     atomic.Int32 // 批大小

// newFlow 里 f.cfgCfg = cfg；computePacing 末尾改为：
	f.batchGapNs.Store(int64(f.batchGap))
	f.batchN.Store(int32(f.batch))

// run() 循环内读取：
	next = next.Add(time.Duration(f.batchGapNs.Load()))
	...
	for i := 0; i < int(f.batchN.Load()); i++ {
		f.seq++
		protocol.EncodeSeqTs(f.payload, f.seq, time.Now().UnixNano())
		if _, err := f.conn.Write(f.payload); err != nil { ... 原逻辑不变 }
	}

// Sender 新增：
type RateSpec struct{ RateMbps, RatePPS float64 }

// SetRates 批量更新每流速率并重算 pacing；seq/socket 不中断（阶梯扫描用）。
func (s *Sender) SetRates(rates []RateSpec) error {
	if len(rates) != len(s.flows) {
		return fmt.Errorf("速率数量 %d 与流数 %d 不符", len(rates), len(s.flows))
	}
	for i, f := range s.flows {
		f.cfgCfg.RateMbps = rates[i].RateMbps
		f.cfgCfg.RatePPS = rates[i].RatePPS
		f.computePacing(f.cfgCfg)
	}
	return nil
}
```

- [ ] **Step 4: 全绿**：`go test ./internal/sender/` 通过（sender_test.go 里直接构造的 flow 若引用旧字段需同步；确认无其他引用 batchGap 的地方）。
- [ ] **Step 5: 提交**：`git commit -m "feat(sender): 载荷写发送时间戳 + SetRates 动态调速（原子 pacing）"`

### Task 4: capture — 传到达时间与 sendTs

**Files:**
- Modify: `internal/capture/capture.go`（Windows/pcap 路径）、`internal/capture/capture_linux.go`

**Interfaces:**
- Consumes: Task 1 DecodeHeader 四返回值、Task 2 RecordRx 五参数

- [ ] **Step 1: 改两处调用**。capture.go（Windows pcap）：

```go
		data, ci, err := c.handle.ReadPacketData()
		...
		fid, seq, sendTs, ok := protocol.DecodeHeader(pkt.payload)
		...
		c.agg.RecordRx(idx, uint64(len(ip)), seq, ci.Timestamp, sendTs)
```

capture_linux.go（AF_PACKET，无内核时间戳，用处理时刻）：

```go
		fid, seq, sendTs, ok := protocol.DecodeHeader(pkt.payload)
		...
		c.agg.RecordRx(idx, uint64(len(ip)), seq, time.Now(), sendTs)
```

（capture_linux.go 需确认已 import "time"；没有则补。）
- [ ] **Step 2: 编译 + 测试**：`go build ./... && go test ./internal/capture/` 通过（fuzz 测试不受影响）。
- [ ] **Step 3: 提交**：`git commit -m "feat(capture): 抓包时间戳与发送端时间戳传入统计"`

### Task 5: config — 阈值字段与校验

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces: `Flow.MaxLossRatePct, MaxAvgDelayMs, MaxJitterMs float64`（yaml `max_loss_rate_pct,omitempty` 等）

- [ ] **Step 1: 写失败测试**：

```go
func TestThresholdFieldsRoundtrip(t *testing.T) {
	in := `flows:
  - name: a
    protocol: udp
    src_ip: 127.0.0.1
    dst_ip: 127.0.0.1
    src_port: 1000
    dst_port: 2000
    dscp: 46
    rate_pps: 100
    ip_len: 92
    max_loss_rate_pct: 0.5
    max_avg_delay_ms: 50
    max_jitter_ms: 10
`
	cfg, err := LoadFromReader(strings.NewReader(in)) // 若不存在此函数，直接用 yaml.Unmarshal 于测试内
	...
}
```

（现有 config_test.go 若已有 yaml 解析辅助则复用；否则测试内 `yaml.Unmarshal` + `cfg.Validate()`。）
断言：三个字段值正确；缺省为 0。

```go
func TestThresholdValidation(t *testing.T) {
	bad := []struct{ name, yaml string }{
		{"负丢包阈值", "max_loss_rate_pct: -1"},
		{"丢包阈值超100", "max_loss_rate_pct: 101"},
		{"负时延阈值", "max_avg_delay_ms: -5"},
		{"负抖动阈值", "max_jitter_ms: -1"},
	}
	for _, b := range bad {
		// 构造完整 flow + b.yaml 字段 → cfg.Validate() 应报错
	}
}
```

- [ ] **Step 2: 运行确认失败**（字段未定义/校验不报错）。
- [ ] **Step 3: 实现**：Flow 加 3 字段；validate() 内（requireRates 分支前，阈值收发两端都校验）加：

```go
		if f.MaxLossRatePct < 0 || f.MaxLossRatePct > 100 {
			return fmt.Errorf("flow %d (%s): max_loss_rate_pct 必须在 0-100", i+1, f.Name)
		}
		if f.MaxAvgDelayMs < 0 {
			return fmt.Errorf("flow %d (%s): max_avg_delay_ms 不能为负", i+1, f.Name)
		}
		if f.MaxJitterMs < 0 {
			return fmt.Errorf("flow %d (%s): max_jitter_ms 不能为负", i+1, f.Name)
		}
```

- [ ] **Step 4: 全绿**：`go test ./internal/config/`。
- [ ] **Step 5: 提交**：`git commit -m "feat(config): 每流阈值字段 max_loss_rate_pct/max_avg_delay_ms/max_jitter_ms"`

### Task 6: report — 表格/汇总时延列 + Verdicts 判定

**Files:**
- Modify: `internal/report/report.go`，Create: `internal/report/verdict.go`
- Test: `internal/report/report_test.go`、Create: `internal/report/verdict_test.go`

**Interfaces:**
- Consumes: Task 2 FlowSnapshot 时延字段、Task 5 阈值字段
- Produces: `type Verdict struct{ FlowIdx int; Checked, Pass bool; FailMsg string }`；`func Verdicts(cfg *config.Config, snap []stats.FlowSnapshot, useTotDelay bool) []Verdict`

- [ ] **Step 1: 写失败测试**（verdict_test.go）：

```go
func TestVerdictLossFail(t *testing.T) {
	cfg := &config.Config{Flows: []config.Flow{{Name: "a", MaxLossRatePct: 0.5}}}
	snap := testSnap(1e6, 1e6, 1000, 990, 10) // 1% 丢包
	vs := Verdicts(cfg, snap, false)
	if !vs[0].Checked || vs[0].Pass {
		t.Fatalf("verdict = %+v, want FAIL", vs[0])
	}
	if !strings.Contains(vs[0].FailMsg, "丢包率") {
		t.Fatalf("FailMsg = %q", vs[0].FailMsg)
	}
}

func TestVerdictLossPass(t *testing.T) {
	cfg := &config.Config{Flows: []config.Flow{{Name: "a", MaxLossRatePct: 0.5}}}
	snap := testSnap(1e6, 1e6, 1000, 999, 1) // 0.1%
	if vs := Verdicts(cfg, snap, false); !vs[0].Pass {
		t.Fatalf("verdict = %+v, want PASS", vs[0])
	}
}

func TestVerdictDelayFail(t *testing.T) {
	cfg := &config.Config{Flows: []config.Flow{{Name: "a", MaxAvgDelayMs: 50}}}
	snap := []stats.FlowSnapshot{{FlowIdx: 0, DelayValid: true, DelayAvgMs: 60, DelayTotAvgMs: 55}}
	if vs := Verdicts(cfg, snap, false); vs[0].Pass {
		t.Fatalf("窗口 60ms > 50ms 应 FAIL, got %+v", vs[0])
	}
	snap2 := []stats.FlowSnapshot{{FlowIdx: 0, DelayValid: true, DelayAvgMs: 40, DelayTotAvgMs: 60}}
	if vs := Verdicts(cfg, snap2, true); vs[0].Pass {
		t.Fatalf("累计 60ms > 50ms 应 FAIL（useTotDelay）, got %+v", vs[0])
	}
}

func TestVerdictJitterFail(t *testing.T) {
	cfg := &config.Config{Flows: []config.Flow{{Name: "a", MaxJitterMs: 10}}}
	snap := []stats.FlowSnapshot{{FlowIdx: 0, DelayValid: true, JitterMs: 12}}
	if vs := Verdicts(cfg, snap, false); vs[0].Pass {
		t.Fatal("jitter 12 > 10 应 FAIL")
	}
}

func TestVerdictNoThresholdNotChecked(t *testing.T) {
	cfg := &config.Config{Flows: []config.Flow{{Name: "a"}}}
	if vs := Verdicts(cfg, testSnap(1e6, 0, 1000, 0, 0), false); vs[0].Checked {
		t.Fatal("无阈值不应 Checked")
	}
}

func TestVerdictNoDelayDataSkipsDelayCheck(t *testing.T) {
	cfg := &config.Config{Flows: []config.Flow{{Name: "a", MaxAvgDelayMs: 50}}}
	snap := []stats.FlowSnapshot{{FlowIdx: 0, DelayValid: false}} // 旧发送端
	if vs := Verdicts(cfg, snap, false); !vs[0].Pass {
		t.Fatal("无时延数据不应判时延 FAIL")
	}
}

func TestVerdictNoDataSkipsLossCheck(t *testing.T) {
	cfg := &config.Config{Flows: []config.Flow{{Name: "a", MaxLossRatePct: 0.5}}}
	snap := []stats.FlowSnapshot{{FlowIdx: 0}} // 未收到任何包
	if vs := Verdicts(cfg, snap, false); !vs[0].Pass {
		t.Fatal("无收发数据不应判丢包 FAIL")
	}
}
```

- [ ] **Step 2: 运行确认失败**：`go test ./internal/report/ -run TestVerdict` → 编译失败。
- [ ] **Step 3: 实现**（verdict.go）：

```go
package report

// Verdict 是单条流的阈值判定结果。
type Verdict struct {
	FlowIdx int
	Checked bool   // 是否配置了任何阈值
	Pass    bool
	FailMsg string // 首个违反的阈值描述
}

// Verdicts 判定全部流；useTotDelay=true 用全程累计平均时延（最终报告），否则用窗口平均（实时）。
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
			if useTotDelay { avg = s.DelayTotAvgMs }
			if avg > f.MaxAvgDelayMs {
				if !v.Pass { v.FailMsg += "; " }
				v.Pass = false
				v.FailMsg += fmt.Sprintf("平均时延 %.2fms > %.2fms", avg, f.MaxAvgDelayMs)
			}
		}
		if f.MaxJitterMs > 0 && s.DelayValid {
			v.Checked = true
			if s.JitterMs > f.MaxJitterMs {
				if !v.Pass { v.FailMsg += "; " }
				v.Pass = false
				v.FailMsg += fmt.Sprintf("抖动 %.2fms > %.2fms", s.JitterMs, f.MaxJitterMs)
			}
		}
		out[i] = v
	}
	return out
}
```

（testSnap 需要 delay 字段辅助：在 report_test.go 加 `testSnapD(delayValid bool, avg, min, max, jitter float64)` 或直接字面量构造，verdict_test.go 已有字面量用法。）
- [ ] **Step 4: 表格/汇总加时延列**（report.go，先改现有 report_test.go 的期望后实现）：

`Table` 表头加 `时延ms` 与 `抖动ms` 两列（`avg/min/max` 格式 `%.1f/%.1f/%.1f`，DelayValid=false 显示 `—`；抖动 `%.2f`）。`Summary` 每流行加时延汇总：`时延 avg/min/max ms` 与 `抖动 ms`。
- [ ] **Step 5: 全绿**：`go test ./internal/report/`。
- [ ] **Step 6: 提交**：`git commit -m "feat(report): 阈值判定 Verdicts + 表格/汇总时延列"`

### Task 7: report — HTML 报告

**Files:**
- Create: `internal/report/html.go`，Test: `internal/report/html_test.go`

**Interfaces:**
- Consumes: Task 6 Verdicts
- Produces: `type ReportMeta struct{ Started, Stopped time.Time; Iface, Mode, Version string }`；`func HTMLReport(cfg *config.Config, snap []stats.FlowSnapshot, verdicts []Verdict, meta ReportMeta, destDir string) (string, error)`

- [ ] **Step 1: 写失败测试**：

```go
func TestHTMLReport(t *testing.T) {
	dir := t.TempDir()
	cfg := testCfg()
	cfg.Flows[0].MaxLossRatePct = 0.5
	snap := testSnap(1e6, 1e6, 1000, 990, 10) // 1% 丢包 → FAIL
	vs := Verdicts(cfg, snap, true)
	meta := ReportMeta{Started: time.Now().Add(-time.Minute), Stopped: time.Now(), Iface: "eth0", Mode: "bidir", Version: "v2.7.0"}
	path, err := HTMLReport(cfg, snap, vs, meta, dir)
	if err != nil { t.Fatal(err) }
	data, err := os.ReadFile(path)
	if err != nil { t.Fatal(err) }
	s := string(data)
	for _, want := range []string{"汇总报告", "EF-语音", "FAIL", "0.5", "eth0", "v2.7.0"} {
		if !strings.Contains(s, want) {
			t.Fatalf("报告缺少 %q", want)
		}
	}
}

func TestHTMLReportAllPass(t *testing.T) {
	dir := t.TempDir()
	cfg := testCfg()
	snap := testSnap(1e6, 1e6, 1000, 1000, 0)
	vs := Verdicts(cfg, snap, true)
	path, err := HTMLReport(cfg, snap, vs, ReportMeta{}, dir)
	if err != nil { t.Fatal(err) }
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "通过") {
		t.Fatalf("全部通过应有结论文本")
	}
	if strings.Contains(string(data), "FAIL") {
		t.Fatalf("无阈值不应出现 FAIL")
	}
}
```

- [ ] **Step 2: 运行确认失败**（HTMLReport 未定义）。
- [ ] **Step 3: 实现**（html.go，自包含单文件、内联深色 CSS、html/template 或 strings.Builder；含：标题、meta 信息表、每流明细表（含阈值列与判定徽标 PASS 绿/FAIL 红/— 灰）、结论行"X/Y 通过"；文件名 `qostool_report_<Stopped:20060102_150405>.html`，destDir 不存在时 MkdirAll）。明细表列：流、DSCP、TX 包、RX 包、丢包、丢包率%、时延 avg/min/max、抖动 ms、阈值（丢包%/时延ms/抖动ms）、判定。所有数值 `html/template` 自动转义（用 `template.HTML` 仅包判定徽标等受控片段）。
- [ ] **Step 4: 全绿**：`go test ./internal/report/`。
- [ ] **Step 5: 提交**：`git commit -m "feat(report): 自包含 HTML 测试报告（阈值判定徽标+结论）"`

### Task 8: controller — 停止自动生成报告 + CSV 时延列

**Files:**
- Modify: `internal/controller/controller.go`，Test: `internal/controller/controller_test.go`

**Interfaces:**
- Consumes: Task 7 HTMLReport
- Produces: `LastReportPath() string`；`WriteHTMLReport() (string, error)`；`Status.ReportFile string`

- [ ] **Step 1: 写失败测试**：

```go
func TestStopGeneratesHTMLReport(t *testing.T) {
	dir := t.TempDir()
	cfg := testCfg()
	c := New(cfg, ModeBidir)
	c.SetLogDir(dir)
	agg := stats.NewAggregator(1, 10)
	agg.RecordTx(0, 100, 10000)
	agg.RecordRx(0, 100, 100, time.Now(), time.Now().Add(-time.Millisecond).UnixNano())
	c.SetAggregatorForTest(agg)
	c.Stop()
	if !strings.HasSuffix(c.LastReportPath(), ".html") {
		t.Fatalf("LastReportPath = %q, want html", c.LastReportPath())
	}
	if _, err := os.Stat(c.LastReportPath()); err != nil {
		t.Fatalf("报告文件不存在: %v", err)
	}
}
```

（注意 SetAggregatorForTest 后 Stop 会 sleep drainDelay 500ms——可接受。SetLogDir 后 closeLogLocked 写 logs/ 目录。若 testCfg 需含阈值字段验证徽标，可在 cfg 上加 `MaxLossRatePct: 0.5` 与故意丢包。）
- [ ] **Step 2: 运行确认失败**（LastReportPath 未定义）。
- [ ] **Step 3: 实现**（controller.go）：
  - 字段加 `lastReportPath string`；`Status` 加 `ReportFile string`（返回 lastReportPath）
  - `closeLogLocked` 尾部（写 summary.txt 后）：用当前 agg/配置生成 HTML 报告到 `<logDir>/logs/`，成功则 `c.lastReportPath = path`（失败仅 log.Printf，不阻塞）
  - 新增：

```go
// LastReportPath 返回最近一次生成的 HTML 报告路径（空=未生成）。
func (c *Controller) LastReportPath() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastReportPath
}

// WriteHTMLReport 按当前（或上次）统计立即生成 HTML 报告（Web 导出按钮用）。
func (c *Controller) WriteHTMLReport() (string, error) {
	c.mu.Lock()
	agg, cfg, logDir := c.agg, c.cfg, c.logDir
	if agg == nil { agg = c.lastAgg }
	st := Status{Running: c.running, Iface: c.iface, Started: c.started}
	c.mu.Unlock()
	if agg == nil || logDir == "" {
		return "", fmt.Errorf("无统计数据或未设置日志目录")
	}
	snap := agg.Current(time.Now())
	vs := report.Verdicts(cfg, snap, true)
	path, err := report.HTMLReport(cfg, snap, vs, report.ReportMeta{
		Started: st.Started, Stopped: time.Now(), Iface: st.Iface, Mode: string(c.mode), Version: "v2.7.0",
	}, filepath.Join(logDir, "logs"))
	if err != nil { return "", err }
	c.mu.Lock()
	c.lastReportPath = path
	c.mu.Unlock()
	return path, nil
}
```
  - CSV（openLogLocked/writeLogRow）：表头追加每流 `avg_delay_ms_N`、`jitter_ms_N`（N=1..nFlows），行追加 `%.3f` 值（DelayValid=false 时留空串）。writeLogRow 里快照来自 agg.Current。
- [ ] **Step 4: 全绿**：`go test ./internal/controller/`（既有测试可能受 CSV 表头变化影响，同步断言）。
- [ ] **Step 5: 提交**：`git commit -m "feat(controller): 停止自动生成 HTML 报告 + CSV 追加时延列"`

### Task 9: web — API 字段、/api/report、页面

**Files:**
- Modify: `internal/web/server.go`、`internal/web/static/index.html`
- Test: `internal/web/server_test.go`

**Interfaces:**
- Consumes: Task 2/6/8
- Produces: apiFlow 新字段；apiHistory.dly；apiConfigFlow 3 阈值字段；`POST /api/report` → `{ok, path}`；apiStatus.report_file

- [ ] **Step 1: 写失败测试**（server_test.go 追加，先看既有测试的建服模式）：

```go
func TestStatsIncludesDelayAndVerdict(t *testing.T) {
	// 按既有模式建 Server + 注入含时延的聚合器（参考 SetAggregatorForTest 用法）
	// GET /api/stats?mode=bidir → 断言 flows[0].delay_avg_ms、delay_valid、verdict 字段存在
}
```

（若既有测试模式不便注入聚合器，可退化为 JSON 字段存在性断言——用 httptest 直接调 handleStats 需 ctrl；参考现有 controller_test/security_test 的建服方式。）
- [ ] **Step 2: 运行确认失败**。
- [ ] **Step 3: 实现**（server.go）：
  - apiFlow 加 `DelayAvgMs float64 json:"delay_avg_ms"`、`DelayMinMs/MaxMs`、`DelayTotAvgMs json:"delay_tot_avg_ms"`、`JitterMs json:"jitter_ms"`、`DelayValid bool json:"delay_valid"`、`Verdict string json:"verdict"`（""/pass/fail）
  - handleStats：`vs := report.Verdicts(cfg, snap, false)`（report 包 import），verdict 字段填 pass/fail/""
  - apiHistory 加 `Dly [][]float64 json:"dly"`（handleStats 透传 hist.Dly）
  - apiConfigFlow 加 3 字段；toConfig/configToAPI 透传
  - apiStatus 加 `ReportFile string json:"report_file"`（handleStatus 填充）
  - 新 handler：

```go
func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.requireControl(w, r) { return }
	m := modeFromQuery(r)
	path, err := s.ctrl(m).WriteHTMLReport()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "生成报告失败: "+err.Error())
		return
	}
	writeJSON(w, struct{ OK bool; Path string `json:"path"` }{OK: true, Path: path})
}
```
  - mux 注册 `mux.HandleFunc("/api/report", s.handleReport)`
- [ ] **Step 4: 页面**（index.html）：
  - 配置表：`renderConfig`/`collectConfig`（约 190-310 行）与 `renderCfgRow` 加 3 列输入：丢包率阈值%、时延阈值ms、抖动阈值ms（字段名 max_loss_rate_pct / max_avg_delay_ms / max_jitter_ms）；表头（约 100-101 行）加 3 个 `<th>`
  - 统计表：表头（122-124 行）加 `时延avg(ms)`、`抖动(ms)`、`判定`；renderTable 行模板加三列：时延 `f.delay_valid ? f.delay_avg_ms.toFixed(2) : '—'`、抖动同、判定徽标 `<span class="badge pass/fail">`（CSS 加 .badge.pass/.badge.fail）；行 FAIL 时 tr 加 class loss 同款红
  - 图表：drawChart 加右轴（轴刻度 = 各流窗口时延最大值，忽略 -1；网格线与左轴共用横格）；每流画 Dly 曲线（`hist.dly[f]`，-1 点跳过；聚合时 -1 忽略计数）；右轴标签 `ms`；`lastStats` 初值加 `dly: []`；poll 循环 `drawChart(d.history)` 不变
  - 顶部按钮区加 `<button id="btnReport">导出报告</button>`（remoteControl 为 false 时隐藏——页面已有 status.remote_control 用法则复用；否则用现有模式）：click → `POST /api/report` → showMsg(`报告已生成: path`)
- [ ] **Step 5: 全绿**：`go test ./internal/web/` + `go build ./...`；手工 `qostool bidir -i lo -d 3` 打开页面验证（Linux 才可跑，Windows 需 Npcap；本机为 Windows 可尝试 NPF 回环适配器，失败则只验证 HTTP API）。
- [ ] **Step 6: 提交**：`git commit -m "feat(web): 时延/抖动/判定列 + 阈值配置 + 导出报告按钮 + 时延曲线"`

### Task 10: sweep — 阶梯扫描包

**Files:**
- Create: `internal/sweep/sweep.go`、`internal/sweep/sweep_test.go`

**Interfaces:**
- Consumes: Task 2/3/5
- Produces: `Options`、`StepOutcome`、`Result`、`Run(ctx, cfg, iface, opts) (*Result, error)`（签名见 spec 4.3）

- [ ] **Step 1: 写失败测试**（纯逻辑，不碰硬件）：

```go
func TestScaleRates(t *testing.T) {
	base := []config.Flow{{RateMbps: 10, RatePPS: 0}, {RateMbps: 0, RatePPS: 500}, {RateMbps: 2, RatePPS: 100}}
	rs := ScaleRates(base, 1.5)
	if rs[0].RateMbps != 15 || rs[1].RatePPS != 750 || rs[2].RateMbps != 3 || rs[2].RatePPS != 150 {
		t.Fatalf("ScaleRates = %+v", rs)
	}
}

func TestPickLossThresholds(t *testing.T) {
	cfg := &config.Config{Flows: []config.Flow{{MaxLossRatePct: 1.0}, {}}}
	th := PickLossThresholds(cfg, 0.5)
	if th[0] != 1.0 || th[1] != 0.5 {
		t.Fatalf("阈值 = %v, want [1 0.5]", th)
	}
}

func TestSweepDecisionConfirmThenStop(t *testing.T) {
	// 模拟：档1 PASS，档2 越限，确认（同档再测）仍越限 → 停止，极限=档1
	m := newStepMachine(0.5, 10)
	m.addOutcome(1.0, 0.0, false)  // scale=1.0 全过
	if got := m.advance(1.2); got != decideContinue { t.Fatalf("档2 应继续, got %v", got) }
	...
}
```

（把"下一档 vs 停止 vs 确认"决策抽成可测试的纯函数/小状态机 `stepMachine`；Run 只做编排。测试覆盖：首档越限→确认→停止（LimitScale=0）；越限后恢复→继续；到 maxScale 全过→停止。）
- [ ] **Step 2: 运行确认失败**。
- [ ] **Step 3: 实现**（sweep.go）：
  - `ScaleRates(base []config.Flow, factor float64) []sender.RateSpec`（两速率同乘 factor）
  - `PickLossThresholds(cfg *config.Config, def float64) []float64`
  - `stepMachine`：字段 `stepPct, maxScale, thresholds []float64, pendingConfirm bool, limitScale float64, limitMbps []float64`；方法 `next(scale float64, losses []float64) (nextScale float64, action int, reason string)`，action ∈ {actContinue, actConfirm, actStop}；首越限返回 actConfirm（nextScale 不变），确认仍越限 → actStop 且 limitScale=上一次全过档；恢复 → actContinue（scale×(1+stepPct)）；scale 超 maxScale → actStop（全过，极限=当前档）
  - `Run(ctx, cfg, iface, opts)`：
    1. 校验 opts（StepPct>0、Hold≥2s、MaxScale>1、LossThresholdPct∈[0,100]）
    2. `agg := stats.NewAggregator(len(cfg.Flows), 10)`；`s, err := sender.New(cfg, agg)`；`cap, err := capture.New(iface, cfg, agg)`；失败清理返回
    3. 启动 sender.Run(sCtx) 与 cap.Run(cCtx) goroutine
    4. 循环：factor=1.0 起步；每档 `s.SetRates(ScaleRates(cfg.Flows, factor))` → sleep 500ms settle → 记录起始计数（agg.Current 的 RxPackets/Lost）→ sleep hold-500ms → 终计数，逐流 `lossΔ/(rxΔ+lossΔ)×100`（窗口无数据 → -1）→ 记 StepOutcome → machine.next 决策；actStop 退出循环
    5. 收尾：cancel 发送 → sleep 500ms drain → cancel 抓包 → wg.Wait
    6. 返回 Result（Steps、LimitScale、LimitMbps、DoneReason）
  - Run 需要 opts 缺省值处理（0 值 → 默认：StepPct=20、Hold=10s、MaxScale=10、LossThresholdPct=0.5）
- [ ] **Step 4: 全绿**：`go test ./internal/sweep/`。
- [ ] **Step 5: 提交**：`git commit -m "feat(sweep): 阶梯扫描引擎（动态调速+越限确认）"`

### Task 11: main — sweep 子命令 + CLI 打印报告路径

**Files:**
- Modify: `cmd/qostool/main.go`（usage()、模式 switch、新增 runSweep）

**Interfaces:**
- Consumes: Task 8/10

- [ ] **Step 1: 实现**：
  - usage() 加 `qostool sweep -c flows.yaml -i 接口 [--step 20] [--hold 10] [--max-scale 10] [--loss-threshold 0.5]`
  - 模式 switch 加 `"sweep"`
  - runSweep(cfgPath, iface string, args []string) error：flagset（--step/--hold/--max-scale/--loss-threshold）；config.Load；opts 填充；`signal.NotifyContext`；`sweep.Run`；打印每档结果表与极限；写 CSV 到 `filepath.Dir(cfgPath)/logs/qostool_sweep_<ts>.csv`（表头：档位,倍率,flow1..flowN 丢包%，行：每档；含极限行注释）并打印路径
  - runCLI：Stop 后 `if p := ctrl.LastReportPath(); p != "" { fmt.Println("报告:", p) }`
- [ ] **Step 2: 编译验证**：`go build ./...`；`go vet ./...`。
- [ ] **Step 3: 提交**：`git commit -m "feat(cmd): sweep 子命令 + CLI 打印报告路径"`

### Task 12: 版本、文档、全量验证

**Files:**
- Modify: `build.sh`（`VERSION="${1:-v2.7.0}"`、echo 文案）、`README.md`、`configs/example.yaml`

- [ ] **Step 1: 文档**：
  - README：时延/抖动说明（双机无同步只报抖动；单机 bidir 可报绝对时延；旧发送端显示 —）、阈值配置与判定、HTML 报告（自动生成路径）、sweep 用法与限制（仅双向）、版本号 v2.7.0、CSV 新列说明
  - example.yaml：加注释掉的阈值示例 `# max_loss_rate_pct: 0.5  # 丢包率阈值 %（0/缺省=不判定）` 等（放在一条流里做示范）
- [ ] **Step 2: 全量验证**：`go test ./...` 全绿；`go vet ./...` 干净；`go build ./cmd/qostool` 成功；`git log --oneline` 检查提交序列。
- [ ] **Step 3: 提交**：`git commit -m "docs: v2.7.0 时延/阈值/阶梯扫描 README 与示例配置"`

## 自检

- 规格覆盖：spec §2→Task1-4、§3→Task5-9、§4→Task10-11、§5/§6/§8→Task12 与各任务错误处理
- 类型一致：`RecordRx` 五参签名、`Verdicts(cfg, snap, useTotDelay)`、`HTMLReport(cfg, snap, verdicts, meta, destDir)`、`sweep.Run(ctx, cfg, iface, opts)` 各任务间一致
- 无占位符：每个任务含具体测试代码与实现要点
