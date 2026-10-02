package dmg

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/blacktop/go-plist"
	lzfse "github.com/blacktop/lzfse-cgo"
	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/ulikunitz/xz"
	"github.com/ulikunitz/xz/lzma"
	boundedxz "github.com/xi2/xz"
	"hash/crc32"
	"io"
	"math"
	"strings"
	"testing"
)

func compressTestData(t *testing.T, kind string, data []byte) []byte {
	t.Helper()
	if kind == "lzfse" {
		return lzfse.EncodeBuffer(data)
	}
	if kind == "adc" {
		var encoded []byte
		for len(data) > 0 {
			n := min(len(data), 128)
			encoded = append(encoded, 0x80|byte(n-1))
			encoded = append(encoded, data[:n]...)
			data = data[n:]
		}
		return encoded
	}
	var out bytes.Buffer
	var w io.WriteCloser
	var err error
	switch kind {
	case "zlib":
		w = zlib.NewWriter(&out)
	case "xz":
		w, err = xz.NewWriter(&out)
	case "lzma":
		w, err = lzma.NewWriter(&out)
	default:
		t.Fatalf("unknown encoder %s", kind)
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestBoundedChunkDecoders(t *testing.T) {
	for _, codec := range []struct {
		name string
		typ  udifBlockChunkType
	}{
		{"zlib", COMPRESS_ZLIB}, {"xz", COMPRESSS_LZMA}, {"lzma", COMPRESSS_LZMA}, {"lzfse", COMPRESSS_LZFSE}, {"adc", COMPRESS_ADC}, {"bzip2", COMPRESSS_BZ2},
	} {
		t.Run(codec.name, func(t *testing.T) {
			plain := bytes.Repeat([]byte("bounded!"), 128)
			var encoded []byte
			if codec.name == "bzip2" {
				plain = []byte("hello bounded bzip2")
				var err error
				encoded, err = base64.StdEncoding.DecodeString("QlpoOTFBWSZTWV2KRWIAAASZgEAAEAAWZcIQIAAiAANCAaAMfWKde6EcE4BfF3JFOFCQXYpFYg==")
				if err != nil {
					t.Fatal(err)
				}
			} else {
				encoded = compressTestData(t, codec.name, plain)
			}
			for _, tc := range []struct {
				name    string
				length  int
				wantErr bool
			}{
				{"exact", len(plain), false}, {"short", len(plain) + 1, true}, {"excess", len(plain) / 2, true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					chunk := udifBlockChunk{Type: codec.typ, DiskLength: uint64(tc.length), CompressedLength: uint64(len(encoded))}
					var out bytes.Buffer
					_, err := chunk.DecompressChunk(io.NewSectionReader(bytes.NewReader(encoded), 0, int64(len(encoded))), make([]byte, len(encoded)), &out)
					if (err != nil) != tc.wantErr {
						t.Fatalf("error=%v, wantErr=%v", err, tc.wantErr)
					}
					if tc.name == "short" && !errors.Is(err, io.ErrUnexpectedEOF) {
						t.Fatalf("short output: %v", err)
					}
					if out.Len() > tc.length+1 {
						t.Fatalf("decoder produced %d bytes; limit %d", out.Len(), tc.length+1)
					}
					if !tc.wantErr && !bytes.Equal(out.Bytes(), plain) {
						t.Fatal("decoded bytes differ")
					}
				})
			}
		})
	}
}

func TestChunkBombAndMarkers(t *testing.T) {
	encoded := compressTestData(t, "zlib", bytes.Repeat([]byte{'x'}, 1<<20))
	chunk := udifBlockChunk{Type: COMPRESS_ZLIB, DiskLength: 512, CompressedLength: uint64(len(encoded))}
	var out bytes.Buffer
	_, err := chunk.DecompressChunk(io.NewSectionReader(bytes.NewReader(encoded), 0, int64(len(encoded))), make([]byte, len(encoded)), &out)
	if err == nil || !strings.Contains(err.Error(), "exceeds declared") || out.Len() != 513 {
		t.Fatalf("bomb: length=%d error=%v", out.Len(), err)
	}
	for _, kind := range []udifBlockChunkType{COMMENT, LAST_BLOCK} {
		out.Reset()
		chunk = udifBlockChunk{Type: kind, DiskLength: math.MaxUint64, CompressedLength: math.MaxUint64}
		if _, err := chunk.DecompressChunk(nil, nil, &out); err != nil || out.Len() != 0 {
			t.Fatalf("marker: length=%d error=%v", out.Len(), err)
		}
	}
	for _, kind := range []udifBlockChunkType{ZERO_FILL, IGNORED} {
		out.Reset()
		chunk = udifBlockChunk{Type: kind, DiskLength: zeroBlockSize + 7}
		if _, err := chunk.DecompressChunk(nil, nil, &out); err != nil || !bytes.Equal(out.Bytes(), make([]byte, zeroBlockSize+7)) {
			t.Fatalf("zero fill: %v", err)
		}
	}
}

type countingReader struct {
	data  []byte
	reads int
}

func (r *countingReader) ReadAt(p []byte, off int64) (int, error) {
	r.reads++
	return bytes.NewReader(r.data).ReadAt(p, off)
}

func testPartition(t *testing.T) (*DMG, *countingReader, []byte) {
	t.Helper()
	data := append(bytes.Repeat([]byte{'A'}, 128), bytes.Repeat([]byte{'B'}, 128)...)
	r := &countingReader{data: data}
	sr := io.NewSectionReader(r, 0, int64(len(data)))
	b := Partition{udifBlockData: udifBlockData{StartSector: 1, SectorCount: 1, BuffersNeeded: 1}, sr: sr, Chunks: []udifBlockChunk{
		{Type: UNCOMPRESSED, DiskOffset: 512, DiskLength: 128, CompressedLength: 128},
		{Type: UNCOMPRESSED, DiskOffset: 640, DiskLength: 128, CompressedOffset: 128, CompressedLength: 128},
		{Type: ZERO_FILL, DiskOffset: 768, DiskLength: 256},
	}}
	cache, err := lru.New[int, []byte](1)
	if err != nil {
		t.Fatal(err)
	}
	d := &DMG{Footer: UDIFResourceFile{SectorCount: 2}, Partitions: []Partition{b}, sr: sr, cache: cache}
	return d, r, append(append([]byte(nil), data...), make([]byte, 256)...)
}

func TestReadAtBoundariesAndCache(t *testing.T) {
	for _, mode := range []string{"partition", "cached", "uncached"} {
		t.Run(mode, func(t *testing.T) {
			d, r, want := testPartition(t)
			var reader io.ReaderAt = d
			if mode == "partition" {
				reader = &d.Partitions[0]
			}
			if mode == "uncached" {
				d.config.DisableCache = true
			}
			for i := 0; i < 2; i++ {
				p := make([]byte, 8)
				n, err := reader.ReadAt(p, 3)
				if n != 8 || err != nil || !bytes.Equal(p, want[3:11]) {
					t.Fatalf("repeated read %d: n=%d err=%v data=%q", i, n, err, p)
				}
			}
			expectedReads := 1
			if mode == "uncached" {
				expectedReads = 2
			}
			if r.reads != expectedReads {
				t.Fatalf("cache reads=%d want=%d", r.reads, expectedReads)
			}
			p := make([]byte, 1)
			if _, err := reader.ReadAt(p, 128); err != nil {
				t.Fatal(err)
			}
			if _, err := reader.ReadAt(p, 0); err != nil {
				t.Fatal(err)
			}
			if r.reads != expectedReads+2 {
				t.Fatalf("eviction reads=%d want=%d", r.reads, expectedReads+2)
			}
			for _, tc := range []struct {
				name   string
				off    int64
				length int
				n      int
				err    error
			}{
				{"within", 11, 12, 12, nil}, {"boundary", 128, 128, 128, nil}, {"all", 0, 512, 512, nil}, {"across", 120, 160, 160, nil}, {"short", 500, 20, 12, io.EOF}, {"end", 512, 1, 0, io.EOF}, {"beyond", 513, 1, 0, io.EOF}, {"empty end", 512, 0, 0, nil}, {"empty beyond", math.MaxInt64, 0, 0, nil},
			} {
				t.Run(tc.name, func(t *testing.T) {
					p := bytes.Repeat([]byte{0xcc}, tc.length)
					n, err := reader.ReadAt(p, tc.off)
					if n != tc.n || !errors.Is(err, tc.err) {
						t.Fatalf("n=%d err=%v, want %d %v", n, err, tc.n, tc.err)
					}
					if n > 0 && !bytes.Equal(p[:n], want[int(tc.off):int(tc.off)+n]) {
						t.Fatalf("wrong bytes: %x", p[:n])
					}
				})
			}
			if n, err := reader.ReadAt(make([]byte, 1), -1); n != 0 || err == nil || !strings.Contains(err.Error(), "negative") {
				t.Fatalf("negative offset: %d %v", n, err)
			}
		})
	}
}

func TestSharedCallersRejectInvalidChunks(t *testing.T) {
	for _, bad := range []struct {
		name string
		edit func(*Partition)
	}{
		{"huge logical length", func(b *Partition) { b.Chunks[0].DiskLength = 1 << 62 }},
		{"huge zero-fill length", func(b *Partition) { b.Chunks[0].Type = ZERO_FILL; b.Chunks[0].DiskLength = 1 << 62 }},
		{"overflow logical end", func(b *Partition) { b.Chunks[0].DiskOffset = math.MaxUint64; b.Chunks[0].DiskLength = 2 }},
		{"overflow partition", func(b *Partition) { b.StartSector = math.MaxUint64 }},
		{"outside input", func(b *Partition) { b.Chunks[0].CompressedOffset = math.MaxUint64 }},
		{"overflow input length", func(b *Partition) { b.Chunks[0].CompressedLength = math.MaxUint64 }},
		{"raw length mismatch", func(b *Partition) { b.Chunks[0].CompressedLength-- }},
		{"chunk allocation ceiling", func(b *Partition) {
			b.SectorCount = maxDecodedChunkSize/sectorSize + 1
			b.Chunks[0].DiskLength = maxDecodedChunkSize + 1
		}},
	} {
		for _, caller := range []string{"DMG.ReadAt", "ReadFile", "Partition.ReadAt", "Write", "Load"} {
			t.Run(bad.name+"/"+caller, func(t *testing.T) {
				d, _, _ := testPartition(t)
				b := &d.Partitions[0]
				bad.edit(b)
				var output bytes.Buffer
				var err error
				switch caller {
				case "DMG.ReadAt":
					_, err = d.ReadAt(make([]byte, 16), 0)
				case "ReadFile":
					err = d.ReadFile(bufio.NewWriter(&output), 0, 16)
				case "Partition.ReadAt":
					_, err = b.ReadAt(make([]byte, 16), 0)
				case "Write":
					err = b.Write(bufio.NewWriter(&output))
				case "Load":
					b.Name = "Primary GPT Header"
					err = d.Load()
				}
				if err == nil {
					t.Fatal("malformed metadata succeeded")
				}
			})
		}
	}
}

func TestReadFileAndPartitionWrite(t *testing.T) {
	d, _, want := testPartition(t)
	var out bytes.Buffer
	if err := d.ReadFile(bufio.NewWriter(&out), 120, 300); err != nil || !bytes.Equal(out.Bytes(), want[120:420]) {
		t.Fatalf("ReadFile: %v bytes=%x", err, out.Bytes())
	}
	out.Reset()
	if err := d.Partitions[0].Write(bufio.NewWriter(&out)); err != nil || !bytes.Equal(out.Bytes(), want) {
		t.Fatalf("Write: %v", err)
	}
	for _, r := range [][2]int64{{-1, 1}, {0, -1}, {math.MaxInt64, 1}, {500, 20}} {
		out.Reset()
		err := d.ReadFile(bufio.NewWriter(&out), r[0], r[1])
		if err == nil {
			t.Fatalf("ReadFile accepted %v", r)
		}
	}
	d.Partitions[0].Chunks = d.Partitions[0].Chunks[:1]
	if n, err := d.ReadAt(make([]byte, 200), 0); n != 128 || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("gap: n=%d err=%v", n, err)
	}
}

