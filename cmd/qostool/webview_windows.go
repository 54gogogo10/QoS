//go:build windows

package main

import (
	"os/exec"

	webview "github.com/jchv/go-webview2"
)

const hintNone = webview.HintNone

// appWindow 是内嵌窗口的最小接口（由 webview.WebView 接口实现）。
type appWindow interface {
	SetTitle(string)
	SetSize(w, h int, hint webview.Hint)
	Navigate(string)
	Run()
	Destroy()
}

// newWebView 创建内嵌 WebView2 窗口；运行时不可用时返回 nil。
func newWebView() appWindow {
	return webview.New(false)
}

// openBrowser 用系统默认浏览器打开 URL（WebView2 不可用时的兜底）。
func openBrowser(url string) {
	exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}
