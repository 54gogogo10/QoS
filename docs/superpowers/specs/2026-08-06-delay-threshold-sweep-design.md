# qostool v2.7.0 设计：时延/抖动测量 + 阈值判定与 HTML 报告 + 吞吐量阶梯扫描

日期：2026-08-06
状态：已与用户确认（2026-08-06，按建议顺序 1→3→2 实现）

## 1. 目标

在现有"速率 + 丢包率"测量基础上补齐 QoS 验收三大件（时延、抖动、丢包）：

1. **单向时延 + 抖动测量**（每流 min/avg/max 时延、RFC3550 抖动）
2. **阈值判定 + HTML 测试报告**（每流可配丢包率/时延/抖动阈值，停止时自动出报告）
3. **吞吐量阶梯扫描**（新子命令 `sweep`，自动加压测出每流极限速率）

版本号 v2.6.0 → v2.7.0（build.sh 默认值 + README）。

## 2. 功能 1：时延/抖动

### 2.1 协议层（internal/protocol）

载荷头 10B → 18B：`[magic 4B][flow_id 2B][seq 4B][send_ts 8B]`，send_ts 为发送时刻 UnixNano（大端）。

- `EncodeHeader(payload, flowID, seq, sendTs)`：全头编码（含时间戳）
- `EncodeSeqTs(payload, seq, sendTs)`：发送循环内只更新 seq + 时间戳
- `DecodeHeader(payload) (flowID, seq, sendTs, ok)`：**sendTs==0 表示旧版发送端**（载荷只有 10B 头，无时间戳）；10B ≤ len < 18B 仍返回 ok=true 且 sendTs=0

兼容性：旧接收端读新包（多余字节忽略）不受影响；新接收端遇旧发送端时延显示 `—`（DelayValid=false）。

### 2.2 统计层（internal/stats）

`FlowSnapshot` 新增：
- `DelayValid bool`：是否收到过带时间戳的包
- `DelayAvgMs float64`：1s 滑动窗口平均时延
- `DelayTotAvgMs float64`：全程累计平均时延（报告/最终判定用）
- `DelayMinMs / DelayMaxMs float64`：全程累计最小/最大
- `JitterMs float64`：RFC3550 抖动（累计，`J += (|D(i-1,i)| - J)/16`）

`RecordRx(flowIdx, bytes, seq, recv time.Time, sendTs int64)`：
- sendTs>0 时更新时延状态：窗口 sum/count、累计 min/max、抖动
- sendTs<=0 只做原有计数（旧发送端兼容）
- 时延为负（时钟差）时钳到 0
- 每流一个 `sync.Mutex` 保护时延状态（RecordRx 抓包线程 / Snapshot 采样线程 / Current Web 线程三处访问）

`Snapshot()` 内：窗口 sum/count 读取后清零（窗口语义），min/max/抖动累计不清零。

`History` 新增 `Dly [][]float64`：每流窗口平均时延（无效为 -1 哨兵，JSON 避免 NaN）。

### 2.3 发送层（internal/sender）

发送循环内每包：`protocol.EncodeSeqTs(f.payload, f.seq, time.Now().UnixNano())`。

### 2.4 抓包层（internal/capture）

两处 RecordRx 调用传入到达时间与解码出的 sendTs：
- Windows pcap 路径：`ci.Timestamp`（pcap 内核时间戳）
- Linux AF_PACKET 路径：`time.Now()`（处理后立即取）

### 2.5 展示

- 终端表格：新增 `时延ms`（`avg/min/max`，如 `1.2/0.8/3.1`）与 `抖动ms` 两列，无数据 `—`
- Web 表格：新增 `时延avg(ms)`、`抖动(ms)`、`判定` 三列
- Web 曲线：时延曲线（右侧独立 Y 轴，无效点跳过）
- 汇总报告/HTML 报告：每流 avg/min/max 时延 + 抖动
- CSV 日志：每流追加 `avg_delay_ms_N`、`jitter_ms_N` 两列（末尾追加，不影响旧列）

### 2.6 口径说明

双机无时钟同步时绝对时延不可靠（偏差=时钟差），**抖动仍有效**；单机 bidir/环回可报绝对时延。不引入时钟同步机制。

## 3. 功能 3：阈值判定 + HTML 报告

### 3.1 配置（internal/config）

每条流新增 3 个可选字段：
```yaml
max_loss_rate_pct: 0.5   # 丢包率阈值 %，0/缺省=不判定
max_avg_delay_ms: 50     # 平均时延阈值 ms，0/缺省=不判定
max_jitter_ms: 10        # 抖动阈值 ms，0/缺省=不判定
```
校验：`max_loss_rate_pct` 必须在 0-100；三个字段均不得为负。

