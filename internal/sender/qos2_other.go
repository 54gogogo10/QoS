//go:build !windows

package sender

// qosFlow 非 Windows 平台占位：无 QoS2 (qWave)，DSCP 走 IP_TOS/IPV6_TCLASS。
type qosFlow struct{}

func (q *qosFlow) close() {}
