// Package protocol 定义测试流 UDP 载荷头的编码与解码。
package protocol

import "encoding/binary"

// 载荷头格式：[magic 4B][flow_id 2B][seq 4B]
const (
	Magic      = 0x514F5354 // "QOST"
	HeaderSize = 8
)

// EncodeHeader 把 flowID/seq 写入 payload 前 8 字节。
func EncodeHeader(payload []byte, flowID uint16, seq uint32) {
	binary.BigEndian.PutUint32(payload[0:4], Magic)
	binary.BigEndian.PutUint16(payload[4:6], flowID)
	binary.BigEndian.PutUint32(payload[6:10], seq)
}

// DecodeHeader 校验 magic 并返回 flow_id/seq；magic 不匹配或过短返回 ok=false。
func DecodeHeader(payload []byte) (flowID uint16, seq uint32, ok bool) {
	if len(payload) < HeaderSize {
		return 0, 0, false
	}
	if binary.BigEndian.Uint32(payload[0:4]) != Magic {
		return 0, 0, false
	}
	return binary.BigEndian.Uint16(payload[4:6]), binary.BigEndian.Uint32(payload[6:10]), true
}
