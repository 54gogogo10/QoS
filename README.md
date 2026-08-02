# qostool — QoS 测试工具

跨平台（Windows / Linux）QoS 流量生成与测量工具：发送 8 条不同 DSCP 优先级的 UDP 流，
接收端按 DSCP + 5 元组精确匹配，实时统计每条流的收发速率与丢包率。
提供终端实时表格与 Web 曲线页面（默认端口 16666）。

## 两种使用方式

**桌面应用（推荐）**：双击 `qostool-app.exe` → 弹出内嵌窗口（无需浏览器），
页面内可选择监听接口、编辑 8 条流的配置（5 元组 / DSCP / 速率）、开始/停止测试并实时查看曲线。
配置自动保存到 exe 同目录的 `config.yaml`。需要 Windows 10/11 自带的 WebView2 运行时。

> ⚠️ **Windows 上请以管理员身份运行**（右键 → 以管理员身份运行）：
> 操作系统限制普通用户设置网络控制类 DSCP（CS6=48、CS7=56 等），
> 普通权限下含这些值的流会启动失败（iperf3 等工具同样受限）。

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
```

通用选项：`--web 16666`（Web 端口，`--web off` 关闭）、`-d 秒数`（限时运行）、`--interval 毫秒`（统计采样间隔，默认 100）。

Web 页面：浏览器打开 `http://<本机IP>:16666`，实时曲线 + 表格，丢包率非零的行标红。

## 配置

见 `configs/example.yaml`。每条流：5 元组（src/dst IP、端口、udp 协议）、`dscp`（0-63 或名字如 EF/AF41/CS7）、
`rate_mbps` 与 `rate_pps`（双参数限速，至少填一个，取先到者）、`ip_len`（IP 包总长，含 IP/UDP 头，IPv4 最小 38、IPv6 最小 58）。

收发两端必须使用同一份配置；发送载荷内嵌序列号，接收端据此计算丢包率。

## 已知限制

- 发送仅支持 UDP；IPv6 扩展头不支持（本工具不产生）
- Windows 上抓环回（127.0.0.1）流量需安装并启用 **Npcap Loopback Adapter**（Npcap 安装时勾选 "Support loopback traffic"），
  且 `-i` 参数须用 `qostool lsdev` 列出的适配器名
- 丢包检测基于 seq 跳号，不处理乱序重排；seq 回绕（约 12 小时 @100k pps）后不计丢包

## 测试

```bash
go test ./...
```
