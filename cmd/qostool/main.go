package main

import (
	"context"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"qostool/internal/config"
	"qostool/internal/controller"
	"qostool/internal/report"
	"qostool/internal/sender"
	"qostool/internal/sweep"
	"qostool/internal/web"
)

const (
	defaultWebAddr = ":16666"        // 监听所有接口（跨机器访问/远端同步需要）
	localhostURL  = "http://127.0.0.1:16666"
	histCap        = 3000
)

func main() {
	// 无参数（如双击 exe）进入桌面 App 模式
	if len(os.Args) < 2 {
		if err := runApp(); err != nil {
			fmt.Fprintln(os.Stderr, "错误:", err)
			os.Exit(1)
		}
		return
	}
	mode := os.Args[1]
	switch mode {
	case "send", "recv", "bidir", "lsdev", "app", "sweep":
	default:
		fmt.Fprintf(os.Stderr, "未知命令 %q\n", mode)
		usage()
		os.Exit(2)
	}
	if mode == "sweep" {
		if err := runSweep(os.Args[2:]); err != nil {
			exitOnError(err)
		}
		return
	}
	if err := runCLI(mode, os.Args[2:]); err != nil {
		exitOnError(err)
	}
}

// testFailedError 表示测试完成但有流未通过阈值（v2.8.0 退出码契约）：
// 退出码 0=全部通过/未配置阈值，1=有流未通过（或运行错误），2=用法错误。
type testFailedError struct {
	flows []string
}

func (e *testFailedError) Error() string {
	return "测试未通过: " + strings.Join(e.flows, "、")
}

// usageError 用法错误（v2.9.0 审计修复）：缺参数/参数非法按契约退出码 2
//（此前误用 1，与 README/usage 声明的 "2=用法错误" 不符）。
type usageError struct {
	msg string
}

func (e *usageError) Error() string {
	return e.msg
}

// exitOnError 打印错误并退出：2=用法错误，1=测试未通过与运行错误。
func exitOnError(err error) {
	var tf *testFailedError
	if errors.As(err, &tf) {
		fmt.Fprintln(os.Stderr, tf.Error())
		os.Exit(1)
	}
	var ue *usageError
	if errors.As(err, &ue) {
		fmt.Fprintln(os.Stderr, "用法错误:", ue.msg)
		os.Exit(2)
	}
	fmt.Fprintln(os.Stderr, "错误:", err)
	os.Exit(1)
}

func usage() {
	fmt.Fprint(os.Stderr, `qostool - QoS 测试工具

用法:
  双击 exe 或 qostool app                         桌面应用（内嵌窗口，页面内配置与启停）
  qostool send   -c flows.yaml [--web 16666] [--bind 127.0.0.1] [-d 秒] [--json]  纯发送
  qostool recv   -c flows.yaml -i 接口 [--web 16666] [--bind 127.0.0.1] [-d 秒] [--json]  纯接收
  qostool bidir  -c flows.yaml -i 接口 [--web 16666] [--bind 127.0.0.1] [-d 秒] [--json]  双向发送+接收
  qostool sweep  -c flows.yaml -i 接口 [--step 20] [--hold 10] [--max-scale 10] [--loss-threshold 0.5] [--json]  阶梯扫描（双向，自动加压找极限）
  qostool lsdev                                  列出可用的抓包接口

退出码 (v2.8.0): 0=全部通过或未配置阈值, 1=有流未通过阈值（或运行错误）, 2=用法错误
--json: 只输出机器可读 JSON 汇总（含每流判定），不打印实时表格与文本汇总；
        send 模式无接收数据不判定，verdict 为 none
--bind: 限制 Web 服务监听地址（默认所有接口）。单机自测建议 127.0.0.1，
        控制 API 无认证，绑定回环可避免内网其他主机控制测试（v2.9.0）
--no-sysconfig: 管理员运行时默认自动放行系统对应用 DSCP 标记的限制
        （DisableUserTOSSetting=0 + Do not use NLA=1，退出自动恢复），此开关禁用
`)
}

