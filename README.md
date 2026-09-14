# qostool — QoS 测试工具

跨平台（Windows / Linux）QoS 流量生成与测量工具：发送 8 条不同 DSCP 优先级的 UDP 流，
接收端按 DSCP + 5 元组精确匹配，实时统计每条流的收发速率、**丢包率、时延与抖动**。
提供终端实时表格与 Web 曲线页面（默认端口 16666）。

## 一键构建

```bash
bash build.sh v2.9.0   # 一次产出全部平台：Win10/11 app+CLI、Linux amd64/arm64、Win7
```

- 自动处理 Win7 所需的 Go 1.20 工具链（C:\go1.20\go）与依赖降级（gopacket v1.2.0 + x/sys v0.13.0），构建后自动恢复 go.mod
- 产物打包到 `dist/<版本>/` 与 `dist/<版本>-win64.zip`

## Windows 7 版本

`qostool-win7.exe`（x86_64）使用 Go 1.20 工具链编译，兼容 Windows 7 SP1。

- **仅命令行模式**：`qostool-win7.exe send/recv/bidir -c flows.yaml -i 接口`
- Windows 7 **不支持 WebView2**：双击 exe 不会弹内嵌窗口，页面操作需在 Win7 上安装现代浏览器（Chrome/Edge 等）后访问 http://127.0.0.1:16666
- 抓包需安装支持 Win7 的 **Npcap 版本**（1.7x 或更早，https://npcap.com 下载旧版；或 WinPcap）
- 发送流量（send 模式）不依赖 Npcap

## 麒麟 V10 / Linux 版本

`qostool-linux-amd64`（x86_64：兆芯/海光/Intel）与 `qostool-linux-arm64`（鲲鹏/飞腾）为
**纯 Go 静态编译**（零外部依赖、无 libpcap），Linux 接收端使用 AF_PACKET 原始套接字抓包。

```bash
sudo ./qostool lsdev                    # 查看接口（eth0/lo 等）
sudo ./qostool bidir -c config.yaml -i eth0   # 双向测试（抓包需要 root）
```

- 无内嵌窗口（Linux 版打开页面自动调用系统默认浏览器）
- 建议双机测试：发送机与接收机各跑一份，配置中 src/dst 填实际 IP
- 接收端同步发送端统计（远端TX）：接收端填入发送端地址（如 192.168.1.10:16666）。
  前提：① 发送端程序必须开着 Web 服务（默认 16666，监听所有接口）；
  ② 发送端机器防火墙需放行 16666 端口（首次运行时允许，或手动加规则）；
  ③ 两台机器网络互通
- 麒麟 V10 需以 root 或 sudo 运行（AF_PACKET 抓包权限要求，与 tcpdump 相同）

## 安全说明

- Web 服务（16666）默认监听所有接口且无认证：**内网中任何能访问该端口的主机都可控制测试**（启停/改配置）。
  请在可信网络中使用；如需限制，用防火墙仅放行指定机器的 16666 端口，
  或命令行加 `--bind 127.0.0.1`（v2.9.0）只监听本机回环——单机自测推荐，
  注意该模式下远端机器将无法访问本端页面/统计（收发同步不可用），需要跨机器同步时不要加
- 已内置防护：控制类 API 校验 Content-Type 为 JSON（防跨站表单伪造 CSRF）、请求体 1MB 上限、
  HTTP 读写超时、包解析对畸形包安全（随机模糊测试通过）
- 机器间收发同步（v2.8.1）带 `remote` 标记的启停通知**不走页面只读限制**：
  CLI 模式（页面只读）的接收端也会接受内网发送端的同步启停——即内网任意主机可带该标记
  启停你的接收端监听（与"无认证、可信内网"威胁模型一致）；不可信网络请用防火墙/`--bind` 收敛
- 接收端自动注册有加固：上报端口必须是 1-65535 数字、注册表有容量上限并淘汰过期项；
  机器间通知（监听/停止同步）只允许 host:port 形式的合法地址，远端响应体读取有 1MB 上限
- 接收端/发送端地址均为用户手动配置，工具不会主动向外发起连接（仅按配置拉取/推送）

## 两种使用方式

**桌面应用（推荐）**：双击 `qostool-app.exe` → 弹出内嵌窗口（无需浏览器），
页面内可选择监听接口、编辑 8 条流的配置（5 元组 / DSCP / 速率）、开始/停止测试并实时查看曲线。
配置自动保存到 exe 同目录的 `config.yaml`。需要 Windows 10/11 自带的 WebView2 运行时。