func syntheticDMG(t *testing.T, change func(*UDIFResourceFile, *udifBlockData, *udifBlockChunk)) *io.SectionReader {
	t.Helper()
	footer := UDIFResourceFile{Signature: udifSignature{'k', 'o', 'l', 'y'}, SectorCount: 4, DataForkOffset: 17, DataForkLength: 523}
	header := udifBlockData{Signature: udifSignature{'m', 'i', 's', 'h'}, StartSector: 1, SectorCount: 1, DataOffset: 11, ChunkCount: 1}
	chunk := udifBlockChunk{Type: UNCOMPRESSED, DiskLength: 1, CompressedLength: 512}
	if change != nil {
		change(&footer, &header, &chunk)
	}
	var blk bytes.Buffer
	if err := binary.Write(&blk, binary.BigEndian, header); err != nil {
		t.Fatal(err)
	}
	if err := binary.Write(&blk, binary.BigEndian, chunk); err != nil {
		t.Fatal(err)
	}
	p, err := plist.Marshal(resourceFork{ResourceFork: map[string][]block{"blkx": {{Name: "test", Data: blk.Bytes()}}}}, plist.XMLFormat)
	if err != nil {
		t.Fatal(err)
	}
	var image bytes.Buffer
	image.Write(make([]byte, 28))
	image.Write(bytes.Repeat([]byte{'P'}, 512))
	footer.PlistOffset = uint64(image.Len())
	footer.PlistLength = uint64(len(p))
	image.Write(p)
	if err := binary.Write(&image, binary.BigEndian, footer); err != nil {
		t.Fatal(err)
	}
	return io.NewSectionReader(bytes.NewReader(image.Bytes()), 0, int64(image.Len()))
}