// runApp 桌面应用模式：内嵌 WebView2 窗口 + 本地 Web 服务（仅 127.0.0.1）。
// 三种角色（发送/接收/双向）各自独立的运行实例与配置文件，互不干扰。
func runApp() error {
	// v2.8.0：管理员时自动放行系统对应用 DSCP 标记的限制（退出自动恢复）
	defer sender.EnsureDSCPSysConfig()()

	cfgDir, err := resolveConfigDir()
	if err != nil {
		return err
	}
	// windowsgui 模式无控制台：把日志写入 exe 同目录 qostool.log
	logFile, err := os.OpenFile(filepath.Join(cfgDir, "qostool.log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err == nil {
		log.SetOutput(logFile)
		defer logFile.Close()
	}
	log.Printf("qostool app 启动, 配置目录: %s", cfgDir)

	// 三个角色独立实例 + 独立配置文件
	ctrls := map[controller.Mode]*controller.Controller{}
	cfgPaths := map[controller.Mode]string{}
	for _, m := range []controller.Mode{controller.ModeSend, controller.ModeRecv, controller.ModeBidir} {
		p := filepath.Join(cfgDir, cfgNameForMode(m))
		cfgPaths[m] = p
		cfg, err := loadOrCreateConfig(p, m)
		if err != nil {
			return err
		}
		ctrl := controller.New(cfg, m)
		ctrl.SetLogDir(cfgDir)
		ctrls[m] = ctrl
	}
	srv := web.New(ctrls, cfgPaths, true)
	// recv 模式日志/汇总的 TX 列使用远端发送端数据（而非本端 0）
	if ctrl := ctrls[controller.ModeRecv]; ctrl != nil {
		ctrl.SetRemoteTXProvider(srv.RemoteTXProvider)
	}
	webErr := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(defaultWebAddr); err != nil && !errors.Is(err, http.ErrServerClosed) {
			webErr <- err
		}
	}()

	w := newWebView()
	if w == nil {
		// WebView2 运行时不可用：兜底用系统默认浏览器打开页面
		fmt.Fprintln(os.Stderr, "警告: WebView2 运行时不可用，改用系统浏览器打开")
		openBrowser(localhostURL)
		<-webErr // 等待 Web 服务退出（Ctrl+C）
		for _, c := range ctrls {
			c.Stop()
		}
		return nil
	}
	defer w.Destroy()

	w.SetTitle("qostool - QoS 测试工具")
	w.SetSize(1280, 820, hintNone)
	w.Navigate(localhostURL)
	w.Run()

	for _, c := range ctrls {
		c.Stop()
	}
	// v2.8.1：发送端退出时同步通知接收端停止监听（排空在途包）
	srv.NotifyAllReceiversStop()
	sc, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	srv.Shutdown(sc)
	cancel()
	return nil
}

// cfgNameForMode 每种角色的独立配置文件。
func cfgNameForMode(m controller.Mode) string {
	switch m {
	case controller.ModeSend:
		return "config-send.yaml"
	case controller.ModeRecv:
		return "config-recv.yaml"
	default:
		return "config.yaml"
	}
}

