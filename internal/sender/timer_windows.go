//go:build windows

package sender

import "syscall"

var (
	winmm            = syscall.NewLazyDLL("winmm.dll")
	timeBeginPeriodP = winmm.NewProc("timeBeginPeriod")
	timeEndPeriodP   = winmm.NewProc("timeEndPeriod")
)

// init 把 Windows 定时器分辨率提升到 1ms。
// Windows 默认系统时钟分辨率约 15.6ms，会让 2ms 的发送节拍严重漂移；
// timeBeginPeriod(1) 是游戏/音频程序的标准做法，进程退出时自动恢复。
func init() {
	if timeBeginPeriodP != nil {
		timeBeginPeriodP.Call(1)
	}
}
