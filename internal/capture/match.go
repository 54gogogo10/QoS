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

// match 返回命中的流下标，-1 表示未命中。
// 方向无关：配置的源/目的互换也命中（双向测试时接收方向是反的）。
func (m *matcher) match(k Key) int {
	for i := range m.keys {
		if m.keys[i].DSCP == k.DSCP && m.keys[i].Proto == k.Proto &&
			m.keys[i].SrcIP.Equal(k.SrcIP) && m.keys[i].DstIP.Equal(k.DstIP) &&
			m.keys[i].SrcPort == k.SrcPort && m.keys[i].DstPort == k.DstPort {
			return i
		}
		if m.keys[i].DSCP == k.DSCP && m.keys[i].Proto == k.Proto &&
			m.keys[i].SrcIP.Equal(k.DstIP) && m.keys[i].DstIP.Equal(k.SrcIP) &&
			m.keys[i].SrcPort == k.DstPort && m.keys[i].DstPort == k.SrcPort {
			return i
		}
	}
	return -1
}