> ⚠️ **Windows 上请以管理员身份运行**（右键 → 以管理员身份运行）：
> 操作系统限制普通用户设置网络控制类 DSCP（CS6=48、CS7=56 等），
> 普通权限下含这些值的流会启动失败（iperf3 等工具同样受限）。

### Windows DSCP 标记（v2.8.0）

Windows 7/8/10 上应用直接 `setsockopt(IP_TOS)` 设置的 DSCP 会被系统剥掉（发出的包 DSCP=0，
但调用返回成功——iperf3/libzmq 等工具均有此问题）。v2.8.0 起发送端优先使用 **QoS2 (qWave)**
API 打标（微软官方应用标记路径，Teams/Webex 同款）：`QOSAddSocketToFlow` + `QOSSetFlow(QOSSetOutgoingDSCPValue)`，
失败时自动回退 IP_TOS。

- **适用系统**：Windows 7 及以上（qwave.dll 内置）；QOSSetOutgoingDSCPValue 需管理员或
  Network Configuration Operators 组成员（与上方管理员要求一致）
- **限制**：qWave 只支持发往**其他主机**的流；单机自测（127.0.0.1 环回/发往本机地址）自动回退 IP_TOS，
  无警告（该场景 Win11 正常，Win7/10 上如需验证 DSCP 请用双机）
- 若双机测试时接收端仍看到 DSCP=0，请检查组策略：计算机配置 → Windows 设置 → 基于策略的 QoS
  → 高级 QoS 设置 → DSCP 标记覆盖，确认不是“忽略”（该设置会连 QoS2 一起剥掉）；
  可用事件查看器 Microsoft-Windows-QoS 日志中的 APP_MARKING_IGNORED（事件 16510）确认

### 自动系统配置（v2.8.0，管理员运行时）

管理员运行时自动放行系统对应用 DSCP 标记的限制，退出时自动恢复原值（`--no-sysconfig` 禁用）：

- `DisableUserTOSSetting=0`（Tcpip\Parameters）：允许应用通过 IP_TOS 设置 DSCP（IP_TOS 兜底路径前提）
- `Do not use NLA=1`（Tcpip\QoS）：非域环境（工作组）下 Policy-based QoS 生效的前提

**为什么不自动设置组策略**：DSCP 标记覆盖（DSCP Marking Override）存储在 registry.pol 二进制策略文件，
直接写注册表无效（策略刷新会覆盖）；而 Policy-based QoS 策略（netsh/PowerShell 创建）实测 Win11 上不生效、
Win10 上社区大量失败案例，仅 Win7 + “Do not use NLA” 组合可靠——因此工具不自动创建/修改组策略，
主路径 QoS2 不依赖任何组策略，上述注册表项仅为 IP_TOS 兜底路径扫清障碍

**命令行**：适合脚本化与无人值守，见下方用法。

## 构建

依赖：Go ≥ 1.22、libpcap（接收路径）、cgo 编译器。

- **Linux**: `sudo apt install libpcap-dev gcc` 后 `go build -o qostool ./cmd/qostool`
- **Windows**: 需要 MinGW gcc、Npcap（运行时，https://npcap.com）、Npcap SDK（解压到 C:\WpdPack）：

```bash
export CGO_ENABLED=1
export CGO_CFLAGS="-IC:/WpdPack/Include"
export CGO_LDFLAGS="-LC:/WpdPack/Lib/x64 -lwpcap"
go build -o qostool.exe ./cmd/qostool                      # 命令行版
# 桌面版：go get github.com/jchv/go-webview2 后
# 把 tools/WebView2Loader.dll 放到 exe 同目录
GOFLAGS="" go build -ldflags "-H windowsgui" -o qostool-app.exe ./cmd/qostool
```

## 用法（命令行）

```bash
qostool lsdev                                  # 列出抓包接口
qostool send  -c configs/example.yaml          # 纯发送（单向测试发送端）
qostool recv  -c configs/example.yaml -i eth0  # 纯接收（单向测试接收端）
qostool bidir -c configs/example.yaml -i eth0  # 双向：同时发送与接收统计
qostool sweep -c configs/example.yaml -i eth0  # 阶梯扫描：自动加压找极限速率
```

通用选项：`--web 16666`（Web 端口，`--web off` 关闭）、`--bind 127.0.0.1`（限制 Web 监听地址，v2.9.0，
默认所有接口）、`-d 秒数`（限时运行）、`--interval 毫秒`（统计采样间隔，默认 100）。

