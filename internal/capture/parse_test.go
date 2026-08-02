package capture

import (
	"encoding/binary"
	"net"
	"testing"

	"github.com/gopacket/gopacket/layers"

	"qostool/internal/protocol"
)

// buildIPv4UDP 构造一个 IPv4+UDP 包：dscp、src/dst、payload。
func buildIPv4UDP(dscp byte, srcIP, dstIP net.IP, srcPort, dstPort uint16, payload []byte) []byte {
	buf := make([]byte, 20+8+len(payload))
	buf[0] = 0x45 // IPv4, IHL=5
	binary.BigEndian.PutUint16(buf[2:4], uint16(len(buf)))
	buf[8] = 64 // TTL
	buf[9] = 17 // UDP
	buf[12] = srcIP[12]
	buf[13] = srcIP[13]
	buf[14] = srcIP[14]
	buf[15] = srcIP[15]
	buf[16] = dstIP[12]
	buf[17] = dstIP[13]
	buf[18] = dstIP[14]
	buf[19] = dstIP[15]
	// TOS
	buf[1] = dscp << 2
	// UDP 头
	binary.BigEndian.PutUint16(buf[20:22], srcPort)
	binary.BigEndian.PutUint16(buf[22:24], dstPort)
	binary.BigEndian.PutUint16(buf[24:26], uint16(8+len(payload)))
	copy(buf[28:], payload)
	return buf
}

// buildIPv6UDP 构造一个 IPv6+UDP 包（无扩展头）。
func buildIPv6UDP(dscp byte, srcIP, dstIP net.IP, srcPort, dstPort uint16, payload []byte) []byte {
	buf := make([]byte, 40+8+len(payload))
	buf[0] = 0x60 | (dscp >> 2)
	buf[1] = (dscp & 0x03) << 6
	binary.BigEndian.PutUint16(buf[4:6], uint16(8+len(payload)))
	buf[6] = 17 // UDP
	buf[7] = 64 // hop limit
	copy(buf[8:24], srcIP)
	copy(buf[24:40], dstIP)
	binary.BigEndian.PutUint16(buf[40:42], srcPort)
	binary.BigEndian.PutUint16(buf[42:44], dstPort)
	binary.BigEndian.PutUint16(buf[44:46], uint16(8+len(payload)))
	copy(buf[48:], payload)
	return buf
}

func TestParseIPv4UDP(t *testing.T) {
	payload := make([]byte, 16)
	protocol.EncodeHeader(payload, 1, 99)
	pkt, err := parsePacket(buildIPv4UDP(46, net.ParseIP("192.168.1.1"), net.ParseIP("192.168.1.2"), 1000, 2000, payload))
	if err != nil {
		t.Fatal(err)
	}
	if pkt.key.DSCP != 46 || pkt.key.Proto != 17 {
		t.Fatalf("DSCP/Proto = %d/%d", pkt.key.DSCP, pkt.key.Proto)
	}
	if !pkt.key.SrcIP.Equal(net.ParseIP("192.168.1.1")) || !pkt.key.DstIP.Equal(net.ParseIP("192.168.1.2")) {
		t.Fatalf("IP 解析错误: %v -> %v", pkt.key.SrcIP, pkt.key.DstIP)
	}
	if pkt.key.SrcPort != 1000 || pkt.key.DstPort != 2000 {
		t.Fatalf("端口 = %d/%d", pkt.key.SrcPort, pkt.key.DstPort)
	}
	fid, seq, ok := protocol.DecodeHeader(pkt.payload)
	if !ok || fid != 1 || seq != 99 {
		t.Fatalf("载荷解码 = %d/%d/%v", fid, seq, ok)
	}
}

func TestParseIPv6UDP(t *testing.T) {
	payload := make([]byte, 16)
	protocol.EncodeHeader(payload, 2, 7)
	pkt, err := parsePacket(buildIPv6UDP(34, net.ParseIP("2001:db8::1"), net.ParseIP("2001:db8::2"), 1000, 2000, payload))
	if err != nil {
		t.Fatal(err)
	}
	if pkt.key.DSCP != 34 {
		t.Fatalf("DSCP = %d, want 34", pkt.key.DSCP)
	}
	if !pkt.key.SrcIP.Equal(net.ParseIP("2001:db8::1")) {
		t.Fatalf("SrcIP = %v", pkt.key.SrcIP)
	}
	fid, seq, ok := protocol.DecodeHeader(pkt.payload)
	if !ok || fid != 2 || seq != 7 {
		t.Fatalf("载荷解码 = %d/%d/%v", fid, seq, ok)
	}
}

func TestParseNonUDP(t *testing.T) {
	buf := buildIPv4UDP(0, net.ParseIP("1.1.1.1"), net.ParseIP("2.2.2.2"), 1, 2, []byte{1, 2, 3})
	buf[9] = 6 // TCP
	pkt, err := parsePacket(buf)
	if err != nil {
		t.Fatal(err)
	}
	if pkt.key.Proto != 6 || pkt.payload != nil {
		t.Fatalf("TCP 包不应有 payload: %+v", pkt)
	}
}

func TestParseGarbage(t *testing.T) {
	if _, err := parsePacket([]byte{0x01, 0x02}); err == nil {
		t.Fatal("过短包应报错")
	}
	if _, err := parsePacket(make([]byte, 40)); err == nil {
		t.Fatal("版本号非法应报错")
	}
}

func TestSkipLinkHeaderEthernet(t *testing.T) {
	ip := buildIPv4UDP(46, net.ParseIP("1.1.1.1"), net.ParseIP("2.2.2.2"), 1, 2, []byte{1})
	eth := append(make([]byte, 14), ip...)
	// 加一层 VLAN 标签
	vlan := append(make([]byte, 18), ip...)
	binary.BigEndian.PutUint16(vlan[12:14], 0x8100)
	binary.BigEndian.PutUint16(vlan[16:18], 0x0800)
	raw := append([]byte(nil), vlan...)
	out := skipLinkHeader(layers.LinkTypeEthernet, raw)
	if len(out) != len(ip) {
		t.Fatalf("VLAN 偏移错误: len = %d, want %d", len(out), len(ip))
	}
	out2 := skipLinkHeader(layers.LinkTypeEthernet, eth)
	if len(out2) != len(ip) {
		t.Fatalf("无 VLAN 偏移错误: len = %d", len(out2))
	}
}
