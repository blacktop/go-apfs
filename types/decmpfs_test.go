package types

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"testing"
)

func patterned(n int, seed byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7) ^ seed ^ byte(i>>8)
	}
	return b
}

func header(ct compMethod, size uint64, attr []byte) *DecmpfsDiskHeader {
	return &DecmpfsDiskHeader{decmpfsDiskHeader: decmpfsDiskHeader{CompressionType: ct, UncompressedSize: size}, AttrBytes: attr}
}

func chunkedFork(chunks ...[]byte) []byte {
	table := make([]uint32, len(chunks)+1)
	table[0] = uint32(4 * len(table))
	for i, c := range chunks {
		table[i+1] = table[i] + uint32(len(c))
	}
	var fork bytes.Buffer
	binary.Write(&fork, binary.LittleEndian, table)
	for _, c := range chunks {
		fork.Write(c)
	}
	return fork.Bytes()
}

func TestDecompressLZBitmap(t *testing.T) {
	plain := patterned(decmpfsChunkSize+100, 1)
	first, second := plain[:decmpfsChunkSize], plain[decmpfsChunkSize:]
	small := patterned(3000, 2)

	for _, tt := range []struct {
		name string
		h    *DecmpfsDiskHeader
		fork []byte
		want []byte
		err  bool
	}{
		{"stored chunks, two blocks", header(CMP_RSRC_LZBITMAP, uint64(len(plain)), nil),
			chunkedFork(append([]byte{0xFF}, first...), append([]byte{0x06}, second...)), plain, false},
		{"stored attribute", header(CMP_ATTR_LZBITMAP, uint64(len(small)), append([]byte{0xFF}, small...)), nil, small, false},

		{"empty offset table", header(CMP_RSRC_LZBITMAP, 10, nil), []byte{0, 0, 0, 0}, nil, true},
		{"decreasing offsets", header(CMP_RSRC_LZBITMAP, 10, nil), append([]byte{12, 0, 0, 0, 20, 0, 0, 0, 16, 0, 0, 0}, make([]byte, 8)...), nil, true},
		{"block past the fork", header(CMP_RSRC_LZBITMAP, 10, nil), append([]byte{8, 0, 0, 0, 0xff, 0xff, 0, 0}, make([]byte, 8)...), nil, true},
		{"too few blocks for the size", header(CMP_RSRC_LZBITMAP, 2*decmpfsChunkSize, nil), chunkedFork(append([]byte{0xFF}, small[:8]...)), nil, true},
		{"stored block shorter than the size", header(CMP_RSRC_LZBITMAP, 100, nil), chunkedFork(append([]byte{0xFF}, small[:50]...)), nil, true},
		{"empty attribute", header(CMP_ATTR_LZBITMAP, 100, nil), nil, nil, true},
		{"attribute larger than one chunk", header(CMP_ATTR_LZBITMAP, decmpfsChunkSize+1, append([]byte{0xFF}, small...)), nil, nil, true},
		{"stored attribute length mismatch", header(CMP_ATTR_LZBITMAP, 99, append([]byte{0xFF}, small[:100]...)), nil, nil, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			w := bufio.NewWriter(&out)
			err := tt.h.DecompressFile(bytes.NewReader(tt.fork), w, []FileExtent{{Block: 0, Length: uint64(len(tt.fork))}}, true)
			if err == nil {
				err = w.Flush()
			}
			if (err != nil) != tt.err || (!tt.err && !bytes.Equal(out.Bytes(), tt.want)) {
				t.Fatalf("got (%d bytes, %v), want (%d bytes, error=%v)", out.Len(), err, len(tt.want), tt.err)
			}
		})
	}
}
