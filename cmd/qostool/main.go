package main

import (
	"context"
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
	"qostool/internal/web"
)

const (
	defaultWebAddr = "127.0.0.1:16666"
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
	case "send", "recv", "bidir", "lsdev", "app":
	default:
		fmt.Fprintf(os.Stderr, "未知命令 %q\n", mode)
		usage()
		os.Exit(2)
	}
	if mode == "app" {
		if err := runApp(); err != nil {
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
  qostool lsdev                                  列出可用的抓包接口
`)
}

// runApp 桌面应用模式：内嵌 WebView2 窗口 + 本地 Web 服务（仅 127.0.0.1）。
func runApp() error {
	cfgPath, err := resolveConfigPath()
	if err != nil {
		return err
	}
	// windowsgui 模式无控制台：把日志写入 exe 同目录 qostool.log
	logFile, err := os.OpenFile(filepath.Join(filepath.Dir(cfgPath), "qostool.log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err == nil {
		log.SetOutput(logFile)
		defer logFile.Close()
	}
	log.Printf("qostool app 启动, 配置: %s", cfgPath)

	cfg, err := loadOrCreateConfig(cfgPath)
	if err != nil {
		return err
	}

	ctrl := controller.New(cfg, controller.ModeBidir)
	ctrl.SetLogDir(filepath.Dir(cfgPath))
	srv := web.New(ctrl, cfgPath, true)
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
		openBrowser("http://" + defaultWebAddr)
		<-webErr // 等待 Web 服务退出（Ctrl+C）
		ctrl.Stop()
		return nil
	}
	defer w.Destroy()

	w.SetTitle("qostool - QoS 测试工具")
	w.SetSize(1280, 820, hintNone)
	w.Navigate("http://" + defaultWebAddr)
	w.Run()

	ctrl.Stop()
	sc, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	srv.Shutdown(sc)
	cancel()
	return nil
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
		srv = web.New(ctrl, "", false) // CLI 模式下页面只读
		if *remote != "" {
			if !strings.Contains(*remote, ":") {
				*remote += ":16666"
			}
			srv.SetRemote(*remote)
		}
		go func() {
			if err := srv.ListenAndServe(":" + *webPort); err != nil && !errors.Is(err, http.ErrServerClosed) {
				fmt.Fprintf(os.Stderr, "Web 服务错误 (端口被占可换 --web 端口): %v\n", err)
			}
		}()
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
		fmt.Print(report.Summary(cfg, snap, txTotal, rxTotal, lost))
	}
	return nil
}

// listDevices 打印可用的抓包接口（平台实现：Windows=Npcap / Linux=net.Interfaces）。
func listDevices() error {
	return listDevicesPlatform()
}

// resolveConfigPath 返回配置文件路径：exe 同目录 config.yaml，
// 无写权限时退回用户主目录。
func resolveConfigPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(exe)
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte{}, 0o644); err == nil {
		os.Remove(p)
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("无法确定配置文件位置: %w", err)
	}
	hd := filepath.Join(home, ".qostool")
	if err := os.MkdirAll(hd, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(hd, "config.yaml"), nil
}

// loadOrCreateConfig 加载配置文件；不存在或非法时用内置默认配置并落盘。
func loadOrCreateConfig(path string) (*config.Config, error) {
	if _, err := os.Stat(path); err == nil {
		cfg, err := config.Load(path)
		if err == nil {
			return cfg, nil
		}
		fmt.Fprintln(os.Stderr, "警告: 配置文件无效，使用内置默认配置:", err)
	}
	cfg := config.DefaultConfig()
	if err := cfg.Save(path); err != nil {
		return nil, fmt.Errorf("写入默认配置 %s 失败: %w", path, err)
	}
	return cfg, nil
}
