package apfs

import (
	"bytes"
	"compress/zlib"
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
		if _, err := decodeInline(tc.encoded, uint64(len(compressedPlain))+1, tc.inline); err == nil {
			t.Fatalf("%s accepted a short decode", tc.name)
		}

		// A resource fork is an offset table followed by the chunks.
		fork := le.AppendUint32(nil, 8)
		fork = le.AppendUint32(fork, uint32(8+len(tc.encoded)))
		fork = append(fork, tc.encoded...)
		file, err := newCompressedFile(&File{ReaderAt: bytes.NewReader(fork), Size: int64(len(fork))}, uint64(len(compressedPlain)), tc.forked)
		if err != nil {
			t.Fatal(tc.name, err)
		}
		out = make([]byte, 100)
		if n, err := file.ReadAt(out, 450); n != 62 || err != io.EOF || !bytes.Equal(out[:n], compressedPlain[450:]) {
			t.Fatalf("%s resource-fork read: %d %v", tc.name, n, err)
		}
		fork[len(fork)-1] ^= 0xff
		fork[8+len(tc.encoded)/2] ^= 0xff
		file, err = newCompressedFile(&File{ReaderAt: bytes.NewReader(fork), Size: int64(len(fork))}, uint64(len(compressedPlain)), tc.forked)
		if err != nil {
			t.Fatal(tc.name, err)
		}
		if n, err := file.ReadAt(out, 0); err == nil && bytes.Equal(out[:n], compressedPlain[:n]) {
			t.Fatalf("%s decoded a corrupt chunk to the original data", tc.name)
		}
	}
}

func sealTestBlock(b []byte) { le.PutUint64(b, types.CreateChecksum(b[8:])) }

func rangedFixture() []byte { return rangedFixtureNamed("fixture.loctable") }

