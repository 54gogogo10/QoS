//go:build windows

package sender

import (
	"fmt"
	"net"
	"os"
	"syscall"
)

// Windows 的 Winsock 常量（ws2ipdef.h）。
const (
	ipProtoIP   = 0
	ipProtoIPv6 = 41
	ipTOS       = 3
	ipv6TCLASS  = 67
)

// setDSCP 通过 IP_TOS / IPV6_TCLASS 设置 DSCP（TOS 高 6 位）。
func setDSCP(conn *net.UDPConn, dscp int) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var serr error
	err = raw.Control(func(fd uintptr) {
		if conn.LocalAddr().(*net.UDPAddr).IP.To4() != nil {
			serr = syscall.SetsockoptInt(syscall.Handle(fd), ipProtoIP, ipTOS, dscp<<2)
			if serr != nil && errnoIs(serr, syscall.WSAEACCES) {
				// Windows 限制：CS6/CS7 等网络控制类 DSCP 需要管理员（提权）进程
				serr = fmt.Errorf("设置 DSCP=%d 被拒绝：Windows 对网络控制类 DSCP（如 CS6/CS7）要求管理员权限，请右键以管理员身份运行", dscp)
			}
		} else {
			// Windows 不支持 IPV6_TCLASS（WSAENOPROTOOPT）；IPV6_ECN 只能设 ECN 位，
			// 无法携带 DSCP，因此 IPv6 下直接报错，请使用 IPv4 或 Linux。
			serr = fmt.Errorf("Windows 不支持 IPv6 DSCP 标记（IPV6_TCLASS 不可用），请使用 IPv4 或 Linux")
		}
	})
	if err != nil {
		return err
	}
	return serr
}

// errnoIs 判断错误是否等于指定 errno（兼容 SyscallError 包装）。
func errnoIs(err error, target syscall.Errno) bool {
	for err != nil {
		if err == target {
			return true
		}
		if se, ok := err.(*os.SyscallError); ok {
			err = se.Err
			continue
		}
		if ee, ok := err.(syscall.Errno); ok {
			return ee == target
		}
		return false
	}
	return false
}
