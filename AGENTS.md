# AGENTS.md — qostool 工作区说明

## 项目概览
qostool：跨平台（Windows/Linux）QoS 测试工具。发送 8 条不同 DSCP 优先级的 UDP 流，
接收端按 DSCP + 5 元组匹配，实时统计每条流的速率/丢包率/时延/抖动；终端表格 + Web 页面（默认 :16666）。
Go 单模块（module `qostool`）。README.md 是按版本号累积的完整功能文档，**改功能必须同步更新 README**。

## 常用命令
- 全部测试：`go test ./...`
- Linux 构建：`CGO_ENABLED=0 go build -o qostool ./cmd/qostool`（纯 Go 静态编译，AF_PACKET，无 libpcap）
- Windows 构建：需 MinGW gcc + Npcap SDK（解压到 `C:\WpdPack`），环境变量见 README「构建」节
- 全平台发布：`bash build.sh v2.8.x`——会**临时降级 go.mod/go.sum**（Win7 用 Go 1.20 + gopacket v1.2.0 + x/sys v0.13.0），脚本 trap 自动恢复；不要手动改动 go.mod 后中途打断该脚本

## 架构与依赖方向
`cmd/qostool`（CLI 入口 + WebView2 桌面模式；无参数双击即 app 模式）→ `internal/*`：
- `protocol`：UDP 载荷头 wire 格式（18 字节大端：magic "QOST" + flow_id + seq + send_ts），收发两端共用。**改动必须兼容旧版发送端**（sendTs==0 表示旧版，时延显示 `—`）
- `config`：YAML 解析 + DSCP 名称/值映射
- `sender`：发送与 DSCP 打标。平台文件：`qos2_windows`（qWave QOS2 API，失败回退 IP_TOS）、`dscp_unix`（IP_TOS/IPV6_TCLASS）、`sysconfig_windows`（注册表，管理员时自动改、退出恢复）、`timer_windows`
- `capture`：抓包 + 解析 + 匹配。Linux 走 AF_PACKET（`capture_linux.go`），Windows 走 gopacket/cgo。parse/match 必须对畸形包安全（有 fuzz 测试）
- `stats`：聚合器。丢包 = seq 空洞（乱序容忍：≤64 包跟踪 200ms）+ 尾部差；RFC3550 抖动；p95/p99 用 1ms 直方图；乱序包计数（v2.9.0：到达时 seq 已小于最大已见 seq，孔内重复不计）；`DelayHistogram()` 导出直方图快照供 HTML 报告分布图
- `controller`：生命周期编排（启停/CSV/HTML 报告/时钟偏差 hint）。Start/Stop/UpdateConfig 并发安全
- `sweep`：阶梯扫描引擎（仅 bidir 单机模式）
- `report`：HTML 报告 + 阈值判定。退出码契约：0=通过/无阈值，1=有流未通过或运行错误，2=用法错误
- `web`：HTTP API + `static/index.html` 单文件 UI（:16666，无认证）。新增控制类端点必须保持既有安全约束：Content-Type 必须为 JSON（防 CSRF）、请求体 1MB 上限、HTTP 读写超时（见 `security_test.go`）

## 平台兼容（关键约束）
- 平台相关代码用 build tag 拆分（`//go:build windows` / `!windows` / `linux`）；每个 windows 专有文件必须有 `_other`/`_unix` 对应实现，否则 Linux/Win7 编译失败
- **全仓库代码必须兼容 Go 1.20**：build.sh 构建 Win7 版时把 go.mod 降到 `go 1.20` 编译整个模块。禁用 1.21+ 的语言特性与标准库（`min`/`max` 内建、`slices`/`maps` 包、`for range int` 等）
- Windows DSCP 打标：QOS2 仅支持发往其他主机（环回/本机地址自动回退 IP_TOS）；CS6/CS7 等控制类值需管理员权限
- Win7：无 WebView2，仅 CLI 模式；抓包需 Npcap ≤1.7x 或 WinPcap

## 行为契约（改统计口径时必须保持）
- 最终判定与终端/Web/JSON 汇总同口径：丢包含停止瞬间尾部差（bidir 含、recv 不计）；send 模式不参与判定（正常完成退出 0）
- 乱序数与丢包/时延一样是累计值，各输出口径一致（Web 表格/终端汇总/HTML/JSON `reordered`/CSV `reorder_N`）
- 时延为"相对最小时延"（双机无时钟同步，按最小观测差值自动校正）；抖动不受时钟偏差影响
- `ip_len` 下限：IPv4 46 / IPv6 66（须容纳 18 字节载荷头，时延统计依赖它）
- 收发两端必须使用同一份配置；载荷内嵌 seq + 发送时间戳

## 约定
- 代码注释、README、Web UI 文案均为中文；提交信息格式 `feat(scope): 中文描述`（参考 git log）
- 构建产物与运行时文件已 gitignore：`dist/`、`logs/`（含各包测试生成的 HTML 报告）、`qostool*.exe`、`/config*.yaml`（app 模式自动落盘 exe 同目录）
- 敏感区域（统计口径、协议格式、Web API）改动前先读 `docs/superpowers/specs/` 下对应设计文档与相关包的 `*_test.go`