func TestNewDMGChecksSectorArithmetic(t *testing.T) {
	for _, tc := range []struct {
		name     string
		change   func(*UDIFResourceFile, *udifBlockData, *udifBlockChunk)
		contains string
	}{
		{"image overflow", func(f *UDIFResourceFile, b *udifBlockData, c *udifBlockChunk) { f.SectorCount = math.MaxUint64 }, "image sector count"},
		{"partition overflow", func(f *UDIFResourceFile, b *udifBlockData, c *udifBlockChunk) { b.StartSector = math.MaxUint64 }, "partition sector range"},
		{"partition outside image", func(f *UDIFResourceFile, b *udifBlockData, c *udifBlockChunk) { b.SectorCount = 4 }, "outside image"},
		{"chunk offset overflow", func(f *UDIFResourceFile, b *udifBlockData, c *udifBlockChunk) { c.DiskOffset = math.MaxUint64 }, "chunk sector range"},
		{"chunk length overflow", func(f *UDIFResourceFile, b *udifBlockData, c *udifBlockChunk) { c.DiskLength = 1 << 62 }, "chunk sector range"},
		{"compressed overflow", func(f *UDIFResourceFile, b *udifBlockData, c *udifBlockChunk) { c.CompressedOffset = math.MaxUint64 }, "compressed range"},
		{"compressed outside fork", func(f *UDIFResourceFile, b *udifBlockData, c *udifBlockChunk) { c.CompressedLength = 513 }, "compressed range"},
		{"fork overflow", func(f *UDIFResourceFile, b *udifBlockData, c *udifBlockChunk) { f.DataForkOffset = math.MaxUint64 }, "data fork range"},
		{"partition data overflow", func(f *UDIFResourceFile, b *udifBlockData, c *udifBlockChunk) { b.DataOffset = math.MaxUint64 }, "data offset"},
		{"truncated chunk table", func(f *UDIFResourceFile, b *udifBlockData, c *udifBlockChunk) { b.ChunkCount = 2 }, "truncated chunk table"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewDMG(syntheticDMG(t, tc.change))
			if err == nil || !strings.Contains(err.Error(), tc.contains) {
				t.Fatalf("error=%v want %q", err, tc.contains)
			}
		})
	}
}

