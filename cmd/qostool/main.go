package main

import (
	"context"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"log"
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
			fmt.Fprintln(os.Stderr, "错误:", err)
			os.Exit(1)
		}
		return
	}
	if err := runCLI(mode, os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `qostool - QoS 测试工具

用法:
  双击 exe 或 qostool app                         桌面应用（内嵌窗口，页面内配置与启停）
  qostool send   -c flows.yaml [--web 16666] [-d 秒]  纯发送
  qostool recv   -c flows.yaml -i 接口 [--web 16666] [-d 秒]  纯接收
  qostool bidir  -c flows.yaml -i 接口 [--web 16666] [-d 秒]  双向发送+接收
  qostool sweep  -c flows.yaml -i 接口 [--step 20] [--hold 10] [--max-scale 10] [--loss-threshold 0.5]  阶梯扫描（双向，自动加压找极限）
  qostool lsdev                                  列出可用的抓包接口
`)
}

// runApp 桌面应用模式：内嵌 WebView2 窗口 + 本地 Web 服务（仅 127.0.0.1）。
// 三种角色（发送/接收/双向）各自独立的运行实例与配置文件，互不干扰。
func runApp() error {
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
	remote := fs.String("remote", "", "发送端地址 IP:port（接收端拉取其 TX 统计统一显示）")
	dur := fs.Int("d", 0, "运行秒数 (0=直到 Ctrl+C)")
	interval := fs.Int("interval", 100, "统计采样间隔毫秒")
	fs.Parse(args)

	if mode == "lsdev" {
		return listDevices()
	}
	if *cfgPath == "" {
		return fmt.Errorf("缺少 -c 配置文件")
	}
	if mode != "send" && *iface == "" {
		return fmt.Errorf("缺少 -i 监听接口")
	}
	if *interval < 10 {
		return fmt.Errorf("interval 不能小于 10ms")
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
	if err := ctrl.Start(*iface); err != nil {
		return err
	}

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
		go func() {
			if err := srv.ListenAndServe(":" + *webPort); err != nil && !errors.Is(err, http.ErrServerClosed) {
				fmt.Fprintf(os.Stderr, "Web 服务错误 (端口被占可换 --web 端口): %v\n", err)
			}
		}()
		// 发送端：启动前通知已连接的接收端监听并等待其就绪（防止漏收起始包）
		if ctrlMode == controller.ModeSend {
			srv.NotifyAllReceivers()
			time.Sleep(2 * time.Second)
		}
	}

	reportLoop(ctx, cfg, ctrl, time.Duration(*interval)*time.Millisecond)

	ctrl.Stop()
	if srv != nil {
		sc, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		srv.Shutdown(sc)
		cancel()
	}

	agg := ctrl.Aggregator()
	if agg != nil {
		snap := agg.Current(time.Now())
		txTotal, rxTotal, lost := agg.Totals()
		fmt.Println()
		fmt.Print(report.Summary(cfg, snap, txTotal, rxTotal, lost, true))
	}
	if p := ctrl.LastReportPath(); p != "" {
		fmt.Println("HTML 报告:", p)
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
	fs.Parse(args)

	if *cfgPath == "" {
		return fmt.Errorf("缺少 -c 配置文件")
	}
	if *iface == "" {
		return fmt.Errorf("缺少 -i 监听接口")
	}
	if *stepPct <= 0 || *stepPct >= 100 {
		return fmt.Errorf("step 必须在 0-100 之间")
	}
	if *hold < 2 {
		return fmt.Errorf("hold 不能小于 2 秒（含 0.5s settle）")
	}
	if *maxScale <= 1 {
		return fmt.Errorf("max-scale 必须大于 1")
	}
	if *lossTh <= 0 || *lossTh > 100 {
		return fmt.Errorf("loss-threshold 必须在 0-100")
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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

	// 汇总 CSV 到配置目录 logs/
	logDir := filepath.Join(filepath.Dir(*cfgPath), "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return err
	}
	csvPath := filepath.Join(logDir, fmt.Sprintf("qostool_sweep_%s.csv", time.Now().Format("20060102_150405")))
	f, err := os.Create(csvPath)
	if err != nil {
		return err
	}
	defer f.Close()
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
	fmt.Println("扫描明细 CSV:", csvPath)
	return nil
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