// runCLI 命令行模式：send/recv/bidir。
func runCLI(mode string, args []string) error {
	fs := flag.NewFlagSet(mode, flag.ExitOnError)
	cfgPath := fs.String("c", "", "配置文件路径 (yaml)")
	iface := fs.String("i", "", "监听接口 (recv/bidir 必填)")
	webPort := fs.String("web", "16666", "Web 端口 (填 off 关闭)")
	bind := fs.String("bind", "", "Web 监听地址 (默认所有接口；填 127.0.0.1 仅本机可访问，v2.9.0)")
	remote := fs.String("remote", "", "发送端地址 IP:port（接收端拉取其 TX 统计统一显示）")
	dur := fs.Int("d", 0, "运行秒数 (0=直到 Ctrl+C)")
	interval := fs.Int("interval", 100, "统计采样间隔毫秒")
	jsonOut := fs.Bool("json", false, "只输出 JSON 汇总（退出码 0=通过 1=未通过）")
	noSysConfig := fs.Bool("no-sysconfig", false, "不自动修改系统 DSCP 注册表设置（管理员时默认自动）")
	fs.Parse(args)

	if mode == "lsdev" {
		return listDevices()
	}
	if *cfgPath == "" {
		return &usageError{msg: "缺少 -c 配置文件"}
	}
	if mode != "send" && *iface == "" {
		return &usageError{msg: "缺少 -i 监听接口"}
	}
	if *interval < 10 {
		return &usageError{msg: "interval 不能小于 10ms"}
	}
	// --bind 安全限制（v2.9.0）：只接受 IP 或 localhost，防止拼出任意 URL
	bindHost := ""
	if *bind != "" {
		if *bind == "localhost" {
			bindHost = "localhost"
		} else if ip := net.ParseIP(*bind); ip != nil {
			bindHost = ip.String()
		} else {
			return &usageError{msg: fmt.Sprintf("--bind 必须是 IP 地址或 localhost（当前 %q）", *bind)}
		}
	}

	var ctrlMode controller.Mode
	switch mode {
	case "send":
		ctrlMode = controller.ModeSend
	case "recv":
		ctrlMode = controller.ModeRecv
	default:
		ctrlMode = controller.ModeBidir
	}

	// 接收端只监听：不要求速率/包长参数
	var cfg *config.Config
	var err error
	if ctrlMode == controller.ModeRecv {
		cfg, err = config.LoadRecv(*cfgPath)
	} else {
		cfg, err = config.Load(*cfgPath)
	}
	if err != nil {
		return err
	}

	ctrl := controller.New(cfg, ctrlMode)
	ctrl.SetLogDir(filepath.Dir(*cfgPath))
	// v2.8.0：管理员时自动放行系统对应用 DSCP 标记的限制（退出自动恢复）
	restoreSys := func() {}
	if !*noSysConfig {
		restoreSys = sender.EnsureDSCPSysConfig()
		defer restoreSys()
	}
	var startedAt time.Time // Start 后取 controller 实际开始时刻（前面可能有时钟样本等待）

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *dur > 0 {
		ctx, stop = context.WithTimeout(ctx, time.Duration(*dur)*time.Second)
		defer stop()
	}

	var srv *web.Server
	if *webPort != "off" {
		srv = web.New(map[controller.Mode]*controller.Controller{ctrlMode: ctrl},
			map[controller.Mode]string{ctrlMode: *cfgPath}, false) // CLI 模式下页面只读
		if *remote != "" {
			if !strings.Contains(*remote, ":") {
				*remote += ":16666"
			}
			srv.SetRemote(*remote)
		}
		if ctrlMode == controller.ModeRecv {
			ctrl.SetRemoteTXProvider(srv.RemoteTXProvider)
		}
		// v2.9.0：--bind 限制监听地址（默认所有接口保持旧行为；127.0.0.1 时控制面仅本机可达）
		listenAddr := ":" + *webPort
		if bindHost != "" {
			listenAddr = net.JoinHostPort(bindHost, *webPort)
		}
		go func() {
			if err := srv.ListenAndServe(listenAddr); err != nil && !errors.Is(err, http.ErrServerClosed) {
				fmt.Fprintf(os.Stderr, "Web 服务错误 (端口被占可换 --web 端口): %v\n", err)
			}
		}()
		// 发送端：启动前通知已连接的接收端监听并等待其就绪（防止漏收起始包）
		if ctrlMode == controller.ModeSend {
			srv.NotifyAllReceivers()
			time.Sleep(2 * time.Second)
		}
	}

	// v2.8.1：recv 模式先等控制通道时钟样本（NTP 风格四时间戳），
	// 注入时延校正基准，再启动测试（流量未开始时即已知两机时钟差）
	if ctrlMode == controller.ModeRecv && *remote != "" && srv != nil {
		// 轮询启动即测一次（0s 第 1 样本，1s 第 2 样本），等 2s 足够拿到 2 个样本
		for i := 0; i < 20 && !hasClockSample(srv); i++ {
			time.Sleep(100 * time.Millisecond)
		}
		if offNs, rttNs, ok := srv.ClockOffsetNs(); ok {
			// NTP 估计值为"发送端−接收端"时钟差，聚合器 hint 基准为
			// "接收端−发送端"（rawD = 时延 + 接收端−发送端），故取负注入
			ctrl.SetClockOffsetHint(-offNs)
			log.Printf("控制通道时钟偏差: %.1f ms（接收端−发送端，RTT %.2f ms），已注入时延校正基准", float64(-offNs)/1e6, float64(rttNs)/1e6)
		} else {
			log.Printf("警告: 控制通道时钟测量暂无样本（发送端 %s 未响应？），时延校正将由测试流自动收敛", *remote)
		}
	}

	if err := ctrl.Start(*iface); err != nil {
		return err
	}
	startedAt = ctrl.Status().Started

	if !*jsonOut {
		reportLoop(ctx, cfg, ctrl, time.Duration(*interval)*time.Millisecond)
	} else {
		<-ctx.Done() // JSON 模式：不打印实时表格，等待结束
	}

	ctrl.Stop()
	if srv != nil {
		// v2.8.1：CLI 发送端退出时同步通知接收端停止监听（接收端延迟排空在途包）
		if ctrlMode == controller.ModeSend {
			srv.NotifyAllReceiversStop()
		}
		sc, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		srv.Shutdown(sc)
		cancel()
	}

	agg := ctrl.Aggregator()
	if agg == nil {
		return nil
	}
	snap := agg.Current(time.Now())
	// 丢包口径与文本汇总/报告一致：recv 模式 TX 来自远端轮询（滞后最多 1s），不计尾部差
	withTailDiff := ctrlMode != controller.ModeRecv
	// 判定：send 模式无接收数据，不参与阈值判定（避免“只发不收=100% 丢包”误判）
	var vs []report.Verdict
	if ctrlMode != controller.ModeSend {
		vs = report.Verdicts(cfg, snap, true)
	}
	if *jsonOut {
		b, err := report.JSONSummary(cfg, snap, vs, report.ReportMeta{
			Started: startedAt, Stopped: time.Now(), Iface: *iface, Mode: string(ctrlMode), Version: report.Version,
		}, withTailDiff, ctrl.LastReportPath())
		if err != nil {
			return err
		}
		fmt.Println(string(b))
	} else {
		txTotal, rxTotal, lost := agg.Totals()
		fmt.Println()
		fmt.Print(report.Summary(cfg, snap, txTotal, rxTotal, lost, withTailDiff))
		// v2.8.1 控制通道时钟偏差（与测试流 min 法互相印证）
		if ctrlMode == controller.ModeRecv && srv != nil {
			if offNs, rttNs, ok := srv.ClockOffsetNs(); ok {
				fmt.Printf("控制通道时钟偏差: %.1f ms（RTT %.2f ms，%d 样本）\n", float64(offNs)/1e6, float64(rttNs)/1e6, srv.ClockSamples())
			}
		}
		if p := ctrl.LastReportPath(); p != "" {
			fmt.Println("HTML 报告:", p)
		}
	}
	// 阈值判定 → 退出码 1（v2.8.0）：脚本/CI 可按退出码判断测试结果
	var failed []string
	for i, v := range vs {
		if v.Checked && !v.Pass {
			failed = append(failed, cfg.Flows[i].Name)
		}
	}
	if len(failed) > 0 {
		return &testFailedError{flows: failed}
	}
	return nil
}

