//go:build windows

package sender

import (
	"fmt"
	"syscall"
	"unsafe"
)

// QoS2 (qWave) DSCP 打标（v2.8.0）。
//
// 背景：Windows 7/8/10 上 setsockopt(IP_TOS) 的标记会被系统剥掉（Pacer.sys /
// QoS 子系统不信任纯 IP_TOS 标记，Pingman/iperf3/libzmq 均有实测），
// 而 QoS2 API（qwave.dll，Vista+ 内置）是官方支持的应用打标路径——
// Teams/Webex 等 VoIP 应用即用此 API 在 Win7/10 上打标。
// QOSSetOutgoingDSCPValue 需要管理员或 Network Configuration Operators 组成员
// （与 IP_TOS 对网络控制类 DSCP 的权限要求一致）。
// 注意：API 头文件是 qos2.h，但实现 DLL 是 qwave.dll（QOSSetFlow 文档标注
// req.dll: Qwave.dll）。
var (
	qwave                = syscall.NewLazyDLL("qwave.dll")
	procQOSCreateHandle    = qwave.NewProc("QOSCreateHandle")
	procQOSAddSocketToFlow = qwave.NewProc("QOSAddSocketToFlow")
	procQOSSetFlow         = qwave.NewProc("QOSSetFlow")
	procQOSCloseHandle     = qwave.NewProc("QOSCloseHandle")
)

// QOS_TRAFFIC_TYPE 枚举（qos2.h）。只是给系统的流量类型建议，
// 最终 DSCP 以 QOSSetOutgoingDSCPValue 显式设置值为准。
const (
	qosTrafficBestEffort      = 0
	qosTrafficBackground      = 1
	qosTrafficExcellentEffort = 2
	qosTrafficAudioVideo      = 3
	qosTrafficVoice           = 4
	qosTrafficControl         = 5
)

// QoS2 常量（qos2.h）。注意 QOS_NON_ADAPTIVE_FLOW = 0x00000002
// （0x1 是其他标志，网上资料常写错；wine 的 qos2.h 与 enet 实现均为 2）。
const (
	qosNonAdaptiveFlow      = 0x00000002 // 非自适应流：不做路径检测/带宽估计，仅打标
	qosSetOutgoingDSCPValue = 2          // QOS_SET_FLOW 操作：设置出向 DSCP
)

// qosVersion 是 QOSCreateHandle 的版本参数（当前版本 1.0）。
type qosVersion struct {
	major, minor uint16
}

// dscpToTrafficType 把 DSCP 值粗略映射到 QOS_TRAFFIC_TYPE。
// 映射仅为流量类型建议，实际标记值由 QOSSetOutgoingDSCPValue 精确控制。
func dscpToTrafficType(dscp int) uint32 {
	switch {
	case dscp <= 0:
		return qosTrafficBestEffort
	case dscp < 16: // CS1（AF11-AF13）：批量
		return qosTrafficBackground
	case dscp < 32: // CS2/CS3（AF21-AF33）：交互
		return qosTrafficExcellentEffort
	case dscp < 40: // CS4/AF4x：音视频
		return qosTrafficAudioVideo
	case dscp < 48: // EF/CS5：语音
		return qosTrafficVoice
	default: // CS6/CS7：网络控制
		return qosTrafficControl
	}
}

// qosFlow 是一个 UDP socket 的 QoS2 流。创建后系统按流对出向包打 DSCP，
// 不再依赖 IP_TOS（解决了 Win7/8/10 上应用标记被剥的问题）。
type qosFlow struct {
	handle syscall.Handle // QOS 句柄（非 0 表示已创建）
	flowID uint32
}

// setupQoS2 把已连接 socket（net.DialUDP）加入 QoS2 流并设置显式 DSCP。
// 返回错误时内部已清理，调用方可安全回退到 IP_TOS。
// DestAddr 传 NULL：socket 已 connect，系统使用其远端地址。
func setupQoS2(sock syscall.Handle, dscp int) (*qosFlow, error) {
	if err := qwave.Load(); err != nil {
		return nil, fmt.Errorf("qwave.dll 不可用（本系统无 qWave 组件）: %w", err)
	}
	ver := qosVersion{major: 1, minor: 0}
	var h syscall.Handle
	if r, _, e := procQOSCreateHandle.Call(
		uintptr(unsafe.Pointer(&ver)), uintptr(unsafe.Pointer(&h))); r == 0 {
		return nil, fmt.Errorf("QOSCreateHandle: %w", e)
	}
	qf := &qosFlow{handle: h}
	var flowID uint32
	if r, _, e := procQOSAddSocketToFlow.Call(
		uintptr(h), uintptr(sock), 0,
		uintptr(dscpToTrafficType(dscp)), uintptr(qosNonAdaptiveFlow),
		uintptr(unsafe.Pointer(&flowID))); r == 0 {
		procQOSCloseHandle.Call(uintptr(h))
		return nil, fmt.Errorf("QOSAddSocketToFlow: %w", e)
	}
	qf.flowID = flowID
	d := uint32(dscp)
	if r, _, e := procQOSSetFlow.Call(
		uintptr(h), uintptr(flowID), uintptr(qosSetOutgoingDSCPValue),
		uintptr(unsafe.Sizeof(d)), uintptr(unsafe.Pointer(&d)), 0, 0); r == 0 {
		qf.close()
		return nil, fmt.Errorf("QOSSetFlow(QOSSetOutgoingDSCPValue): %w", e)
	}
	return qf, nil
}

// close 释放 QOS 句柄（同时移除其下所有流）。
func (q *qosFlow) close() {
	if q != nil && q.handle != 0 {
		procQOSCloseHandle.Call(uintptr(q.handle))
		q.handle = 0
	}
}
