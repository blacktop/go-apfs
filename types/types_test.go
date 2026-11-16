package types

import (
	"encoding/binary"
	"testing"
)

func checksummedBlock() []byte {
	block := make([]byte, NX_DEFAULT_BLOCK_SIZE)
	for i := 8; i < len(block); i++ {
		block[i] = byte(i * 31)
	}
	binary.LittleEndian.PutUint64(block, CreateChecksum(block[8:]))
	return block
}

func TestVerifyChecksum(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(block []byte) []byte
		want   bool
	}{
		{"valid block", func(b []byte) []byte { return b }, true},
		{"corrupt payload byte", func(b []byte) []byte { b[2000] ^= 0xff; return b }, false},
		{"corrupt checksum field", func(b []byte) []byte { b[3] ^= 0x01; return b }, false},
		{"all-zero block", func(b []byte) []byte { return make([]byte, len(b)) }, false},
		{"shorter than the checksum field", func(b []byte) []byte { return b[:7] }, false},
		{"empty", func(b []byte) []byte { return nil }, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := VerifyChecksum(tt.change(checksummedBlock())); got != tt.want {
				t.Fatalf("VerifyChecksum = %v, want %v", got, tt.want)
			}
		})
	}
}
