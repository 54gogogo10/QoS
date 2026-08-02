package protocol

import "testing"

func TestEncodeDecode(t *testing.T) {
	payload := make([]byte, HeaderSize+16)
	EncodeHeader(payload, 3, 42)
	fid, seq, ok := DecodeHeader(payload)
	if !ok || fid != 3 || seq != 42 {
		t.Fatalf("DecodeHeader = %d %d %v", fid, seq, ok)
	}
}

func TestDecodeBadMagic(t *testing.T) {
	payload := make([]byte, HeaderSize)
	payload[0] = 0xDE // 破坏 magic
	if _, _, ok := DecodeHeader(payload); ok {
		t.Fatal("坏 magic 应返回 ok=false")
	}
}

func TestDecodeTooShort(t *testing.T) {
	if _, _, ok := DecodeHeader(make([]byte, 4)); ok {
		t.Fatal("过短载荷应返回 ok=false")
	}
}
