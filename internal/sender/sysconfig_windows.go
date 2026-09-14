//go:build windows

package sender

import (
	"log"

	"golang.org/x/sys/windows/registry"
)

// EnsureDSCPSysConfig 管理员运行时自动放行应用 DSCP 标记（v2.8.0）。
//
// 为什么不做"自动设置组策略"：
//  1. DSCP 标记覆盖（DSCP Marking Override）存在 registry.pol 二进制策略文件中，
//     直接写注册表无效（策略刷新时被覆盖回滚），程序化修改风险高；
//  2. Policy-based QoS 策略（netsh/PowerShell 创建）实测在 Win11 上不生效、
//     Win10 上社区大量失败案例，仅 Win7 + "Do not use NLA" 组合可靠。
//
// 这里设置的是社区/厂商文档共识的"让 Windows 尊重应用 DSCP 标记"的注册表项
// （Cisco/TrueConf/3CX 文档一致），低风险、可逆：
//  1. HKLM\...\Tcpip\Parameters\DisableUserTOSSetting = 0
//     允许应用通过 IP_TOS 设置 DSCP（IP_TOS 兜底路径的前提）
//  2. HKLM\...\Tcpip\QoS\"Do not use NLA" = "1"
//     非域环境（工作组）下 Policy-based QoS 生效的前提（Win7 组策略场景）
//
// 返回恢复函数：退出时恢复原值/删除新增项，保持系统干净。失败仅警告不阻塞。
func EnsureDSCPSysConfig() func() {
	if !isElevated() {
		return func() {} // 非管理员：QoS2 主路径不依赖系统配置，跳过
	}
	var restore []func()
	// ① DisableUserTOSSetting（Tcpip\Parameters 键必然存在）
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Services\Tcpip\Parameters`, registry.SET_VALUE|registry.QUERY_VALUE); err == nil {
		old, _, oldErr := k.GetIntegerValue("DisableUserTOSSetting")
		if err2 := k.SetDWordValue("DisableUserTOSSetting", 0); err2 == nil {
			restore = append(restore, func() {
				k2, e := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Services\Tcpip\Parameters`, registry.SET_VALUE|registry.QUERY_VALUE)
				if e != nil {
					return
				}
				defer k2.Close()
				if oldErr == nil { // 原有值 → 写回
					k2.SetDWordValue("DisableUserTOSSetting", uint32(old))
				} else { // 原无此值 → 删除
					k2.DeleteValue("DisableUserTOSSetting")
				}
			})
			log.Printf("已放行应用 DSCP 标记（DisableUserTOSSetting=0），退出时自动恢复")
		}
		k.Close()
	}
	// ② Do not use NLA（Tcpip\QoS 键可能不存在，需创建）
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Services\Tcpip\QoS`, registry.SET_VALUE|registry.QUERY_VALUE); err == nil {
		old, _, oldErr := k.GetStringValue("Do not use NLA")
		if err2 := k.SetStringValue("Do not use NLA", "1"); err2 == nil {
			restore = append(restore, func() {
				k2, e := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Services\Tcpip\QoS`, registry.SET_VALUE|registry.QUERY_VALUE)
				if e != nil {
					return
				}
				defer k2.Close()
				if oldErr == nil {
					k2.SetStringValue("Do not use NLA", old)
				} else {
					k2.DeleteValue("Do not use NLA")
				}
			})
			log.Printf("已设置 Do not use NLA=1（非域环境 QoS 策略生效前提），退出时自动恢复")
		}
		k.Close()
	} else {
		// 键不存在：创建并写入
		if k2, _, err := registry.CreateKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Services\Tcpip\QoS`, registry.SET_VALUE); err == nil {
			if err2 := k2.SetStringValue("Do not use NLA", "1"); err2 == nil {
				restore = append(restore, func() {
					registry.DeleteKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Services\Tcpip\QoS`)
				})
				log.Printf("已设置 Do not use NLA=1（非域环境 QoS 策略生效前提），退出时自动恢复")
			}
			k2.Close()
		}
	}
	if len(restore) == 0 {
		return func() {}
	}
	return func() {
		for _, f := range restore {
			f()
		}
		log.Printf("已恢复系统 DSCP 相关注册表设置")
	}
}
