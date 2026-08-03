//go:build !windows

package main

import "os/exec"

const hintNone = 0

// appWindow 是内嵌窗口的最小接口（非 Windows 平台无实现，始终返回 nil 走浏览器兜底）。
type appWindow interface {
	SetTitle(string)
	SetSize(w, h, hint int)
	Navigate(string)
	Run()
	Destroy()
}

// newWebView 非 Windows 平台暂不支持内嵌窗口，返回 nil（走浏览器兜底）。
func newWebView() appWindow {
	return nil
}

// openBrowser 用系统默认浏览器打开 URL。
func openBrowser(url string) {
	exec.Command("xdg-open", url).Start()
}
