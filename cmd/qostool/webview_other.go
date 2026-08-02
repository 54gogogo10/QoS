//go:build !windows

package main

import "os/exec"

const hintNone = 0

// newWebView 非 Windows 平台暂不支持内嵌窗口，返回 nil（走浏览器兜底）。
func newWebView() appWindow {
	return nil
}

// openBrowser 用系统默认浏览器打开 URL。
func openBrowser(url string) {
	exec.Command("xdg-open", url).Start()
}
