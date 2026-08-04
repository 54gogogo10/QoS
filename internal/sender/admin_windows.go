//go:build windows

package sender

import "golang.org/x/sys/windows"

// isElevated 检测当前进程是否管理员（提权）运行。
// Windows 设置 IP_TOS（DSCP）受权限限制：非管理员时 DSCP 可能被忽略（Win7 尤甚）。
func isElevated() bool {
	t, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return false
	}
	defer t.Close()
	return t.IsElevated()
}
