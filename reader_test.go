package apfs

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"testing"

	"github.com/blacktop/go-apfs/types"
	"github.com/blacktop/lzfse-cgo"
)

func TestReaderNodeBounds(t *testing.T) {
	for _, data := range [][]byte{nil, make([]byte, readerBlockSize-1)} {
		if _, err := parseReaderNode(data); err == nil {
			t.Fatal("accepted truncated node")
		}
	}
	node := func(flags, level uint16, count uint32) []byte {
		data := make([]byte, readerBlockSize)
		le.PutUint16(data[32:], flags)
		le.PutUint16(data[34:], level)
		le.PutUint32(data[36:], count)
		return data
	}
	if _, err := parseReaderNode(node(nodeLeaf, 0, 0)); err != nil {
		t.Fatal("rejected an empty leaf:", err)
	}
	for name, data := range map[string][]byte{
		"excessive record count":  node(nodeLeaf, 0, 1025),
		"level 0 without leaf":    node(0, 0, 0),
		"leaf flag above level 0": node(nodeLeaf, 1, 1),
		"empty index node":        node(0, 1, 0),
	} {
		if _, err := parseReaderNode(data); err == nil {
			t.Fatal("accepted node with", name)
		}
	}
}

func TestReaderExtents(t *testing.T) {
	source := make([]byte, 3*readerBlockSize)
	copy(source[readerBlockSize:], "hello")
	copy(source[2*readerBlockSize:], "world")
	r := &Reader{source: bytes.NewReader(source), size: int64(len(source))}
	file, err := r.extentFile([]readerExtent{{0, 1, 5}, {5, 2, 5}}, 10)
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 6)
	if _, err := file.ReadAt(data, 3); err != nil || string(data) != "loworl" {
		t.Fatal(string(data), err)
	}
	if n, err := file.ReadAt(data, 8); n != 2 || err != io.EOF {
		t.Fatal(n, err)
	}
	if _, err := r.extentFile([]readerExtent{{1, 1, 5}}, 5); err == nil {
		t.Fatal("accepted sparse extent")
	}
	if _, err := r.extentFile([]readerExtent{{0, ^uint64(0), 5}}, 5); err == nil {
		t.Fatal("accepted overflowing extent")
	}
}

func TestReaderInlineCompression(t *testing.T) {
	for _, tc := range []struct {
		method uint32
		data   []byte
	}{
		{cmpAttrLZVN, []byte{6, 1, 2}},
		{cmpAttrLZFSE, []byte{6, 1, 2}},
		{cmpAttrLZBitmap, []byte{0xff, 1, 2}},
		{cmpAttrZlib, []byte{0xf, 1, 2}},
		{cmpAttrUncompressed, []byte{0xcc, 1, 2}},
	} {
		out, err := decodeInline(tc.data, 2, tc.method)
		if err != nil || !bytes.Equal(out, []byte{1, 2}) {
			t.Fatal(tc.method, out, err)
		}
		if _, err := decodeInline(tc.data, 3, tc.method); err == nil {
			t.Fatal("accepted decoded size mismatch")
		}
	}
	if _, err := decodeInline(nil, 0, cmpAttrLZBitmap); err == nil {
		t.Fatal("accepted empty compressed data")
	}
	if _, err := decodeInline([]byte{0}, ^uint64(0), cmpAttrLZFSE); err == nil {
		t.Fatal("accepted oversized inline data")
	}
}

// The fixtures are compressedPlain encoded by macOS libcompression with
// COMPRESSION_LZVN and COMPRESSION_LZBITMAP, so that each decoder runs on a
// real compressed stream.
const lzvnFixture = "e009676f2d61706673206c7a6269746d61702066697874757265203819f0fff0bce262690600000000000000"
const lzbitmapFixture = "5a424d094500000002002d00002e00002e0000676f2d61706673206c7a6269746d617020666978757265676f2d6170667319665437ffff0a00bce3cf3f000100000000000000000000060000000000"

var compressedPlain = bytes.Repeat([]byte("go-apfs lzbitmap fixture "), 21)[:512]

