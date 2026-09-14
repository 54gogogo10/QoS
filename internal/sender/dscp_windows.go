//go:build windows

package sender

import (
	"fmt"
	"log"
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

// setDSCP 为 UDP socket 设置 DSCP（TOS 高 6 位），返回 QoS2 流句柄（nil=未使用）。
//
// v2.8.0 起优先使用 QoS2 (qWave) 打标：Windows 7/8/10 上纯 setsockopt(IP_TOS)
// 的标记会被系统剥掉（发出的包 DSCP=0），QoS2 是官方支持的应用打标路径；
// QoS2 失败（如权限不足、系统不支持）时自动回退 IP_TOS（Win11 与组策略
// 放行环境下仍有效），保持原有行为与错误提示。
func setDSCP(conn *net.UDPConn, dscp int) (*qosFlow, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return nil, err
	}
	var qf *qosFlow
	var serr error
	err = raw.Control(func(fd uintptr) {
		if conn.LocalAddr().(*net.UDPAddr).IP.To4() == nil {
			// Windows 不支持 IPV6_TCLASS（WSAENOPROTOOPT）；IPV6_ECN 只能设 ECN 位，
			// 无法携带 DSCP，因此 IPv6 下直接报错，请使用 IPv4 或 Linux。
			serr = fmt.Errorf("Windows 不支持 IPv6 DSCP 标记（IPV6_TCLASS 不可用），请使用 IPv4 或 Linux")
			return
		}
		h := syscall.Handle(fd)
		if dscp > 0 && !isLoopbackDest(conn.RemoteAddr()) {
			// QoS2 优先：系统按流打标，不依赖 IP_TOS 是否被信任。
			// 注意 qWave 只支持发往其他主机的流（环回/本机地址会 Element not found），
			// 单机自测场景直接走 IP_TOS。
			var err2 error
			qf, err2 = setupQoS2(h, dscp)
			if err2 != nil {
				log.Printf("警告: QoS2 (qWave) 打标失败，回退 IP_TOS: %v", err2)
			}
		}
		// IP_TOS 兜底：QoS2 成功时失败仅警告（标记已由 QoS 流接管）；
		// QoS2 失败时保持原有报错行为（网络控制类 DSCP 需管理员）。
		if err2 := syscall.SetsockoptInt(h, ipProtoIP, ipTOS, dscp<<2); err2 != nil {
			if qf != nil {
				log.Printf("警告: IP_TOS 设置失败（QoS2 已接管 DSCP 标记）: %v", err2)
			} else if errnoIs(err2, syscall.WSAEACCES) {
				serr = fmt.Errorf("设置 DSCP=%d 被拒绝：Windows 对网络控制类 DSCP（如 CS6/CS7）要求管理员权限，请右键以管理员身份运行", dscp)
			} else {
				serr = err2
			}
		}
	})
	if err != nil {
		if qf != nil {
			qf.close()
		}
		return nil, err
	}
	if serr != nil {
		if qf != nil {
			qf.close()
		}
		return nil, serr
	}
	return qf, nil
}

// isLoopbackDest 判断 UDP 目标是否为环回地址（qWave 不支持环回流）。
func isLoopbackDest(addr net.Addr) bool {
	if ua, ok := addr.(*net.UDPAddr); ok && ua.IP != nil {
		return ua.IP.IsLoopback()
	}
	return false
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