// runSweep 吞吐量阶梯扫描：逐档加压找每流极限速率（仅双向，需本机抓包）。
func runSweep(args []string) error {
	fs := flag.NewFlagSet("sweep", flag.ExitOnError)
	cfgPath := fs.String("c", "", "配置文件路径 (yaml)")
	iface := fs.String("i", "", "监听接口")
	stepPct := fs.Float64("step", 20, "每档递增百分比")
	hold := fs.Int("hold", 10, "每档保持秒数")
	maxScale := fs.Float64("max-scale", 10, "最大倍率")
	lossTh := fs.Float64("loss-threshold", 0.5, "丢包率阈值 %（被每流 max_loss_rate_pct 覆盖）")
	jsonOut := fs.Bool("json", false, "只输出 JSON 汇总（不打印过程表格）")
	noSysConfig := fs.Bool("no-sysconfig", false, "不自动修改系统 DSCP 注册表设置（管理员时默认自动）")
	fs.Parse(args)

	if *cfgPath == "" {
		return &usageError{msg: "缺少 -c 配置文件"}
	}
	if *iface == "" {
		return &usageError{msg: "缺少 -i 监听接口"}
	}
	if *stepPct <= 0 || *stepPct >= 100 {
		return &usageError{msg: "step 必须在 0-100 之间"}
	}
	if *hold < 2 {
		return &usageError{msg: "hold 不能小于 2 秒（含 0.5s settle）"}
	}
	if *maxScale <= 1 {
		return &usageError{msg: "max-scale 必须大于 1"}
	}
	if *lossTh <= 0 || *lossTh > 100 {
		return &usageError{msg: "loss-threshold 必须在 0-100"}
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// v2.8.0：管理员时自动放行系统对应用 DSCP 标记的限制（退出自动恢复）
	if !*noSysConfig {
		defer sender.EnsureDSCPSysConfig()()
	}

	fmt.Printf("=== 阶梯扫描：起始速率按 %.0f%% 递增，每档保持 %ds，丢包阈值 %.2f%% ===\n", *stepPct, *hold, *lossTh)
	res, err := sweep.Run(ctx, cfg, *iface, sweep.Options{
		StepPct: *stepPct, Hold: time.Duration(*hold) * time.Second,
		MaxScale: *maxScale, LossThresholdPct: *lossTh,
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	if res == nil {
		return nil // Ctrl+C 中断
	}

	// 汇总 CSV 到配置目录 logs/（越限停止时也写，便于回看越限档明细）
	csvPath := ""
	logDir := filepath.Join(filepath.Dir(*cfgPath), "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return err
	}
	csvPath = filepath.Join(logDir, fmt.Sprintf("qostool_sweep_%s.csv", time.Now().Format("20060102_150405")))
	f, err := os.Create(csvPath)
	if err != nil {
		return err
	}
	writeSweepCSV(f, cfg, res)
	f.Close()

	if *jsonOut {
		b, err := sweep.JSONSummary(cfg, res, sweep.Options{
			StepPct: *stepPct, Hold: time.Duration(*hold) * time.Second,
			MaxScale: *maxScale, LossThresholdPct: *lossTh,
		}, report.Version, csvPath)
		if err != nil {
			return err
		}
		fmt.Println(string(b))
	} else {
		// 逐步打印结果表
		var b strings.Builder
		b.WriteString("档位   倍率   ")
		for _, f := range cfg.Flows {
			b.WriteString(fmt.Sprintf("%-12s", f.Name))
		}
		b.WriteString("\n")
		for i, st := range res.Steps {
			b.WriteString(fmt.Sprintf("%-6d %-6.2f ", i+1, st.Scale))
			for _, l := range st.LossPct {
				if l < 0 {
					b.WriteString(fmt.Sprintf("%-12s", "无数据"))
				} else {
					b.WriteString(fmt.Sprintf("%-12s", fmt.Sprintf("%.2f%%", l)))
				}
			}
			b.WriteString("\n")
		}
		fmt.Print(b.String())

		// 结论
		if res.LimitScale <= 0 {
			fmt.Println("结论: 起始档（1.0x）即越限，极限速率低于配置的 base 速率")
		} else {
			fmt.Printf("结论: 极限档位 = %.2fx（最后一档全部通过）\n", res.LimitScale)
			for i, f := range cfg.Flows {
				if f.RateMbps > 0 {
					fmt.Printf("  %-12s 极限 %.2f Mbps\n", f.Name, res.LimitMbps[i])
				} else {
					fmt.Printf("  %-12s 极限 %.0f pps\n", f.Name, f.RatePPS*res.LimitScale)
				}
			}
		}
		fmt.Println("结束原因:", res.DoneReason)
		fmt.Println("扫描明细 CSV:", csvPath)
	}

	// v2.8.0 退出码：起始档即越限 → 极限速率低于配置 → 退出码 1（CI 可据此失败）
	if res.LimitScale <= 0 {
		return &testFailedError{flows: []string{"起始档（1.0x）即越限，极限速率低于配置的 base 速率"}}
	}
	return nil
}

// writeSweepCSV 写出扫描明细 CSV（档位/倍率/每流丢包率 + 极限倍率行）。
func writeSweepCSV(f *os.File, cfg *config.Config, res *sweep.Result) {
	w := csv.NewWriter(f)
	rec := []string{"档位", "倍率"}
	for _, fl := range cfg.Flows {
		rec = append(rec, fl.Name+" 丢包%")
	}
	w.Write(rec)
	for i, st := range res.Steps {
		rec := []string{fmt.Sprintf("%d", i+1), fmt.Sprintf("%.2f", st.Scale)}
		for _, l := range st.LossPct {
			rec = append(rec, fmt.Sprintf("%.3f", l))
		}
		w.Write(rec)
	}
	rec = []string{"极限倍率", fmt.Sprintf("%.2f", res.LimitScale)}
	w.Write(rec)
	w.Flush()
}

// hasClockSample 控制通道时钟测量是否已有可用样本。
func hasClockSample(srv *web.Server) bool {
	_, _, ok := srv.ClockOffsetNs()
	return ok
}

// listDevices 打印可用的抓包接口（平台实现：Windows=Npcap / Linux=net.Interfaces）。
func listDevices() error {
	return listDevicesPlatform()
}

// resolveConfigDir 返回配置目录：exe 同目录，无写权限时退回用户主目录 ~/.qostool。
func resolveConfigDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(exe)
	p := filepath.Join(dir, ".qostool-write-test")
	if err := os.WriteFile(p, []byte{}, 0o644); err == nil {
		os.Remove(p)
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("无法确定配置目录: %w", err)
	}
	hd := filepath.Join(home, ".qostool")
	if err := os.MkdirAll(hd, 0o755); err != nil {
		return "", err
	}
	return hd, nil
}

// loadOrCreateConfig 按角色加载配置文件；不存在或非法时用内置默认配置并落盘。
// 接收端角色使用宽松校验（无需速率/包长），默认配置为无速率版本。
func loadOrCreateConfig(path string, m controller.Mode) (*config.Config, error) {
	if _, err := os.Stat(path); err == nil {
		var cfg *config.Config
		var err error
		if m == controller.ModeRecv {
			cfg, err = config.LoadRecv(path)
		} else {
			cfg, err = config.Load(path)
		}
		if err == nil {
			return cfg, nil
		}
		fmt.Fprintln(os.Stderr, "警告: 配置文件无效，使用内置默认配置:", err)
	}
	var cfg *config.Config
	if m == controller.ModeRecv {
		cfg = config.DefaultConfig()
		for i := range cfg.Flows {
			cfg.Flows[i].RateMbps = 0
			cfg.Flows[i].RatePPS = 0
			cfg.Flows[i].IPLen = 0
		}
	} else {
		cfg = config.DefaultConfig()
	}
	if err := cfg.Save(path); err != nil {
		return nil, fmt.Errorf("写入默认配置 %s 失败: %w", path, err)
	}
	return cfg, nil
}