// TestReaderCompressedChunks runs each codec's decoder on a compressed chunk,
// not a stored one, through both the attribute and the resource-fork path.
func TestReaderCompressedChunks(t *testing.T) {
	lzvn, err := hex.DecodeString(lzvnFixture)
	if err != nil {
		t.Fatal(err)
	}
	lzbitmap, err := hex.DecodeString(lzbitmapFixture)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name           string
		inline, forked uint32
		stored         bool
		encoded        []byte
	}{
		{"LZVN", cmpAttrLZVN, cmpRsrcLZVN, lzvn[0] == 6, lzvn},
		{"LZFSE", cmpAttrLZFSE, cmpRsrcLZFSE, false, lzfse.EncodeBuffer(compressedPlain)},
		{"LZBITMAP", cmpAttrLZBitmap, cmpRsrcLZBitmap, lzbitmap[0] == 0xff, lzbitmap},
	} {
		if tc.name == "LZFSE" {
			tc.stored = tc.encoded[0] != 0x62
		}
		if tc.stored || len(tc.encoded) == 0 || len(tc.encoded) >= len(compressedPlain) {
			t.Fatalf("%s fixture is not a compressed chunk", tc.name)
		}
		out, err := decodeInline(tc.encoded, uint64(len(compressedPlain)), tc.inline)
		if err != nil || !bytes.Equal(out, compressedPlain) {
			t.Fatalf("%s attribute decode: %v", tc.name, err)
		}
		// lzfse.LzBitMapDecompress does not report the decoded length, so a
		// short LZBITMAP decode cannot be detected yet.
		if _, err := decodeInline(tc.encoded, uint64(len(compressedPlain))+1, tc.inline); err == nil && tc.name != "LZBITMAP" {
			t.Fatalf("%s accepted a short decode", tc.name)
		}

		// A resource fork is an offset table followed by the chunks.
		fork := le.AppendUint32(nil, 8)
		fork = le.AppendUint32(fork, uint32(8+len(tc.encoded)))
		fork = append(fork, tc.encoded...)
		file, err := newCompressedFile(&File{bytes.NewReader(fork), int64(len(fork))}, uint64(len(compressedPlain)), tc.forked)
		if err != nil {
			t.Fatal(tc.name, err)
		}
		out = make([]byte, 100)
		if n, err := file.ReadAt(out, 450); n != 62 || err != io.EOF || !bytes.Equal(out[:n], compressedPlain[450:]) {
			t.Fatalf("%s resource-fork read: %d %v", tc.name, n, err)
		}
		fork[len(fork)-1] ^= 0xff
		fork[8+len(tc.encoded)/2] ^= 0xff
		file, err = newCompressedFile(&File{bytes.NewReader(fork), int64(len(fork))}, uint64(len(compressedPlain)), tc.forked)
		if err != nil {
			t.Fatal(tc.name, err)
		}
		if n, err := file.ReadAt(out, 0); err == nil && bytes.Equal(out[:n], compressedPlain[:n]) {
			t.Fatalf("%s decoded a corrupt chunk to the original data", tc.name)
		}
	}
}

func sealTestBlock(b []byte) { le.PutUint64(b, types.CreateChecksum(b[8:])) }