### 3.2 判定逻辑（internal/report）

```go
type Verdict struct {
	FlowIdx int
	Checked bool     // 是否配置了任何阈值
	Pass    bool
	FailMsg string   // 首个违反的阈值描述，如 "丢包率 2.30% > 0.50%"
}
func Verdicts(cfg *config.Config, snap []stats.FlowSnapshot, useTotDelay bool) []Verdict
```
- 丢包：`MaxLossRatePct>0` 且（RxPackets>0 或 Lost>0）且 `LossRate*100 > 阈值` → FAIL
- 时延：`MaxAvgDelayMs>0` 且 DelayValid 且 平均时延 > 阈值 → FAIL（useTotDelay=true 用 DelayTotAvgMs，否则 DelayAvgMs）
- 抖动：`MaxJitterMs>0` 且 DelayValid 且 JitterMs > 阈值 → FAIL
- 无数据（未收到任何包）不判定丢包，但时延/抖动因 DelayValid=false 自然跳过
- 未配置任何阈值 → Checked=false

Web 实时判定用窗口时延（useTotDelay=false）；最终报告/汇总用累计（useTotDelay=true）。

### 3.3 HTML 报告（internal/report/html.go）

```go
type ReportMeta struct {
	Started, Stopped time.Time
	Iface, Mode, Version string
}
func HTMLReport(cfg *config.Config, snap []stats.FlowSnapshot, verdicts []Verdict, meta ReportMeta, destDir string) (string, error)
```
- 自包含单文件（内联 CSS，深色主题），文件名 `qostool_report_<时间戳>.html`
- 内容：测试信息（时间/时长/接口/模式/版本）、每流明细表（收发包/字节、丢包率、时延 avg/min/max、抖动、阈值、判定徽标）、结论汇总（PASS/FAIL 计数）

### 3.4 生成时机（internal/controller）

- `closeLogLocked`（停止时）自动生成，路径存入 `c.lastReportPath`，新增 `LastReportPath()`
- 新增 `WriteHTMLReport() (string, error)`：按需生成（Web"导出报告"按钮）
- `Status` 增加 `ReportFile` 字段（/api/status 返回）
- CLI 停止后打印报告路径

### 3.5 Web

- `apiFlow` 增加 `delay_avg_ms / delay_min_ms / delay_max_ms / delay_tot_avg_ms / jitter_ms / delay_valid / verdict`（verdict: "pass"/"fail"/""，服务端用 LiveVerdicts 计算）
- `apiHistory` 增加 `dly`
- `apiConfigFlow` 增加 3 个阈值字段（toConfig/configToAPI 透传）
- 新 API `POST /api/report` → `{path}`（requireControl 保护）
- `/api/status` 增加 `report_file`
- 页面：配置表加 3 列阈值输入；统计表加 时延/抖动/判定 列（FAIL 徽标红底、PASS 绿底）；"导出报告"按钮（remoteControl=false 时隐藏）

## 4. 功能 2：阶梯扫描（CLI 优先）

### 4.1 子命令

```
qostool sweep -c flows.yaml -i eth0 [--step 20] [--hold 10] [--max-scale 10] [--loss-threshold 0.5]
```
- `--step`：每档递增百分比（默认 20，即 1.0x → 1.2x → 1.4x …）
- `--hold`：每档保持秒数（默认 10，测量窗口 = hold - 0.5s settle）
- `--max-scale`：最大倍率（默认 10，超出即止，全部 PASS 则极限 = 最后一档）
- `--loss-threshold`：全局丢包率阈值 %（默认 0.5），被每流 `max_loss_rate_pct` 覆盖

**仅双向模式**（本机发送+抓包）：损耗测量依赖本端抓包；双机场景 v1 不支持（发送端无法得知远端丢包）。README 说明。

### 4.2 发送端动态调速（internal/sender）

```go
type RateSpec struct{ RateMbps, RatePPS float64 }
func (s *Sender) SetRates(rates []RateSpec) error // len 必须等于流数
```
- `flow` 增加 `cfg config.Flow` 副本与原子 pacing 字段（batchGapNs atomic.Int64 / batchN atomic.Int32）
- `SetRates` 更新每流 cfg 副本并重算 pacing；run() 每批读取原子值——**seq 不中断、socket 不重建**
- 发送端 DSCP/5元组不变，仅速率缩放