// rangedFixtureNamed builds the fixture with its first directory entry named
// first.
func rangedFixtureNamed(first string) []byte {
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
	le.PutUint64(b[40:], payloadBlock+2)
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
	inode := func(id, size uint64, mode uint16) rec {
		v := make([]byte, 92)
		le.PutUint64(v[8:], id)
		le.PutUint16(v[80:], mode)
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
		dirrec(first, 3, uint16(types.DT_REG)),
		dirrec("link", 4, uint16(types.DT_LNK)),
		dirrec("stream.cmp", 5, uint16(types.DT_REG)),
		inode(3, 7, 0100755),
		// An embedded resource fork that the plain file never needs.
		xattr(3, types.XATTR_RESOURCEFORK_EA_NAME, xattrEmbedded, []byte("fork")),
		inode(4, 0, 0120755),
		xattr(4, types.XATTR_SYMLINK_EA_NAME, xattrEmbedded, []byte("fixture.loctable\x00")),
		inode(5, 0, 0100644),
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
	if file.Mode != 0755 {
		t.Fatal(file.Mode)
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
	if file.Mode != 0644 {
		t.Fatal(file.Mode)
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

// TestReaderBlockGate covers the cells of the block cache's checksum gate that
// the volume-level tests do not reach.
func TestReaderBlockGate(t *testing.T) {
	open := func(image []byte) *Reader {
		t.Helper()
		fs, err := OpenReader(bytes.NewReader(image), int64(len(image)))
		if err != nil {
			t.Fatal(err)
		}
		return fs
	}
	mismatch := func(err error, want string) bool { return err != nil && strings.Contains(err.Error(), want) }

	// A header-less node cached for a tree read is still refused to a caller
	// that requires a checksum.
	fs := open(rangedFixture())
	if _, err := fs.block(7, nil, true); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.block(7, nil, false); !mismatch(err, "checksum mismatch") {
		t.Fatalf("cached header-less node served to a checksummed caller: %v", err)
	}

	// A checksummed node is still held to its parent's hash, also from the cache.
	image := rangedFixture()
	fs = open(image)
	sum := sha256.Sum256(image[6*readerBlockSize : 7*readerBlockSize])
	if _, err := fs.block(6, sum[:], true); err != nil {
		t.Fatal(err)
	}
	sum[0] ^= 1
	if _, err := fs.block(6, sum[:], true); !mismatch(err, "hash mismatch") {
		t.Fatalf("error = %v, want child hash mismatch", err)
	}

	// A header-less extent-tree node has no parent hash and is accepted on a
	// sealed volume, fresh and from the cache.
	image = rangedFixture()
	node := image[8*readerBlockSize : 9*readerBlockSize]
	clear(node[:32])
	le.PutUint16(node[32:], le.Uint16(node[32:])|nodeNoHeader)
	fs = open(image)
	for pass := range 2 {
		file, err := fs.OpenFileID(3)
		if err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
		data := make([]byte, file.Size)
		if _, err := file.ReadAt(data, 0); err != nil || string(data) != "fixture" {
			t.Fatalf("pass %d: %q, %v", pass, data, err)
		}
	}

	// A corrupt checksummed block is rejected, and stays rejected on retry.
	image = rangedFixture()
	image[8*readerBlockSize+2000] ^= 1
	fs = open(image)
	for pass := range 2 {
		if _, err := fs.OpenFileID(3); !mismatch(err, "checksum mismatch") {
			t.Fatalf("pass %d: error = %v, want checksum mismatch", pass, err)
		}
	}
	image = rangedFixture()
	image[3*readerBlockSize+2000] ^= 1 // volume superblock
	if _, err := OpenReader(bytes.NewReader(image), int64(len(image))); !mismatch(err, "checksum mismatch") {
		t.Fatalf("error = %v, want checksum mismatch", err)
	}
}

func TestReaderLatestCheckpoint(t *testing.T) {
	image := rangedFixture()
	n := len(image) / readerBlockSize
	good := bytes.Clone(image[:readerBlockSize])
	le.PutUint64(good[16:], 2)
	le.PutUint64(good[40:], uint64(n+2))
	sealTestBlock(good)
	bad := bytes.Clone(good) // newer, but its checksum is stale: must be ignored
	le.PutUint64(bad[16:], 3)
	le.PutUint64(bad[160:], 0)
	image = append(append(image, good...), bad...)
	b := image[:readerBlockSize]
	le.PutUint32(b[104:], 2)
	le.PutUint64(b[112:], uint64(n))
	le.PutUint64(b[160:], 0) // block 0 alone cannot open
	sealTestBlock(b)
	fs, err := OpenReader(bytes.NewReader(image), int64(len(image)))
	if err != nil {
		t.Fatal(err)
	}
	if entries, err := fs.ReadDirID(types.ROOT_DIR_INO_NUM); err != nil || len(entries) != 3 {
		t.Fatal(entries, err)
	}
}

func TestReaderRejectsUnsupportedContainers(t *testing.T) {
	for name, mutate := range map[string]func(image []byte) []byte{
		"a second volume": func(image []byte) []byte {
			le.PutUint64(image[192:], 101)
			sealTestBlock(image[:readerBlockSize])
			return image
		},
		"8 KiB blocks": func(image []byte) []byte {
			le.PutUint32(image[36:], 8192)
			sealTestBlock(image[:readerBlockSize])
			return image
		},
		"a truncated image": func(image []byte) []byte { return image[:readerBlockSize-1] },
		"no blocks": func(image []byte) []byte {
			le.PutUint64(image[40:], 0)
			sealTestBlock(image[:readerBlockSize])
			return image
		},
		"more blocks than the image": func(image []byte) []byte {
			le.PutUint64(image[40:], uint64(len(image)/readerBlockSize)+1)
			sealTestBlock(image[:readerBlockSize])
			return image
		},
	} {
		image := mutate(rangedFixture())
		if _, err := OpenReader(bytes.NewReader(image), int64(len(image))); err == nil {
			t.Fatalf("opened a container with %s", name)
		}
	}
}

// A size that is only an upper bound must not widen the container: metadata
// and extents aimed past its block count fail the bounds checks.
func TestReaderContainerBlockCount(t *testing.T) {
	padded := func() []byte { return append(rangedFixture(), make([]byte, 4*readerBlockSize)...) }
	image := padded()
	blocks := uint64(len(rangedFixture()) / readerBlockSize)
	fs, err := OpenReader(bytes.NewReader(image), int64(len(image)))
	if err != nil {
		t.Fatal(err)
	}
	if file, err := fs.OpenFileID(3); err != nil || file.Size != 7 {
		t.Fatal(file, err)
	}
	if _, err := fs.block(blocks, nil, false); err == nil || !strings.Contains(err.Error(), "outside image") {
		t.Fatalf("read a block past the container: %v", err)
	}
	if _, err := fs.extentFile([]readerExtent{{logical: 0, physical: blocks - 1, length: 2 * readerBlockSize}}, 1); err == nil {
		t.Fatal("accepted an extent that runs past the container")
	}

	image = padded()
	volume := image[3*readerBlockSize : 4*readerBlockSize]
	le.PutUint64(volume[128:], blocks) // object map in the padding
	sealTestBlock(volume)
	if _, err := OpenReader(bytes.NewReader(image), int64(len(image))); err == nil || !strings.Contains(err.Error(), "outside image") {
		t.Fatalf("followed a pointer past the container: %v", err)
	}
}

func TestReaderNames(t *testing.T) {
	for _, name := range []string{"..", ".", "a/b", ""} {
		image := rangedFixtureNamed(name)
		fs, err := OpenReader(bytes.NewReader(image), int64(len(image)))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fs.ReadDirID(types.ROOT_DIR_INO_NUM); err == nil || !strings.Contains(err.Error(), "unsafe APFS name") {
			t.Fatalf("name %q: error = %v, want unsafe name", name, err)
		}
		if _, err := fs.Lookup("/link"); err == nil {
			t.Fatalf("name %q: Lookup scanned past an unsafe entry", name)
		}
	}
	// A backslash is an ordinary character in an APFS name.
	image := rangedFixtureNamed(`a\b`)
	fs, err := OpenReader(bytes.NewReader(image), int64(len(image)))
	if err != nil {
		t.Fatal(err)
	}
	if entry, err := fs.Lookup(`/a\b`); err != nil || entry.ID != 3 {
		t.Fatal(entry, err)
	}
}

func TestReaderOpenFileErrors(t *testing.T) {
	image := rangedFixture()
	fs, err := OpenReader(bytes.NewReader(image), int64(len(image)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fs.OpenFileID(999); err == nil {
		t.Fatal("opened an inode number with no inode record")
	}
	fork := &File{ReaderAt: bytes.NewReader(nil)}
	if _, err := newCompressedFile(fork, 0, cmpRsrcLZVN); err == nil || strings.Contains(err.Error(), "unsupported resource compression") {
		t.Fatalf("error = %v, want a size error", err)
	}
	if _, err := newCompressedFile(fork, 1, uint32(types.CMP_RSRC_ZLIB)); err == nil || !strings.Contains(err.Error(), "unsupported resource compression") {
		t.Fatalf("error = %v, want an unsupported-method error", err)
	}
}

func TestReaderDstreamSize(t *testing.T) {
	inode := make([]byte, 92)
	le.PutUint64(inode[84:], 1)        // the fixed field loses to the data-stream field
	inode = le.AppendUint16(inode, 2)  // number of extended fields
	inode = le.AppendUint16(inode, 48) // bytes they use
	inode = append(inode, byte(types.INO_EXT_TYPE_NAME), 0)
	inode = le.AppendUint16(inode, 5)
	inode = append(inode, byte(types.INO_EXT_TYPE_DSTREAM), 0)
	inode = le.AppendUint16(inode, 40)
	inode = append(inode, "name\x00\x00\x00\x00"...) // padded to 8 bytes
	stream := make([]byte, 40)
	le.PutUint64(stream, 12345)
	inode = append(inode, stream...)
	if size, err := dstreamSize(inode); err != nil || size != 12345 {
		t.Fatal(size, err)
	}
	if _, err := dstreamSize(inode[:len(inode)-1]); err == nil {
		t.Fatal("accepted a truncated data-stream field")
	}
	if _, err := dstreamSize(inode[:94]); err == nil {
		t.Fatal("accepted a truncated field table")
	}
}

func TestReaderNodeCountOverflow(t *testing.T) {
	data := make([]byte, readerBlockSize)
	le.PutUint16(data[32:], nodeLeaf)
	le.PutUint32(data[36:], 0x80000000) // negative as a 32-bit int
	if _, err := parseReaderNode(data); err == nil {
		t.Fatal("accepted a node with an out-of-range record count")
	}
}

func TestReaderInlineZlibStream(t *testing.T) {
	plain := bytes.Repeat([]byte("zlib "), 100)
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	w.Write(plain)
	w.Close()
	out, err := decodeInline(buf.Bytes(), uint64(len(plain)), cmpAttrZlib)
	if err != nil || !bytes.Equal(out, plain) {
		t.Fatal(err)
	}
	for _, size := range []uint64{uint64(len(plain)) - 1, uint64(len(plain)) + 1} {
		if _, err := decodeInline(buf.Bytes(), size, cmpAttrZlib); err == nil {
			t.Fatalf("accepted declared size %d for %d decoded bytes", size, len(plain))
		}
	}
}

func TestReaderLZBitmapShortStream(t *testing.T) {
	// A magic and a terminal chunk decode to nothing, which must not satisfy
	// a declared size of 100.
	short := []byte{0x5a, 0x42, 0x4d, 0x09, 6, 0, 0, 0, 0, 0}
	if got, err := decodeReaderBlock(short, 100, cmpRsrcLZBitmap); err == nil {
		t.Fatalf("accepted a short LZBITMAP stream, returned %d bytes", len(got))
	}
}

func TestReaderMultiChunkFork(t *testing.T) {
	plain := make([]byte, 2*readerChunkSize+1000)
	for i := range plain {
		plain[i] = byte(i % 251 * (i/70000 + 1))
	}
	var chunks [][]byte
	for off := 0; off < len(plain); off += readerChunkSize {
		end := min(off+readerChunkSize, len(plain))
		if off == readerChunkSize { // a stored chunk at full size
			chunks = append(chunks, append([]byte{0x06}, plain[off:end]...))
			continue
		}
		chunks = append(chunks, lzfse.EncodeBuffer(plain[off:end]))
	}
	pos := uint32((len(chunks) + 1) * 4)
	fork := le.AppendUint32(nil, pos)
	for _, c := range chunks {
		pos += uint32(len(c))
		fork = le.AppendUint32(fork, pos)
	}
	for _, c := range chunks {
		fork = append(fork, c...)
	}
	file, err := newCompressedFile(&File{ReaderAt: bytes.NewReader(fork), Size: int64(len(fork))}, uint64(len(plain)), cmpRsrcLZFSE)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]byte, len(plain))
	if n, err := file.ReadAt(out, 0); n != len(plain) || err != nil || !bytes.Equal(out, plain) {
		t.Fatal(n, err)
	}
	out = make([]byte, 200)
	if n, err := file.ReadAt(out, readerChunkSize-100); n != 200 || err != nil || !bytes.Equal(out, plain[readerChunkSize-100:readerChunkSize+100]) {
		t.Fatal(n, err)
	}
	if n, err := file.ReadAt(out, int64(len(plain))); n != 0 || err != io.EOF {
		t.Fatal(n, err)
	}
}

// eofReaderAt returns io.EOF together with the data for a read that ends at
// the end of its input, as io.ReaderAt permits.
type eofReaderAt struct{ data []byte }

func (r eofReaderAt) ReadAt(p []byte, off int64) (int, error) {
	n := copy(p, r.data[off:])
	if n < len(p) || off+int64(n) == int64(len(r.data)) {
		return n, io.EOF
	}
	return n, nil
}

func TestReaderSourceEOFWithData(t *testing.T) {
	// The last block of the fixture holds the stream-stored decmpfs header.
	image := rangedFixture()
	fs, err := OpenReader(eofReaderAt{image}, int64(len(image)))
	if err != nil {
		t.Fatal(err)
	}
	file, err := fs.OpenFileID(5)
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, file.Size)
	if _, err := file.ReadAt(data, 0); err != nil || string(data) != "hello" {
		t.Fatal(string(data), err)
	}

	// A resource fork whose last chunk ends exactly at the end of its source.
	encoded := lzfse.EncodeBuffer(compressedPlain)
	fork := le.AppendUint32(nil, 8)
	fork = append(le.AppendUint32(fork, uint32(8+len(encoded))), encoded...)
	file, err = newCompressedFile(&File{ReaderAt: eofReaderAt{fork}, Size: int64(len(fork))}, uint64(len(compressedPlain)), cmpRsrcLZFSE)
	if err != nil {
		t.Fatal(err)
	}
	data = make([]byte, len(compressedPlain))
	if _, err := file.ReadAt(data, 0); err != nil || !bytes.Equal(data, compressedPlain) {
		t.Fatal(err)
	}
}

// resealFuzzImage restores the object checksum of every block that has an
// object header, so that mutated metadata reaches the parsers instead of
// stopping at the checksum.
func resealFuzzImage(image []byte) {
	for off := 0; off+readerBlockSize <= len(image); off += readerBlockSize {
		if block := image[off : off+readerBlockSize]; [32]byte(block[:32]) != [32]byte{} {
			sealTestBlock(block)
		}
	}
}

func FuzzParseReaderNode(f *testing.F) {
	image := rangedFixture()
	for off := 0; off < len(image); off += readerBlockSize {
		f.Add(image[off : off+readerBlockSize])
	}
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		if _, err := parseReaderNode(data); err == nil && len(data) != readerBlockSize {
			t.Fatalf("parsed a node of %d bytes", len(data))
		}
		// Fit the input to one block so that every mutation reaches the parser.
		block := make([]byte, readerBlockSize)
		copy(block, data)
		node, err := parseReaderNode(block)
		if err != nil {
			return
		}
		if len(node.records) > readerMaxNodeRecords {
			t.Fatalf("%d records in one node", len(node.records))
		}
		for _, rec := range node.records {
			if len(rec.key) < 8 {
				t.Fatalf("record key of %d bytes", len(rec.key))
			}
			// The accessors every caller uses must not read out of bounds.
			rec.object()
			rec.kind()
			rec.treeID(true)
		}
	})
}

func FuzzOpenReader(f *testing.F) {
	f.Add(rangedFixture())
	f.Add(rangedFixtureNamed(`a\b`))
	f.Add(make([]byte, readerBlockSize))
	f.Fuzz(func(t *testing.T, image []byte) {
		resealFuzzImage(image)
		fs, err := OpenReader(bytes.NewReader(image), int64(len(image)))
		if err != nil {
			return
		}
		entries, err := fs.ReadDirID(types.ROOT_DIR_INO_NUM)
		if err != nil {
			return
		}
		buf := make([]byte, 1<<16)
		for _, entry := range entries[:min(len(entries), 8)] {
			if found, err := fs.Lookup("/" + entry.Name); err == nil && found.Name != entry.Name {
				t.Fatalf("Lookup(%q) returned %q", entry.Name, found.Name)
			}
			fs.Readlink(entry.ID)
			file, err := fs.OpenFileID(entry.ID)
			if err != nil {
				continue
			}
			if file.Size < 0 {
				t.Fatalf("file size %d", file.Size)
			}
			// Sizes come from the image, so read the two ends, not the whole.
			for _, off := range []int64{0, max(0, file.Size-int64(len(buf)))} {
				if n, err := file.ReadAt(buf, off); n < 0 || n > len(buf) || int64(n) > file.Size-off || (err == nil && n != len(buf)) {
					t.Fatalf("ReadAt(%d) of a %d-byte file = %d, %v", off, file.Size, n, err)
				}
			}
		}
	})
}

func FuzzDecodeReaderBlock(f *testing.F) {
	lzvn, _ := hex.DecodeString(lzvnFixture)
	lzbitmap, _ := hex.DecodeString(lzbitmapFixture)
	f.Add(lzvn, uint16(len(compressedPlain)), cmpRsrcLZVN)
	f.Add(lzfse.EncodeBuffer(compressedPlain), uint16(len(compressedPlain)), cmpRsrcLZFSE)
	f.Add(lzbitmap, uint16(len(compressedPlain)), cmpRsrcLZBitmap)
	f.Add([]byte{6, 1, 2}, uint16(2), cmpRsrcLZVN)
	f.Add([]byte{0x0f, 1, 2}, uint16(2), cmpAttrZlib)
	f.Fuzz(func(t *testing.T, data []byte, size uint16, method uint32) {
		for _, decode := range []func() ([]byte, error){
			func() ([]byte, error) { return decodeReaderBlock(data, int(size), method) },
			func() ([]byte, error) { return decodeInline(data, uint64(size), method) },
		} {
			if out, err := decode(); err == nil && len(out) != int(size) {
				t.Fatalf("method %d decoded %d bytes for a declared size of %d", method, len(out), size)
			}
		}
	})
}
