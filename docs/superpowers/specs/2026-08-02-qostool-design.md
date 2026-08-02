# QoS 测试工具 qostool 设计文档

日期：2026-08-02
状态：已与用户确认

## 1. 目标

开发一个跨平台（Windows + Linux）的 QoS 测试工具，用于在测试环境中生成和测量不同 DSCP 优先级的流量：

- 支持 8 条不同优先级的流量，每条流可独立配置 IP 5 元组、DSCP 值和发送速率
- 支持发送与接收，接收端可选择监听接口
- 按 DSCP 优先级（精确到每条流）实时统计发送/接收速率、丢包率

## 2. 技术选型

| 项 | 选择 | 理由 |
|---|---|---|
| 语言 | Go | 单二进制分发、goroutine 并发 8 流、内嵌 Web 服务器、微秒级定时精度 |
| 发送路径 | UDP socket + `IP_TOS` / `IPV6_TCLASS` | 最快最稳，OS 打 DSCP 并计算校验和，Win/Linux 原生支持，无需提权 |
| 接收路径 | pcap（Linux libpcap / Windows Npcap） | 需要按接口抓包并解析 IP 头 DSCP 字段 |
| 配置格式 | YAML | 8 条流结构化描述，两端共用同一份 |
| 依赖 | gopacket、gopkg.in/yaml.v3 | 最小化 |

## 3. 运行模式

单个可执行文件 `qostool`，三个子命令：

| 命令 | 作用 |
|---|---|
| `qostool send -c flows.yaml` | 纯发送（单向测试发送端） |
| `qostool recv -c flows.yaml -i <iface>` | 纯接收（单向测试接收端，`-i` 选择监听接口） |
| `qostool bidir -c flows.yaml -i <iface>` | 双向：同时发送 8 条流 + 抓包统计（`-i lo` 可环回自测） |

通用选项：

- `--web [port]`：启动内嵌 Web 界面，默认端口 **16666**；指定端口则用指定值
- `-d <seconds>`：限时运行，默认一直运行到 Ctrl+C
- `--interval <ms>`：统计采样/显示刷新间隔，默认：采样 100ms、终端重绘 1s、Web 轮询 500ms

## 4. 配置文件

一份 YAML 描述 8 条流，收发两端共用同一份（接收端据此精确匹配）。

```yaml
flows:
  - name: EF-语音
    protocol: udp
    src_ip: 192.168.1.10
    dst_ip: 192.168.1.20
    src_port: 10000
    dst_port: 20000
    dscp: 46          # 0-63 任意值；也支持名字别名（EF/AF11-AF43/CS0-CS7 等）
    rate_mbps: 10     # 带宽限速，可选
    rate_pps: 1000    # 包速率限速，可选（至少填一个）
    payload_size: 128 # UDP payload 字节数
```

要点：

- **DSCP 支持全部 64 个值（0–63）**：任意数值合法，常用名字（EF、AF11–AF43、CS0–CS7 等）仅为别名，解析后一律按数值处理。接收端匹配、统计表显示均按实际数值。
- 速率双参数：`rate_mbps` 和 `rate_pps` 可同时填，取先达到者为上限；至少填一个。
- 校验规则：DSCP ∈ [0,63]；IP 地址合法；端口 ∈ [1,65535]；payload_size ≥ 0；速率参数 ≥ 0。任一不合法 → 启动即报错，指出哪条流的哪个字段错。

## 5. 发送引擎

- **每流一个 goroutine + 一个 UDP socket**：socket 绑定 `src_ip:src_port`，`connect` 到 `dst_ip:dst_port`。
- **DSCP 标记**：IPv4 用 `setsockopt(IP_TOS)`，IPv6 用 `IPV6_TCLASS`，值 = `dscp << 2`。
- **双参数令牌桶限速**：字节桶（按 rate_mbps）+ 包桶（按 rate_pps），哪个先耗尽就等哪个。调度粒度 10ms：每 10ms 计算本轮可发包数，批量 `sendto`，避免逐包睡眠。
- **包格式**：`[magic 4B][flow_id 2B][seq 4B] + 填充字节`，总长 = 8 + payload_size。seq 从 1 递增，接收端据此计算丢包。
- **防漂移**：按绝对时间点调度（记录下次唤醒绝对时间，而非每次固定 sleep），长时间运行不漂移。
- **速率精度**：10ms 批调度下 1 Gbps 以内 ±1%，高 pps 小包可支撑数万 pps/流。

## 6. 接收与统计

