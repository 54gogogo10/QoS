//go:build windows

package sender

import (
	"log"

	"golang.org/x/sys/windows"
)

// isElevated 检测当前进程是否管理员（提权）运行。
// Windows 设置 IP_TOS（DSCP）受权限限制：非管理员时 DSCP 可能被忽略（Win7 尤甚）。
// 令牌获取失败时按"未提权"处理并记录日志（进程权限本身受限时 DSCP 确实可能不生效）。
func isElevated() bool {
	t, err := windows.OpenCurrentProcessToken()
	if err != nil {
		log.Printf("警告: 无法获取进程令牌判断管理员权限: %v（按非管理员处理）", err)
		return false
	}
	defer t.Close()
	return t.IsElevated()
}
