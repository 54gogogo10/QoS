package capture

import (
	"net"

	"qostool/internal/config"
)

// matcher 把抓到的包匹配到配置的流。
type matcher struct {
	keys []Key
}

func newMatcher(cfg *config.Config) *matcher {
	m := &matcher{keys: make([]Key, len(cfg.Flows))}
	for i, f := range cfg.Flows {
		m.keys[i] = Key{
			DSCP:    int(f.DSCP),
			Proto:   17, // udp
			SrcIP:   net.ParseIP(f.SrcIP),
			DstIP:   net.ParseIP(f.DstIP),
			SrcPort: uint16(f.SrcPort),
			DstPort: uint16(f.DstPort),
		}
	}
	return m
}

// matchWithDiag 单趟扫描（不匹配包不再扫两遍）：
// 返回精确匹配（含 DSCP）的流下标 idx，以及忽略 DSCP 时 5 元组命中的下标 dscpOnly；
// 两者均为 -1 表示未命中。dscpOnly 用于诊断“发送端 DSCP 未生效”（实际 DSCP=0
// 但仍能识别出本应匹配的流）。
func (m *matcher) matchWithDiag(k Key) (idx, dscpOnly int) {
	idx, dscpOnly = -1, -1
	for i := range m.keys {
		if m.keys[i].Proto == k.Proto &&
			m.keys[i].SrcIP.Equal(k.SrcIP) && m.keys[i].DstIP.Equal(k.DstIP) &&
			m.keys[i].SrcPort == k.SrcPort && m.keys[i].DstPort == k.DstPort {
			if dscpOnly < 0 {
				dscpOnly = i
			}
			if m.keys[i].DSCP == k.DSCP {
				return i, -1
			}
		}
		if m.keys[i].Proto == k.Proto &&
			m.keys[i].SrcIP.Equal(k.DstIP) && m.keys[i].DstIP.Equal(k.SrcIP) &&
			m.keys[i].SrcPort == k.DstPort && m.keys[i].DstPort == k.SrcPort {
			if dscpOnly < 0 {
				dscpOnly = i
			}
			if m.keys[i].DSCP == k.DSCP {
				return i, -1
			}
		}
	}
	return -1, dscpOnly
}

// matchIgnoreDSCP 返回 5 元组命中的流下标（忽略 DSCP），-1 表示未命中。
// 用于诊断：发送端 DSCP 未生效时（实际 DSCP=0），仍能识别出“本应匹配的流”。
func (m *matcher) matchIgnoreDSCP(k Key) int {
	_, dscpOnly := m.matchWithDiag(k)
	return dscpOnly
}

// match 返回命中的流下标，-1 表示未命中。
// 方向无关：配置的源/目的互换也命中（双向测试时接收方向是反的）。
func (m *matcher) match(k Key) int {
	idx, _ := m.matchWithDiag(k)
	return idx
}
