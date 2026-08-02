//go:build windows

package sender

import (
	"net"
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
		} else {
			serr = syscall.SetsockoptInt(syscall.Handle(fd), ipProtoIPv6, ipv6TCLASS, dscp<<2)
		}
	})
	if err != nil {
		return err
	}
	return serr
}
