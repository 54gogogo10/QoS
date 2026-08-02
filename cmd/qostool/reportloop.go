package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"qostool/internal/config"
	"qostool/internal/controller"
	"qostool/internal/report"
)

// reportLoop 显示实时表格：TTY 下每秒清屏重绘，非 TTY 每 5 秒打印一行日志。
// 采样（100ms）由 controller 内部完成，这里只负责展示。
func reportLoop(ctx context.Context, cfg *config.Config, ctrl *controller.Controller, interval time.Duration) {
	isTTY := isTerminal(os.Stdout)
	next := time.Now()
	var lastDraw time.Time
	haveSender := ctrl.Mode() != controller.ModeRecv
	haveCapture := ctrl.Mode() != controller.ModeSend
	for {
		next = next.Add(interval)
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		agg := ctrl.Aggregator()
		if agg == nil {
			continue
		}
		if isTTY {
			if time.Since(lastDraw) >= time.Second {
				lastDraw = time.Now()
				fmt.Print("\x1b[2J\x1b[H") // 清屏 + 光标回原点
				fmt.Print(report.Table(cfg, agg.Current(time.Now()), haveSender, haveCapture))
			}
		} else if time.Since(lastDraw) >= 5*time.Second {
			lastDraw = time.Now()
			fmt.Println("=== " + time.Now().Format("2006-01-02 15:04:05") + " ===")
			fmt.Print(report.Table(cfg, agg.Current(time.Now()), haveSender, haveCapture))
		}
	}
}

// isTerminal 判断输出是否终端（决定是否清屏重绘）。
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
