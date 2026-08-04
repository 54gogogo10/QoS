//go:build !windows

package sender

// isElevated 非 Windows 平台无权限限制，恒为 true。
func isElevated() bool {
	return true
}