Web 页面：浏览器打开 `http://<本机IP>:16666`，实时曲线 + 表格，丢包率非零的行标红。

### 收发同步（v2.8.1）

发送端页面勾选接收端后点击**开始**：自动通知接收端开始监听（等待 2s 就绪后再发包，不漏起始包）；
点击**停止**（或发送端进程退出）：自动通知接收端，接收端**延迟 1s 停止监听**（排空在途包，避免尾部差虚假丢包）。

- 同步通知走机器间 HTTP（`/api/start`、`/api/stop` 带 `remote` 标记），不受 CLI 页面只读限制；接收端延迟停止期间若被手动重启/停止，延迟停止自动跳过（不误停新会话）
- 手动停止接收端（页面/`Ctrl+C`）不受延迟影响，立即停止

### 退出码与 JSON 汇总（v2.8.0）

脚本化/CI 集成：测试结束后按阈值判定返回退出码，并可用 `--json` 只输出机器可读汇总。

```bash
qostool bidir -c flows.yaml -i eth0 -d 60 --json   # 只输出 JSON，不打印实时表格
qostool bidir -c flows.yaml -i eth0 -d 60; echo $? # 退出码 0/1
```

- **退出码**：`0`=测试完成且全部通过（或未配置阈值）；`1`=有流未通过阈值（或运行错误）；`2`=用法错误。sweep 模式起始档（1.0x）即越限时同样退出 1
- `send` 模式无接收数据，不参与阈值判定（避免“只发不收=100% 丢包”误判），正常完成退出 0
- `--json` 输出单个 JSON 对象：`verdict`（pass/fail/none）、`fail_count`、每流收发/丢包/乱序/时延（avg/p95/p99/min/max/jitter）/判定明细、`html_report` 路径等；丢包口径与文本汇总一致（bidir 含停止瞬间尾部差，recv 不计）

## 配置

见 `configs/example.yaml`。每条流：5 元组（src/dst IP、端口、udp 协议）、`dscp`（0-63 或名字如 EF/AF41/CS7）、
`rate_mbps` 与 `rate_pps`（双参数限速，至少填一个，取先到者）、`ip_len`（IP 包总长，含 IP/UDP 头，IPv4 最小 46、IPv6 最小 66——18 字节协议头含发送时间戳，时延/抖动统计依赖它）。

收发两端必须使用同一份配置；发送载荷内嵌序列号与发送时间戳，接收端据此计算丢包率与时延。

### 阈值判定（v2.7.0）

每条流可选配置 3 个阈值，测试过程中实时判定（Web 表格 PASS/FAIL 徽标），停止时汇总进报告：

```yaml
  max_loss_rate_pct: 0.5   # 丢包率阈值 %（0/缺省=不判定）
  max_avg_delay_ms: 50     # 平均时延阈值 ms（0/缺省=不判定）
  max_jitter_ms: 10        # 抖动阈值 ms（0/缺省=不判定）
```

- 实时判定用最近 100ms 窗口时延；**最终判定与汇总同口径**：丢包 = seq 空洞 + 尾部差（只发不收视为 100% 丢包）
- 未配置阈值的流不参与判定

### 时延/抖动测量（v2.7.0）

发送载荷头 v2.7.0 起为 18 字节（magic + flow_id + seq + 8 字节发送时间戳）。

- 每流实时统计：平均时延（100ms 窗口）、全程 min/avg/max 时延、RFC3550 抖动
- **双机无时钟同步**：时延按最小观测差值自动校正（v2.8.1，优先用控制通道测得的
  时钟偏差做基准、与测试流最小观测差值取更小者），报告的是**相对最小时延**
  （消除了固定时钟偏差；时钟漂移的残余偏差可能使瞬时值为负，已钳 0）；
  时钟偏差估计（≈两机时钟差，**接收端−发送端**，接收端快为正）显示在终端汇总与
  HTML 报告（meta 表）中，`--json` 输出 `clock_offset_ms`（Web 页面顶部状态栏的
  时钟偏差为相反方向：发送端−接收端）
- **抖动不受时钟偏差影响**（RFC3550 相邻差抵消固定偏移）；单机 bidir / 环回自测时
  偏差≈最小时延，时延即真实值
- 旧版发送端（无时间戳）兼容：时延显示 `—`，抖动/时延阈值自动跳过，丢包统计不受影响
- 时延曲线显示在 Web 图表右侧独立 Y 轴（ms）