func rangedFixture() []byte {
	const payloadBlock = 9
	const (
		rootLeaf   = uint16(types.BTNODE_ROOT | types.BTNODE_LEAF)
		rootHashed = uint16(types.BTNODE_ROOT | types.BTNODE_HASHED)
		sealedLeaf = uint16(types.BTNODE_LEAF | types.BTNODE_NOHEADER)
	)
	image := make([]byte, (payloadBlock+2)*readerBlockSize)
	seal := sealTestBlock
	block := func(index int) []byte { return image[index*readerBlockSize : (index+1)*readerBlockSize] }
	b := block(0)
	copy(b[32:], types.NX_MAGIC)
	le.PutUint32(b[36:], readerBlockSize)
	le.PutUint64(b[16:], 1)
	le.PutUint64(b[160:], 1)
	le.PutUint64(b[184:], 100)
	seal(b)
	b = block(1)
	le.PutUint64(b[48:], 2)
	seal(b)
	b = block(3)
	copy(b[32:], types.APFS_MAGIC)
	le.PutUint64(b[128:], 4)
	le.PutUint64(b[136:], 200)
	le.PutUint64(b[1032:], 8)
	le.PutUint64(b[56:], uint64(types.APFS_INCOMPAT_SEALED_VOLUME))
	seal(b)
	b = block(4)
	le.PutUint64(b[48:], 5)
	le.PutUint64(b[16:], 1)
	seal(b)
	type rec struct{ k, v []byte }
	u64s := func(values ...uint64) []byte {
		b := make([]byte, 0, len(values)*8)
		for _, v := range values {
			b = le.AppendUint64(b, v)
		}
		return b
	}
	pair := func(oid, physical uint64) rec { return rec{u64s(oid, 1), u64s(0, physical)} }
	node := func(index int, flags uint16, level uint16, records []rec) {
		b := block(index)
		le.PutUint16(b[32:], flags)
		le.PutUint16(b[34:], level)
		le.PutUint32(b[36:], uint32(len(records)))
		le.PutUint16(b[42:], uint16(len(records)*8))
		kb := 56 + len(records)*8
		kp := 0
		vp := readerBlockSize
		if flags&nodeRoot != 0 {
			vp -= 40
		}
		ve := vp
		for i, r := range records {
			loc := 56 + i*8
			le.PutUint16(b[loc:], uint16(kp))
			le.PutUint16(b[loc+2:], uint16(len(r.k)))
			copy(b[kb+kp:], r.k)
			kp += len(r.k)
			vp -= len(r.v)
			copy(b[vp:], r.v)
			le.PutUint16(b[loc+4:], uint16(ve-vp))
			le.PutUint16(b[loc+6:], uint16(len(r.v)))
		}
		if flags&nodeNoHeader == 0 {
			seal(b)
		}
	}
	node(2, rootLeaf, 0, []rec{pair(100, 3)})
	node(5, rootLeaf, 0, []rec{pair(200, 6), pair(201, 7)})
	key := func(id, kind uint64, size int) []byte {
		b := make([]byte, size)
		le.PutUint64(b, id|kind<<types.OBJ_TYPE_SHIFT)
		return b
	}
	dirrec := func(name string, id uint64, kind uint16) rec {
		k := key(types.ROOT_DIR_INO_NUM, kindDirRec, 12+len(name)+1)
		le.PutUint32(k[8:], uint32(len(name)+1))
		copy(k[12:], name)
		v := make([]byte, 18)
		le.PutUint64(v, id)
		le.PutUint16(v[16:], kind)
		return rec{k, v}
	}
	inode := func(id, size uint64) rec {
		v := make([]byte, 92)
		le.PutUint64(v[8:], id)
		le.PutUint64(v[84:], size)
		return rec{key(id, kindInode, 8), v}
	}
	xattr := func(id uint64, name string, flags uint16, data []byte) rec {
		k := key(id, kindXattr, 10+len(name)+1)
		le.PutUint16(k[8:], uint16(len(name)+1))
		copy(k[10:], name)
		v := le.AppendUint16(le.AppendUint16(nil, flags), uint16(len(data)))
		return rec{k, append(v, data...)}
	}
	// Inode 3 is a plain file, 4 a symbolic link to it, and 5 a decmpfs file
	// whose header attribute lives in data stream 6 instead of the record.
	const cmpPayload = "hello"
	cmpHeader := le.AppendUint32(nil, decmpfsMagic)
	cmpHeader = le.AppendUint32(cmpHeader, cmpAttrUncompressed)
	cmpHeader = le.AppendUint64(cmpHeader, uint64(len(cmpPayload)))
	cmpHeader = append(append(cmpHeader, 0xcc), cmpPayload...)
	node(7, sealedLeaf, 0, []rec{
		dirrec("fixture.loctable", 3, uint16(types.DT_REG)),
		dirrec("link", 4, uint16(types.DT_LNK)),
		dirrec("stream.cmp", 5, uint16(types.DT_REG)),
		inode(3, 7),
		inode(4, 0),
		xattr(4, types.XATTR_SYMLINK_EA_NAME, xattrEmbedded, []byte("fixture.loctable\x00")),
		inode(5, 0),
		xattr(5, types.XATTR_DECMPFS_EA_NAME, xattrStream, u64s(6, uint64(len(cmpHeader)), 0, 0, 0, 0)),
	})
	hash := sha256.Sum256(block(7))
	child := append(u64s(1), hash[:]...)
	node(6, rootHashed, 1, []rec{{key(types.ROOT_DIR_INO_NUM, kindDirRec, 8), child}})
	node(8, rootLeaf, 0, []rec{
		{u64s(3, 0), u64s(7, payloadBlock)},
		{u64s(6, 0), u64s(uint64(len(cmpHeader)), payloadBlock+1)},
	})
	copy(block(payloadBlock), "fixture")
	copy(block(payloadBlock+1), cmpHeader)
	return image
}

