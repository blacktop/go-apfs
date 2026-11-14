package types

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

func checkpointMapBlock(count uint32, entries int) []byte {
	block := make([]byte, NX_DEFAULT_BLOCK_SIZE)
	binary.LittleEndian.PutUint64(block[8:], 0x1234)
	binary.LittleEndian.PutUint64(block[16:], 7)
	binary.LittleEndian.PutUint32(block[24:], uint32(OBJECT_TYPE_CHECKPOINT_MAP))
	binary.LittleEndian.PutUint32(block[32:], uint32(CHECKPOINT_MAP_LAST))
	binary.LittleEndian.PutUint32(block[36:], count)
	for i := 0; i < entries; i++ {
		binary.LittleEndian.PutUint64(block[40+i*40+32:], uint64(0x1000+i))
	}
	binary.LittleEndian.PutUint64(block[0:], CreateChecksum(block[8:]))
	return block
}

func TestReadObjCheckpointMap(t *testing.T) {
	BLOCK_SIZE = NX_DEFAULT_BLOCK_SIZE
	const fits = (NX_DEFAULT_BLOCK_SIZE - 40) / 40
	for _, tt := range []struct {
		name    string
		count   uint32
		entries int
		err     bool
	}{
		{"mappings decode", 5, 5, false},
		{"count fills the block", fits, 0, false},
		{"count overruns the block", fits + 1, 0, true},
		{"count of 2^32-1", 0xFFFFFFFF, 0, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			o, err := ReadObj(bytes.NewReader(checkpointMapBlock(tt.count, tt.entries)), 0)
			if (err != nil) != tt.err {
				t.Fatalf("got error %v, want error=%v", err, tt.err)
			}
			if err != nil && !strings.Contains(err.Error(), "does not fit") {
				t.Fatalf("got error %v, want the count bound to reject it before allocating", err)
			}
			if err == nil {
				if cpm := o.Body.(CheckpointMapPhys); len(cpm.Map) != int(tt.count) {
					t.Fatalf("got %d mappings, want %d", len(cpm.Map), tt.count)
				}
			}
		})
	}
}
