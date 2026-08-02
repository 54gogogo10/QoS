package capture

import (
	"encoding/binary"
	"fmt"
	"net"

	"github.com/gopacket/gopacket/layers"
)

// Key 是匹配用的 DSCP + 5 元组。
type Key struct {
	DSCP    int
	Proto   uint8 // 6=TCP, 17=UDP
	SrcIP   net.IP
	DstIP   net.IP
	SrcPort uint16
	DstPort uint16
}

// packet 是解析后的包（仅 UDP 有 payload）。
type packet struct {
	key     Key
	payload []byte
}

// parsePacket 解析 IPv4/IPv6 包，提取 DSCP、5 元组和 UDP payload。
// IPv6 扩展头暂不支持（本工具发送的包无扩展头）。
func parsePacket(data []byte) (*packet, error) {
	if len(data) < 20 {
		return nil, fmt.Errorf("包太短")
	}
	switch data[0] >> 4 {
	case 4:
		return parseIPv4(data)
	case 6:
		return parseIPv6(data)
	}
	return nil, fmt.Errorf("不支持的 IP 版本")
}

func parseIPv4(data []byte) (*packet, error) {
	ihl := int(data[0]&0x0F) * 4
	if ihl < 20 || len(data) < ihl {
		return nil, fmt.Errorf("非法 IPv4 头")
	}
	p := &packet{key: Key{
		DSCP:  int(data[1]) >> 2,
		Proto: data[9],
		SrcIP: net.IP(data[12:16]),
		DstIP: net.IP(data[16:20]),
	}}
	if p.key.Proto != 17 {
		return p, nil
	}
	if len(data) < ihl+8 {
		return nil, fmt.Errorf("UDP 头不完整")
	}
	p.key.SrcPort = binary.BigEndian.Uint16(data[ihl : ihl+2])
	p.key.DstPort = binary.BigEndian.Uint16(data[ihl+2 : ihl+4])
	udpLen := int(binary.BigEndian.Uint16(data[ihl+4 : ihl+6]))
	end := ihl + udpLen
	if end > len(data) {
		end = len(data)
	}
	if end > ihl+8 {
		p.payload = data[ihl+8 : end]
	}
	return p, nil
}

func parseIPv6(data []byte) (*packet, error) {
	if len(data) < 40 {
		return nil, fmt.Errorf("IPv6 包太短")
	}
	p := &packet{key: Key{
		DSCP:  (int(data[0]&0x0F) << 2) | (int(data[1]) >> 6),
		Proto: data[6],
		SrcIP: net.IP(data[8:24]),
		DstIP: net.IP(data[24:40]),
	}}
	if p.key.Proto != 17 {
		return p, nil
	}
	if len(data) < 48 {
		return nil, fmt.Errorf("UDP 头不完整")
	}
	p.key.SrcPort = binary.BigEndian.Uint16(data[40:42])
	p.key.DstPort = binary.BigEndian.Uint16(data[42:44])
	udpLen := int(binary.BigEndian.Uint16(data[44:46]))
	end := 40 + udpLen
	if end > len(data) {
		end = len(data)
	}
	if end > 48 {
		p.payload = data[48:end]
	}
	return p, nil
}

// skipLinkHeader 根据链路类型跳过链路层头，返回 IP 包起点。
// 支持 Ethernet（含最多两层 VLAN 标签）、LinuxSLL、Null/Loop、Raw。
func skipLinkHeader(lt layers.LinkType, data []byte) []byte {
	switch lt {
	case layers.LinkTypeEthernet:
		if len(data) < 14 {
			return nil
		}
		off := 14
		for i := 0; i < 2; i++ {
			if off+4 > len(data) {
				return nil
			}
			switch binary.BigEndian.Uint16(data[off-2 : off]) {
			case 0x8100, 0x88A8, 0x9100:
				off += 4
			default:
				return data[off:]
			}
		}
		return data[off:]
	case layers.LinkTypeLinuxSLL:
		if len(data) < 16 {
			return nil
		}
		return data[16:]
	case layers.LinkTypeNull, layers.LinkTypeLoop:
		if len(data) < 4 {
			return nil
		}
		return data[4:]
	case layers.LinkTypeRaw:
		return data
	}
	return data
}