### 4.3 扫描算法（internal/sweep）

```go
type Options struct {
	StepPct   float64       // 每档递增 %，默认 20
	Hold      time.Duration // 每档保持，默认 10s
	MaxScale  float64       // 最大倍率，默认 10
	LossThresholdPct float64 // 全局丢包阈值 %，默认 0.5
}
type StepOutcome struct {
	Scale    float64
	RateMbps []float64 // 每流实际 Mbps（0=纯 pps 流）
	LossPct  []float64 // 每流窗口丢包率 %（窗口无数据 = -1）
	Over     bool
}
type Result struct {
	Steps      []StepOutcome
	LimitScale float64   // 最后一档全 PASS 的倍率（0 = 起始档即越限）
	LimitMbps  []float64 // 该档每流速率
	DoneReason string
}
func Run(ctx context.Context, cfg *config.Config, iface string, opts Options) (*Result, error)
```

状态机：
1. factor 从 1.0 起；每档：SetRates(按 factor 缩放) → settle 0.5s → 测量窗口丢包率（本档起止累计计数差值，`lostΔ/(rxΔ+lostΔ)`）→ 判定 over
2. **首次越限**：保持同档再测一个 hold（确认，防抖动误判）；恢复 → 继续加压；仍越限 → 停止，极限 = 前一档
3. 全程 PASS 直到 factor > maxScale → 停止，极限 = 最后一档
4. 起始档（1.0x）即越限并确认 → LimitScale=0（"低于起始档"）

内部直接组装 sender + capture + aggregator（不经 controller），不启 Web；停止顺序同 controller（先停发送 → drain 0.5s → 停抓包）。

### 4.4 输出

- 逐步打印：档位、倍率、每流丢包率
- 结束：极限档位/倍率/每流极限速率、结束原因
- 汇总 CSV 写入 `<配置目录>/logs/qostool_sweep_<时间戳>.csv`（每档一行），打印路径

## 5. 影响面汇总

| 包 | 改动 |
|---|---|
| protocol | 头 10B→18B、EncodeSeqTs、DecodeHeader 返回 sendTs |
| stats | FlowSnapshot 时延字段、RecordRx 签名、History.Dly、窗口重置 |
| sender | 每包写时间戳、SetRates 动态调速（原子 pacing） |
| capture | 传 ci.Timestamp/time.Now() 与 sendTs（两个平台文件） |
| config | Flow 3 个阈值字段 + 校验 |
| report | 表格/汇总时延列、Verdicts、HTMLReport |
| controller | 停止时自动报告、WriteHTMLReport、LastReportPath、CSV 追加时延列 |
| web | API 字段、/api/report、表格/配置表/曲线/按钮 |
| cmd/qostool | sweep 子命令、CLI 打印报告路径、usage |
| sweep（新包） | 阶梯扫描 |
| build.sh / README / example.yaml | 版本 v2.7.0、文档、阈值示例 |

## 6. 错误处理

| 场景 | 行为 |
|---|---|
| sweep 参数非法（step≤0、hold<2s、max-scale≤1、阈值越界） | 启动即报错 |
| sweep 起始档即越限 | 确认后停止，极限=低于起始档，明确提示 |
| 阈值字段非法（负数、丢包率>100） | 配置校验报错 |
| HTML 报告写失败 | 记日志不阻塞测试，返回错误给调用方 |
| 抓包接口在 sweep 中故障 | 同现有 capture 错误处理（记录，统计停更） |

## 7. 测试

- protocol：新头编解码、旧 10B 头解码（ts=0）、短载荷、坏 magic、EncodeSeqTs
- stats：时延 avg/min/max、抖动递推、窗口重置、旧包（无 ts）DelayValid=false、History.Dly
- config：阈值字段 yaml 往返、非法值报错
- report：Verdicts 三类阈值 PASS/FAIL/未配置、HTML 报告内容
- sender：SetRates 后 pacing 变化、seq 连续性（不重置）
- sweep：缩放计算、窗口丢包率、确认逻辑（首越限→确认→停止/恢复）、阈值覆盖（每流 vs 全局）
- controller：停止后自动生成 HTML 报告
- web：apiFlow 新字段、/api/report

## 8. 兼容性

- 旧接收端 + 新发送端：多余 8B 忽略，正常
- 新接收端 + 旧发送端：时延显示 `—`，阈值判定只查丢包
- CSV：末尾追加新列，旧列顺序不变
- Win7（Go 1.20）：atomic.Int64/Int32 自 1.19 可用，无新依赖