- **抓包**：pcap 打开指定接口，BPF 过滤 `ip or ip6` 减少无关包。抓包在独立 goroutine，解析结果经 channel 交给统计线程，不阻塞抓包。
- **解析**：IPv4 头 TOS 高 6 位 = DSCP；IPv6 头 Traffic Class 高 6 位 = DSCP。提取协议、源/目的 IP、源/目的端口组成 5 元组。
- **精确匹配**：与配置的 8 条流逐一比对（DSCP + 5 元组全等才算命中）。命中后校验 magic 与 flow_id，按 seq 更新：总包数、总字节数、收到包数、丢失包数。seq 跳号即丢包（丢失 = 跳过序号个数），乱序包按已收计数（不做重排）。
- **统计快照**：每 100ms 一次，实时速率按滑动窗口计算（该窗口字节数/时长）。丢包率 = 丢失 / (已收 + 丢失)。
- **未命中显示**：单向模式下未配置发送的流 RX 显示 `N/A`；匹配不上任何流的包静默丢弃。
- **汇总报告**：Ctrl+C 或限时到点后打印：总时长、每流总收发字节/包、平均速率、总丢包率。

## 7. 终端显示与 Web 界面

### 终端

- 每秒重绘实时表格（ANSI 清屏 + 光标定位），列：`流名 | DSCP | 5元组 | TX Mbps/pps | RX Mbps/pps | 丢包率`。
- 非 TTY（输出重定向）时自动降级：不清屏，每 5 秒一行紧凑日志。
- 8 行固定对应 8 条配置流；未发送的流 TX 显示 `-`，未收到的流 RX 显示 `N/A`。

### Web（默认端口 16666）

- 单页 HTML 经 `embed` 打进二进制，零外部依赖（不引 CDN，离线可用）。
- 每 500ms 轮询 `GET /api/stats`，返回：8 条流当前 TX/RX 速率、包/字节计数、丢包率 + 最近 5 分钟历史序列（100ms 粒度，最多 3000 点）。
- `<canvas>` 手绘曲线：每流一行 TX/RX 双曲线（分色），横轴时间、纵轴 Mbps；下方表格同终端布局，丢包率非零时该行标红。
- 打开即自动刷新；浏览器关闭不影响 CLI 运行。

## 8. 项目结构

```
QoS/
├── go.mod
├── README.md
├── configs/
│   └── example.yaml        # 8 条流示例配置
├── cmd/qostool/main.go     # CLI 入口（子命令 + flag 解析）
├── internal/config/        # YAML 加载与校验（DSCP 0-63、端口/IP、速率参数）
├── internal/sender/        # 发送引擎（令牌桶、DSCP 标记、pacing）
├── internal/capture/       # pcap 抓包 + IPv4/IPv6 解析 + 5元组提取
├── internal/stats/         # 流计数、100ms 快照、速率计算、历史环形缓冲
├── internal/report/        # 终端实时表格 + 汇总报告
├── internal/web/           # 内嵌 HTTP 服务（/api/stats、静态页）
└── web/index.html          # 前端单页（canvas 曲线）
```

依赖仅 3 个：`gopacket`（pcap 绑定）、`gopkg.in/yaml.v3`，其余标准库。

## 9. 错误处理

| 场景 | 行为 |
|---|---|
| 配置不合法 | 启动即报错，指出哪条流哪个字段错 |
| 接口不存在 / 无权限打开 | 明确报错退出 |
| Windows 未安装 Npcap | 提示下载链接（npcap.com）后退出 |
| 本地 src_port 冲突 | 报错退出 |
| Web 端口被占 | 提示用 `--web` 换端口 |
| 抓包线程错误（如接口被拔） | 记录错误，统计停更并提示 |

## 10. 测试

**单元测试**：

- 令牌桶限速精度（短时发包量 vs 理论值，±容差）
- IPv4/IPv6 DSCP 字段解析
- 5 元组精确匹配
- seq 跳号丢包计算
- DSCP 名字 ↔ 数值映射（含 0-63 边界）

**端到端**：`bidir -i lo`（Linux lo / Windows 需安装并启用 Npcap Loopback Adapter 才能抓到环回流量）跑数秒，验证 8 条流全部收发、丢包率 ≈ 0、速率符合配置。Windows 上如未启用环回抓包，`-i lo` 会收不到包，README 中注明该前提。

## 11. 分发

`go build` 产出单二进制，Windows/Linux 各编译一份。接收端所在 Windows 机器需安装 Npcap（仅接收路径依赖）。
