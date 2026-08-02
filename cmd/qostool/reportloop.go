package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"qostool/internal/config"
	"qostool/internal/report"
	"qostool/internal/stats"
)

// reportLoop 每 interval 采样一次；TTY 下每秒重绘表格，非 TTY 每 5 秒打印一行日志。
func reportLoop(ctx context.Context, cfg *config.Config, agg *stats.Aggregator,
	haveSender, haveCapture bool, capErrCh chan error, interval time.Duration) {

	isTTY := isTerminal(os.Stdout)
	next := time.Now()
	var lastDraw time.Time
	for {
		next = next.Add(interval)
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		agg.Snapshot(time.Now())

		select {
		case err := <-capErrCh:
			if err != nil {
				fmt.Fprintln(os.Stderr, "警告: 抓包已停止:", err)
			}
			capErrCh = nil
		default:
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
