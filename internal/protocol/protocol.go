// Package protocol 定义测试流 UDP 载荷头的编码与解码。
package protocol

import "encoding/binary"

// 载荷头格式：[magic 4B][flow_id 2B][seq 4B][send_ts 8B]，总长 18 字节。
// send_ts 为发送时刻 UnixNano（v2.7.0 起）；旧版发送端只有前 10 字节（send_ts=0）。
const (
	Magic           = 0x514F5354 // "QOST"
	HeaderSize      = 18
	TimestampOffset = 10 // send_ts 在载荷头中的偏移（8 字节，大端）
)

// EncodeHeader 把 flowID/seq/sendTs 写入 payload 前 18 字节。
func EncodeHeader(payload []byte, flowID uint16, seq uint32, sendTs int64) {
	binary.BigEndian.PutUint32(payload[0:4], Magic)
	binary.BigEndian.PutUint16(payload[4:6], flowID)
	binary.BigEndian.PutUint32(payload[6:10], seq)
	binary.BigEndian.PutUint64(payload[TimestampOffset:TimestampOffset+8], uint64(sendTs))
}

// EncodeSeqTs 发送循环内只更新 seq 与时间戳（比 EncodeHeader 少写 flow_id/magic，更快）。
func EncodeSeqTs(payload []byte, seq uint32, sendTs int64) {
	binary.BigEndian.PutUint32(payload[6:10], seq)
	binary.BigEndian.PutUint64(payload[TimestampOffset:TimestampOffset+8], uint64(sendTs))
}

// DecodeHeader 校验 magic 并返回 flow_id/seq/send_ts；magic 不匹配或过短返回 ok=false。
// sendTs==0 表示旧版发送端（载荷头只有 10 字节，无时间戳）。
func DecodeHeader(payload []byte) (flowID uint16, seq uint32, sendTs int64, ok bool) {
	if len(payload) < 10 {
		return 0, 0, 0, false
	}
	if binary.BigEndian.Uint32(payload[0:4]) != Magic {
		return 0, 0, 0, false
	}
	flowID = binary.BigEndian.Uint16(payload[4:6])
	seq = binary.BigEndian.Uint32(payload[6:10])
	if len(payload) >= HeaderSize {
		sendTs = int64(binary.BigEndian.Uint64(payload[TimestampOffset : TimestampOffset+8]))
	}
	return flowID, seq, sendTs, true
}
