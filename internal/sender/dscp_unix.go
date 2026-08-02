//go:build !windows

package sender

import (
	"net"
	"syscall"
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
			serr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_TOS, dscp<<2)
		} else {
			serr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IPV6, syscall.IPV6_TCLASS, dscp<<2)
		}
	})
	if err != nil {
		return err
	}
	return serr
}