func TestCompressedOffsetCoordinates(t *testing.T) {
	d, err := NewDMG(syntheticDMG(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	b := &d.Partitions[0]
	if b.Chunks[0].CompressedOffset != 28 || b.Chunks[0].DiskOffset != 512 {
		t.Fatalf("offsets: %+v", b.Chunks[0])
	}
	p := make([]byte, 512)
	if n, err := b.ReadAt(p, 0); n != 512 || err != nil || !bytes.Equal(p, bytes.Repeat([]byte{'P'}, 512)) {
		t.Fatalf("partition offset: n=%d err=%v", n, err)
	}
	if n, err := d.ReadAt(p, 0); n != 512 || err != nil || !bytes.Equal(p, bytes.Repeat([]byte{'P'}, 512)) {
		t.Fatalf("DMG offset: n=%d err=%v", n, err)
	}
}

func TestLZMADictionaryLimit(t *testing.T) {
	encoded := compressTestData(t, "lzma", []byte("small"))
	binary.LittleEndian.PutUint32(encoded[1:5], maxLZMADictionary+1)
	_, err := newLZMAReader(encoded)
	var dictErr *lzma.ErrDictSize
	if !errors.As(err, &dictErr) {
		t.Fatalf("oversized dictionary: %v", err)
	}
}

func TestFixtureReadAt(t *testing.T) {
	for _, name := range []string{"test", "secure"} {
		t.Run(name, func(t *testing.T) {
			cfg := &Config{}
			if name == "secure" {
				cfg.Password = "password"
			}
			d, err := Open(fmt.Sprintf("../../../testdata/%s.dmg", name), cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { d.Close() })
			for _, reader := range []io.ReaderAt{d, &d.Partitions[d.firstAPFSPartition]} {
				magic := make([]byte, 4)
				if n, err := reader.ReadAt(magic, 32); n != 4 || err != nil || string(magic) != "NXSB" {
					t.Fatalf("APFS magic=%q n=%d error=%v", magic, n, err)
				}
			}
		})
	}
}

type rejectingWriter struct {
	called bool
	length int
}

func (w *rejectingWriter) Write(p []byte) (int, error) {
	w.called = true
	w.length = len(p)
	return 0, io.ErrClosedPipe
}

func TestLargeSparseChunkStaysBounded(t *testing.T) {
	const size = uint64(1) << 40
	b := Partition{udifBlockData: udifBlockData{SectorCount: size / sectorSize}, Chunks: []udifBlockChunk{{Type: ZERO_FILL, DiskLength: size}}}
	p := bytes.Repeat([]byte{'X'}, 13)
	if n, err := b.ReadAt(p, int64(size)-13); n != 13 || err != nil || !bytes.Equal(p, make([]byte, 13)) {
		t.Fatalf("sparse read n=%d err=%v bytes=%x", n, err, p)
	}
	w := new(rejectingWriter)
	if err := b.Write(bufio.NewWriter(w)); !errors.Is(err, io.ErrClosedPipe) || !w.called || w.length > zeroBlockSize {
		t.Fatalf("sparse write: err=%v first write=%d", err, w.length)
	}
}

func TestMarkerMetadataIsNotData(t *testing.T) {
	for _, kind := range []udifBlockChunkType{COMMENT, LAST_BLOCK} {
		d, err := NewDMG(syntheticDMG(t, func(f *UDIFResourceFile, b *udifBlockData, c *udifBlockChunk) {
			*c = udifBlockChunk{Type: kind, DiskOffset: math.MaxUint64, DiskLength: math.MaxUint64, CompressedOffset: math.MaxUint64, CompressedLength: math.MaxUint64}
		}))
		if err != nil {
			t.Fatalf("marker %s: %v", kind, err)
		}
		if len(d.Partitions[0].Chunks) != 0 {
			t.Fatal("marker became a data chunk")
		}
	}
}

func TestXZDictionaryLimit(t *testing.T) {
	encoded := compressTestData(t, "xz", []byte("small"))
	const headerStart = xz.HeaderLen
	headerLength := (int(encoded[headerStart]) + 1) * 4
	header := encoded[headerStart : headerStart+headerLength]
	if header[1] != 0 || header[2] != 0x21 || header[3] != 1 {
		t.Fatalf("unexpected generated XZ block header: %x", header)
	}
	header[4] = lzma.EncodeDictCap(int64(maxLZMADictionary) + 1)
	binary.LittleEndian.PutUint32(header[len(header)-4:], crc32.ChecksumIEEE(header[:len(header)-4]))
	for _, prefix := range [][]byte{nil, compressTestData(t, "xz", []byte("first stream"))} {
		stream := append(append([]byte(nil), prefix...), encoded...)
		reader, err := newLZMAReader(stream)
		if err == nil {
			_, err = io.Copy(io.Discard, reader)
		}
		if !errors.Is(err, boundedxz.ErrMemlimit) {
			t.Fatalf("oversized XZ dictionary: %v", err)
		}
	}
}