func TestReaderSealedVolume(t *testing.T) {
	image := rangedFixture()
	fs, err := OpenReader(bytes.NewReader(image), int64(len(image)))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := fs.ReadDirID(types.ROOT_DIR_INO_NUM)
	if err != nil || len(entries) != 3 {
		t.Fatal(entries, err)
	}
	if entry, err := fs.Lookup("/fixture.loctable"); err != nil || entry != entries[0] {
		t.Fatal(entry, err)
	}
	if _, err := fs.Lookup("/missing"); err == nil {
		t.Fatal("resolved a missing path")
	}
	if root, err := fs.Lookup("/"); err != nil || root.ID != types.ROOT_DIR_INO_NUM {
		t.Fatal(root, err)
	}
	file, err := fs.OpenFileID(entries[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, file.Size)
	if _, err := file.ReadAt(data, 0); err != nil || string(data) != "fixture" {
		t.Fatal(string(data), err)
	}
	if target, err := fs.Readlink(entries[1].ID); err != nil || target != entries[0].Name {
		t.Fatal(target, err)
	}
	if _, err := fs.Readlink(entries[0].ID); err == nil {
		t.Fatal("read a link target from a regular file")
	}
	file, err = fs.OpenFileID(entries[2].ID)
	if err != nil {
		t.Fatal(err)
	}
	data = make([]byte, file.Size)
	if _, err := file.ReadAt(data, 0); err != nil || string(data) != "hello" {
		t.Fatal(string(data), err)
	}
	image[7*readerBlockSize+2000] ^= 1
	fs, err = OpenReader(bytes.NewReader(image), int64(len(image)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fs.ReadDirID(types.ROOT_DIR_INO_NUM); err == nil {
		t.Fatal("accepted a sealed child with an invalid hash")
	}
}

// TestReaderHeaderlessNodes checks that a block may skip its object checksum
// only as a wholly header-less file-system or extent tree node of a volume
// that declares itself sealed.
func TestReaderHeaderlessNodes(t *testing.T) {
	block := func(image []byte, index int) []byte {
		return image[index*readerBlockSize : (index+1)*readerBlockSize]
	}
	for name, corrupt := range map[string]func(image []byte){
		"a volume that is not sealed": func(image []byte) {
			volume := block(image, 3)
			le.PutUint64(volume[56:], 0)
			sealTestBlock(volume)
		},
		"a header-less object-map node": func(image []byte) {
			omap := block(image, 5)
			clear(omap[:32])
			le.PutUint16(omap[32:], le.Uint16(omap[32:])|nodeNoHeader)
		},
		"a partly zero object header": func(image []byte) {
			block(image, 7)[24] = 1
		},
	} {
		image := rangedFixture()
		corrupt(image)
		fs, err := OpenReader(bytes.NewReader(image), int64(len(image)))
		if err == nil {
			_, err = fs.ReadDirID(types.ROOT_DIR_INO_NUM)
		}
		if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
			t.Fatalf("header-less node in %s: error = %v, want checksum mismatch", name, err)
		}
	}
}
