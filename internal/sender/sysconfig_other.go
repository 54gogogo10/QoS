//go:build !windows

package sender

// EnsureDSCPSysConfig 非 Windows 平台无系统配置需要（IP_TOS/IPV6_TCLASS 直接生效）。
func EnsureDSCPSysConfig() func() {
	return func() {}
}
