package protocol

import (
	"encoding/binary"
	"testing"
)

func TestEncodeDecode(t *testing.T) {
	payload := make([]byte, HeaderSize+16)
	EncodeHeader(payload, 3, 42, 123456789)
	fid, seq, ts, ok := DecodeHeader(payload)
	if !ok || fid != 3 || seq != 42 || ts != 123456789 {
		t.Fatalf("DecodeHeader = %d %d %d %v", fid, seq, ts, ok)
	}
}

func TestDecodeLegacyHeaderNoTs(t *testing.T) {
	payload := make([]byte, 10) // 旧版 10B 头：无时间戳
	binary.BigEndian.PutUint32(payload[0:4], Magic)
	binary.BigEndian.PutUint16(payload[4:6], 5)
	binary.BigEndian.PutUint32(payload[6:10], 99)
	fid, seq, ts, ok := DecodeHeader(payload)
	if !ok || fid != 5 || seq != 99 || ts != 0 {
		t.Fatalf("旧头解码 = %d %d %d %v, want ts=0", fid, seq, ts, ok)
	}
}

func TestDecodeBadMagic(t *testing.T) {
	payload := make([]byte, HeaderSize)
	payload[0] = 0xDE // 破坏 magic
	if _, _, _, ok := DecodeHeader(payload); ok {
		t.Fatal("坏 magic 应返回 ok=false")
	}
}

func TestDecodeTooShort(t *testing.T) {
	if _, _, _, ok := DecodeHeader(make([]byte, 4)); ok {
		t.Fatal("过短载荷应返回 ok=false")
	}
}

func TestEncodeSeqTs(t *testing.T) {
	payload := make([]byte, HeaderSize)
	EncodeHeader(payload, 1, 100, 0)
	EncodeSeqTs(payload, 101, 999)
	fid, seq, ts, ok := DecodeHeader(payload)
	if !ok || fid != 1 || seq != 101 || ts != 999 {
		t.Fatalf("EncodeSeqTs 后 = %d %d %d %v", fid, seq, ts, ok)
	}
}
