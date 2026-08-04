# qostool — QoS 测试工具

跨平台（Windows / Linux）QoS 流量生成与测量工具：发送 8 条不同 DSCP 优先级的 UDP 流，
接收端按 DSCP + 5 元组精确匹配，实时统计每条流的收发速率与丢包率。
提供终端实时表格与 Web 曲线页面（默认端口 16666）。

## 一键构建

```bash
bash build.sh v2.6.0   # 一次产出全部平台：Win10/11 app+CLI、Linux amd64/arm64、Win7
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

- Web 服务（16666）监听所有接口且无认证：**内网中任何能访问该端口的主机都可控制测试**（启停/改配置）。
  请在可信网络中使用；如需限制，用防火墙仅放行指定机器的 16666 端口
- 已内置防护：控制类 API 校验 Content-Type 为 JSON（防跨站表单伪造 CSRF）、请求体 1MB 上限、
  HTTP 读写超时、包解析对畸形包安全（随机模糊测试通过）
- 接收端/发送端地址均为用户手动配置，工具不会主动向外发起连接（仅按配置拉取/推送）

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
