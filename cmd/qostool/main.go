package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gopacket/gopacket/pcap"

	"qostool/internal/capture"
	"qostool/internal/config"
	"qostool/internal/report"
	"qostool/internal/sender"
	"qostool/internal/stats"
	"qostool/internal/web"
)

const (
	defaultWebPort = "16666"
	histCap        = 3000
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	mode := os.Args[1]
	switch mode {
	case "send", "recv", "bidir", "lsdev":
	default:
		fmt.Fprintf(os.Stderr, "未知命令 %q\n", mode)
		usage()
		os.Exit(2)
	}
	if err := run(mode, os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `qostool - QoS 测试工具

用法:
  qostool send   -c flows.yaml [--web 16666] [-d 秒] [--interval 毫秒]  纯发送
  qostool recv   -c flows.yaml -i 接口 [--web 16666] [-d 秒]            纯接收
  qostool bidir  -c flows.yaml -i 接口 [--web 16666] [-d 秒]            双向发送+接收
  qostool lsdev                                                         列出可用的抓包接口
`)
}

func run(mode string, args []string) error {
	fs := flag.NewFlagSet(mode, flag.ExitOnError)
	cfgPath := fs.String("c", "", "配置文件路径 (yaml)")
	iface := fs.String("i", "", "监听接口 (recv/bidir 必填)")
	webPort := fs.String("web", defaultWebPort, "Web 端口 (填 off 关闭)")
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

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	agg := stats.NewAggregator(len(cfg.Flows), histCap)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *dur > 0 {
		ctx, stop = context.WithTimeout(ctx, time.Duration(*dur)*time.Second)
		defer stop()
	}

	haveSender := mode == "send" || mode == "bidir"
	haveCapture := mode == "recv" || mode == "bidir"

	if haveSender {
		s, err := sender.New(cfg, agg)
		if err != nil {
			return err
		}
		go s.Run(ctx)
	}

	var capErrCh chan error
	if haveCapture {
		c, err := capture.New(*iface, cfg, agg)
		if err != nil {
			return err
		}
		capErrCh = make(chan error, 1)
		go func() {
			capErrCh <- c.Run(ctx)
		}()
	}

	if *webPort != "off" {
		srv := web.New(cfg, agg)
		go func() {
			if err := srv.ListenAndServe(":" + *webPort); err != nil {
				fmt.Fprintf(os.Stderr, "Web 服务错误 (端口被占可换 --web 端口): %v\n", err)
			}
		}()
	}

	reportLoop(ctx, cfg, agg, haveSender, haveCapture, capErrCh, time.Duration(*interval)*time.Millisecond)

	snap := agg.Current(time.Now())
	txTotal, rxTotal, lost := agg.Totals()
	fmt.Println()
	fmt.Print(report.Summary(cfg, snap, txTotal, rxTotal, lost))
	return nil
}

// listDevices 打印 pcap 可用的接口（Windows 上是 GUID 名，帮助用户选 -i）。
func listDevices() error {
	devs, err := pcap.FindAllDevs()
	if err != nil {
		return err
	}
	for _, d := range devs {
		fmt.Printf("%s\t%s\n", d.Name, d.Description)
	}
	return nil
}