### 时延百分位 p95/p99（v2.8.0）

每流全程累计 p95/p99 时延（1ms 直方图桶，报告桶下界；>2000ms 的样本入溢出桶，用全程最大时延代表）。

- 显示位置：终端汇总与 HTML 报告（时延列 = avg/p95/p99/max）、Web 表格新增“时延p95/时延p99”列、CSV 新增 `p95_delay_ms_N`、`p99_delay_ms_N` 列
- 无时延数据（旧发送端）时显示 `—`

### 乱序容忍（v2.8.0）

seq 空洞不再立即计丢包：先跟踪最多 64 个包、保留 200ms 宽限期，宽限期内到达的乱序包逐个补齐空洞（不计丢包）；超时未补齐或空洞被更大的空洞越过时，剩余缺失才计为丢包。更大的空洞（>64 包）立即计丢包。停止时未补齐的孔洞直接结算，最终统计不遗漏。

### 乱序包统计（v2.9.0）

每流统计**乱序到达包数**：到达时 seq 已小于当前最大已见 seq 的包（补齐空洞的包与极晚到达的包均计）。
孔内重复包与最新包重复不计；孔洞关闭后到达的重复包无法与极晚乱序包区分，按乱序计（可接受的高估）。

- 显示位置：Web 表格“乱序”列、终端汇总“乱序”列、HTML 报告、`--json` 的 `reordered` 字段、CSV 新增 `reorder_N` 列
- 乱序数是 QoS 队列调度/多路径负载均衡问题的重要信号：正常单路径网络应为 0 或极小

### HTML 测试报告（v2.7.0）

停止时自动生成 `logs/qostool_report_<时间戳>.html`（自包含单文件），含测试信息、每流明细
（收发/丢包率/乱序/时延/抖动/阈值/判定徽标）与结论汇总；Web 页面"导出报告"按钮可随时手动生成；
CLI 停止时打印报告路径。CSV 日志同时追加每流 `avg_delay_ms_N`、`jitter_ms_N` 列。
报告中的丢包/丢包率与最终判定同口径：含停止瞬间尾部差（recv 角色不计，其 TX 来自远端轮询快照）。

- **时延分布直方图（v2.9.0）**：报告末尾附每流全程时延分布图（纯 CSS 柱状图，基于 p95/p99 同源的
  1ms 直方图，聚合为 ≤40 个显示桶），直观呈现时延集中区与长尾；≥2000ms 的溢出样本单独标注

### 吞吐量阶梯扫描（v2.7.0）

自动逐档加压，测出每条流"丢包率突破阈值"的极限速率：

```bash
qostool sweep -c flows.yaml -i eth0 [--step 20] [--hold 10] [--max-scale 10] [--loss-threshold 0.5] [--json]
```

- 所有流从配置速率按比例递增（默认每档 +20%），每档保持 hold 秒（含 0.5s 稳定期）
- 任一流丢包率超阈值（每流 `max_loss_rate_pct` 优先，否则 `--loss-threshold`）→ **同档再测一个 hold 确认**（防抖动误判）；确认仍越限即停止
- 输出：每档每流丢包率表 + 极限档位/每流极限速率 + 明细 CSV（`logs/qostool_sweep_<时间戳>.csv`，
  起始档越限时同样写出便于回看）
- `--json`（v2.9.0）：输出机器可读汇总（每档丢包率、每流 base/极限速率、生效阈值、`verdict` 与退出码一致），
  不打印过程表格；CI 中可用 `jq .limit_scale` 提取极限倍率
- **仅双向模式**（本机发送+抓包）；双机场景发送端无法得知远端丢包，v2.7.0 不支持

## 已知限制

- 发送仅支持 UDP；IPv6 扩展头不支持（本工具不产生）
- Windows 上抓环回（127.0.0.1）流量需安装并启用 **Npcap Loopback Adapter**（Npcap 安装时勾选 "Support loopback traffic"），
  且 `-i` 参数须用 `qostool lsdev` 列出的适配器名
- 丢包检测基于 seq 跳号 + 乱序容忍窗口（v2.8.0）：≤64 包的 seq 空洞先跟踪 200ms，乱序包补齐不计丢包，超时未补齐计丢包；更大的空洞直接计丢包；seq 回绕（约 12 小时 @100k pps）后不计丢包

## 测试

```bash
go test ./...
```
