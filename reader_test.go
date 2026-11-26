package apfs

import (
	"bytes"
	"compress/zlib"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"slices"
	"strings"
	"testing"

	"github.com/blacktop/go-apfs/pkg/disk/dmg"
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
		"record count past int32": node(nodeLeaf, 0, 0x80000000),
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
	lzfseData := lzfse.EncodeBuffer(compressedPlain)
	for _, tc := range []struct {
		name           string
		inline, forked uint32
		stored         bool
		encoded        []byte
	}{
		{"LZVN", cmpAttrLZVN, cmpRsrcLZVN, lzvn[0] == 6, lzvn},
		{"LZFSE", cmpAttrLZFSE, cmpRsrcLZFSE, lzfseData[0] != 0x62, lzfseData},
		{"LZBITMAP", cmpAttrLZBitmap, cmpRsrcLZBitmap, lzbitmap[0] == 0xff, lzbitmap},
	} {
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

		fork := testFork(tc.encoded)
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

func testBlock(image []byte, index int) []byte {
	return image[index*readerBlockSize : (index+1)*readerBlockSize]
}

// stripTestHeader turns a block into a header-less node.
func stripTestHeader(b []byte) {
	clear(b[:32])
	le.PutUint16(b[32:], le.Uint16(b[32:])|nodeNoHeader)
}

func openTestReader(t testing.TB, image []byte) *Reader {
	t.Helper()
	r, err := OpenReader(bytes.NewReader(image), int64(len(image)))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// readTestFile opens the file with inode number id and returns it with its
// whole contents.
func readTestFile(t testing.TB, r *Reader, id uint64) (*File, string) {
	t.Helper()
	file, err := r.OpenFileID(id)
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, file.Size)
	if err := readFull(file, data, 0); err != nil {
		t.Fatal(err)
	}
	return file, string(data)
}

// testFork lays chunks out as a decmpfs resource fork: an offset table
// followed by the chunks.
func testFork(chunks ...[]byte) []byte {
	pos := uint32((len(chunks) + 1) * 4)
	fork := le.AppendUint32(nil, pos)
	for _, c := range chunks {
		pos += uint32(len(c))
		fork = le.AppendUint32(fork, pos)
	}
	for _, c := range chunks {
		fork = append(fork, c...)
	}
	return fork
}

func errContains(err error, want string) bool {
	return err != nil && strings.Contains(err.Error(), want)
}

func rangedFixture() []byte { return rangedFixtureNamed("fixture.loctable") }

// rangedFixtureNamed builds the fixture with its first directory entry named
// first.
func rangedFixtureNamed(first string) []byte { return rangedFixtureWith(first, false) }

// ownedFixture is the fixture with owners on its inodes, an inode record for
// the root directory, and a sticky subdirectory "tmp" (inode 7).
func ownedFixture() []byte { return rangedFixtureWith("fixture.loctable", true) }

func rangedFixtureWith(first string, owned bool) []byte {
	const payloadBlock = 9
	const (
		rootLeaf   = uint16(types.BTNODE_ROOT | types.BTNODE_LEAF)
		rootHashed = uint16(types.BTNODE_ROOT | types.BTNODE_HASHED)
		sealedLeaf = uint16(types.BTNODE_LEAF | types.BTNODE_NOHEADER)
	)
	image := make([]byte, (payloadBlock+2)*readerBlockSize)
	seal := sealTestBlock
	block := func(index int) []byte { return testBlock(image, index) }
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
	own := func(r rec, uid, gid uint32) rec {
		le.PutUint32(r.v[72:], uid)
		le.PutUint32(r.v[76:], gid)
		return r
	}
	var extra []rec
	file3, link4 := inode(3, 7, uint16(types.REG)|0755), inode(4, 0, uint16(types.LNK)|0755)
	if owned {
		file3 = own(inode(3, 7, uint16(types.REG)|04755), 0, 80)
		link4 = own(link4, 501, 20)
		extra = []rec{
			dirrec("tmp", 7, uint16(types.DT_DIR)),
			own(inode(types.ROOT_DIR_INO_NUM, 0, uint16(types.DIR)|0750), 0, 80),
			own(inode(7, 0, uint16(types.DIR)|01777), 0, 0),
		}
	}
	node(7, sealedLeaf, 0, append(extra, []rec{
		dirrec(first, 3, uint16(types.DT_REG)),
		dirrec("link", 4, uint16(types.DT_LNK)),
		dirrec("stream.cmp", 5, uint16(types.DT_REG)),
		file3,
		// An embedded resource fork that the plain file never needs.
		xattr(3, types.XATTR_RESOURCEFORK_EA_NAME, xattrEmbedded, []byte("fork")),
		link4,
		xattr(4, types.XATTR_SYMLINK_EA_NAME, xattrEmbedded, []byte("fixture.loctable\x00")),
		inode(5, 0, uint16(types.REG)|0644),
		xattr(5, types.XATTR_DECMPFS_EA_NAME, xattrStream, u64s(6, uint64(len(cmpHeader)), 0, 0, 0, 0)),
	}...))
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
	fs := openTestReader(t, image)
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
	if file, data := readTestFile(t, fs, entries[0].ID); data != "fixture" || file.Mode != 0755 {
		t.Fatal(data, file.Mode)
	}
	if target, err := fs.Readlink(entries[1].ID); err != nil || target != entries[0].Name {
		t.Fatal(target, err)
	}
	if _, err := fs.Readlink(entries[0].ID); err == nil {
		t.Fatal("read a link target from a regular file")
	}
	if file, data := readTestFile(t, fs, entries[2].ID); data != "hello" || file.Mode != 0644 {
		t.Fatal(data, file.Mode)
	}
	testBlock(image, 7)[2000] ^= 1
	fs = openTestReader(t, image)
	if _, err := fs.ReadDirID(types.ROOT_DIR_INO_NUM); err == nil {
		t.Fatal("accepted a sealed child with an invalid hash")
	}
}

// TestReaderOwners checks that the mode and owner in the inode records reach
// a directory itself, the volume root included, each listed child, and files
// and symbolic links.
func TestReaderOwners(t *testing.T) {
	r := openTestReader(t, ownedFixture())
	dir, children, err := r.ReadDirInfoID(types.ROOT_DIR_INO_NUM)
	if err != nil {
		t.Fatal(err)
	}
	if want := (Info{fs.ModeDir | 0750, uint16(types.DIR) | 0750, 0, 80}); dir != want {
		t.Fatalf("root = %+v, want %+v", dir, want)
	}
	want := []EntryInfo{
		{Entry{"fixture.loctable", 3, uint16(types.DT_REG)}, Info{fs.ModeSetuid | 0755, uint16(types.REG) | 04755, 0, 80}},
		{Entry{"link", 4, uint16(types.DT_LNK)}, Info{fs.ModeSymlink | 0755, uint16(types.LNK) | 0755, 501, 20}},
		{Entry{"stream.cmp", 5, uint16(types.DT_REG)}, Info{0644, uint16(types.REG) | 0644, 0, 0}},
		{Entry{"tmp", 7, uint16(types.DT_DIR)}, Info{fs.ModeDir | fs.ModeSticky | 0777, uint16(types.DIR) | 01777, 0, 0}},
	}
	if !slices.Equal(children, want) {
		t.Fatalf("children = %+v, want %+v", children, want)
	}
	// The listing agrees with ReadDirID, and Stat and StatID with the listing.
	entries, err := r.ReadDirID(types.ROOT_DIR_INO_NUM)
	if err != nil || len(entries) != len(children) {
		t.Fatal(entries, err)
	}
	for i, child := range children {
		if child.Entry != entries[i] {
			t.Fatalf("entry %d = %+v, want %+v", i, child.Entry, entries[i])
		}
		if got, err := r.Stat("/" + child.Name); err != nil || got != child {
			t.Fatalf("Stat(%s) = %+v, %v", child.Name, got, err)
		}
		if got, err := r.StatID(child.ID); err != nil || got != child.Info {
			t.Fatalf("StatID(%d) = %+v, %v", child.ID, got, err)
		}
	}
	for _, path := range []string{"/", ""} {
		if root, err := r.Stat(path); err != nil || root.ID != types.ROOT_DIR_INO_NUM || root.Info != dir {
			t.Fatalf("Stat(%q) = %+v, %v", path, root, err)
		}
	}
	// An empty directory still has a mode and an owner of its own.
	if tmp, empty, err := r.ReadDirInfoID(7); err != nil || tmp != want[3].Info || len(empty) != 0 {
		t.Fatal(tmp, empty, err)
	}
	if file, _ := readTestFile(t, r, 3); file.Mode != fs.ModeSetuid|0755 || file.UID != 0 || file.GID != 80 {
		t.Fatal(file.Mode, file.UID, file.GID)
	}
	if _, err := r.Stat("/missing"); err == nil {
		t.Fatal("stat-ed a missing path")
	}
	if _, err := r.StatID(999); err == nil {
		t.Fatal("stat-ed an inode number with no inode record")
	}
	// The plain fixture's root directory has no inode record: its entries
	// still list, but nothing can be said of its mode or owner.
	r = openTestReader(t, rangedFixture())
	if _, _, err := r.ReadDirInfoID(types.ROOT_DIR_INO_NUM); !errContains(err, "no inode record") {
		t.Fatalf("error = %v, want a missing inode record", err)
	}
	if info, err := r.StatID(4); err != nil || info != (Info{fs.ModeSymlink | 0755, uint16(types.LNK) | 0755, 0, 0}) {
		t.Fatal(info, err)
	}
	if _, err := inodeInfo(make([]byte, 91)); err == nil {
		t.Fatal("decoded a short inode record")
	}
}

// TestReaderOwnersImage reads the mode and owner out of a real container,
// testdata/test.dmg, made by a user with uid 501 and gid 20. It holds a root
// directory and one file, with no setuid, setgid or sticky bit anywhere.
func TestReaderOwnersImage(t *testing.T) {
	d, err := dmg.Open("testdata/test.dmg", &dmg.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.Load(); err != nil {
		t.Fatal(err)
	}
	r, err := OpenReader(d, int64(d.GetSize()))
	if err != nil {
		t.Fatal(err)
	}
	dir, children, err := r.ReadDirInfoID(types.ROOT_DIR_INO_NUM)
	if err != nil {
		t.Fatal(err)
	}
	if want := (Info{fs.ModeDir | 0755, uint16(types.DIR) | 0755, 501, 20}); dir != want {
		t.Fatalf("root = %+v, want %+v", dir, want)
	}
	want := EntryInfo{Entry{"README.md", 16, uint16(types.DT_REG)}, Info{0644, uint16(types.REG) | 0644, 501, 20}}
	if len(children) != 1 || children[0] != want {
		t.Fatalf("children = %+v, want %+v", children, want)
	}
	if file, err := r.OpenFileID(want.ID); err != nil || file.Mode != 0644 || file.UID != 501 || file.GID != 20 {
		t.Fatal(file, err)
	}
}

// TestReaderHeaderlessNodes checks that a block may skip its object checksum
// only as a wholly header-less file-system or extent tree node of a volume
// that declares itself sealed.
func TestReaderHeaderlessNodes(t *testing.T) {
	for name, corrupt := range map[string]func(image []byte){
		"a volume that is not sealed": func(image []byte) {
			volume := testBlock(image, 3)
			le.PutUint64(volume[56:], 0)
			sealTestBlock(volume)
		},
		"a header-less object-map node": func(image []byte) {
			stripTestHeader(testBlock(image, 5))
		},
		"a partly zero object header": func(image []byte) {
			testBlock(image, 7)[24] = 1
		},
	} {
		image := rangedFixture()
		corrupt(image)
		fs, err := OpenReader(bytes.NewReader(image), int64(len(image)))
		if err == nil {
			_, err = fs.ReadDirID(types.ROOT_DIR_INO_NUM)
		}
		if !errContains(err, "checksum mismatch") {
			t.Fatalf("header-less node in %s: error = %v, want checksum mismatch", name, err)
		}
	}
}

// TestReaderBlockGate covers the cells of the block cache's checksum gate that
// the volume-level tests do not reach.
func TestReaderBlockGate(t *testing.T) {
	// A header-less node cached for a tree read is still refused to a caller
	// that requires a checksum.
	fs := openTestReader(t, rangedFixture())
	if _, err := fs.treeBlock(7, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.block(7); !errContains(err, "checksum mismatch") {
		t.Fatalf("cached header-less node served to a checksummed caller: %v", err)
	}

	// A checksummed node is still held to its parent's hash, also from the cache.
	image := rangedFixture()
	fs = openTestReader(t, image)
	sum := sha256.Sum256(testBlock(image, 6))
	if _, err := fs.treeBlock(6, sum[:]); err != nil {
		t.Fatal(err)
	}
	sum[0] ^= 1
	if _, err := fs.treeBlock(6, sum[:]); !errContains(err, "hash mismatch") {
		t.Fatalf("error = %v, want child hash mismatch", err)
	}

	// A header-less extent-tree node has no parent hash and is accepted on a
	// sealed volume, fresh and from the cache.
	image = rangedFixture()
	stripTestHeader(testBlock(image, 8))
	fs = openTestReader(t, image)
	for pass := range 2 {
		if _, data := readTestFile(t, fs, 3); data != "fixture" {
			t.Fatalf("pass %d: %q", pass, data)
		}
	}

	// A corrupt checksummed block is rejected, and stays rejected on retry.
	image = rangedFixture()
	testBlock(image, 8)[2000] ^= 1
	fs = openTestReader(t, image)
	for pass := range 2 {
		if _, err := fs.OpenFileID(3); !errContains(err, "checksum mismatch") {
			t.Fatalf("pass %d: error = %v, want checksum mismatch", pass, err)
		}
	}
	image = rangedFixture()
	testBlock(image, 3)[2000] ^= 1 // volume superblock
	if _, err := OpenReader(bytes.NewReader(image), int64(len(image))); !errContains(err, "checksum mismatch") {
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
	fs := openTestReader(t, image)
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
	const padding = 4
	padded := func() []byte { return append(rangedFixture(), make([]byte, padding*readerBlockSize)...) }
	image := padded()
	blocks := uint64(len(image)/readerBlockSize - padding)
	fs := openTestReader(t, image)
	if file, err := fs.OpenFileID(3); err != nil || file.Size != 7 {
		t.Fatal(file, err)
	}
	if _, err := fs.block(blocks); !errContains(err, "outside image") {
		t.Fatalf("read a block past the container: %v", err)
	}
	if _, err := fs.extentFile([]readerExtent{{logical: 0, physical: blocks - 1, length: 2 * readerBlockSize}}, 1); err == nil {
		t.Fatal("accepted an extent that runs past the container")
	}

	image = padded()
	volume := testBlock(image, 3)
	le.PutUint64(volume[128:], blocks) // object map in the padding
	sealTestBlock(volume)
	if _, err := OpenReader(bytes.NewReader(image), int64(len(image))); !errContains(err, "outside image") {
		t.Fatalf("followed a pointer past the container: %v", err)
	}
}

func TestReaderNames(t *testing.T) {
	for _, name := range []string{"..", ".", "a/b", ""} {
		fs := openTestReader(t, rangedFixtureNamed(name))
		if _, err := fs.ReadDirID(types.ROOT_DIR_INO_NUM); !errContains(err, "unsafe APFS name") {
			t.Fatalf("name %q: error = %v, want unsafe name", name, err)
		}
		if _, err := fs.Lookup("/link"); err == nil {
			t.Fatalf("name %q: Lookup scanned past an unsafe entry", name)
		}
	}
	// A backslash is an ordinary character in an APFS name.
	fs := openTestReader(t, rangedFixtureNamed(`a\b`))
	if entry, err := fs.Lookup(`/a\b`); err != nil || entry.ID != 3 {
		t.Fatal(entry, err)
	}
}

func TestReaderOpenFileErrors(t *testing.T) {
	fs := openTestReader(t, rangedFixture())
	if _, err := fs.OpenFileID(999); err == nil {
		t.Fatal("opened an inode number with no inode record")
	}
	fork := &File{ReaderAt: bytes.NewReader(nil)}
	if _, err := newCompressedFile(fork, 0, cmpRsrcLZVN); err == nil || errContains(err, "unsupported resource compression") {
		t.Fatalf("error = %v, want a size error", err)
	}
	if _, err := newCompressedFile(fork, 1, uint32(types.CMP_MAX)); !errContains(err, "unsupported resource compression") {
		t.Fatalf("error = %v, want an unsupported-method error", err)
	}
}

// TestReaderResourceTable checks the offset table shared by the LZVN, LZFSE and
// LZBITMAP forks. Only the table is validated at open, so chunk bytes are placeholders.
func TestReaderResourceTable(t *testing.T) {
	open := func(fork []byte) error {
		source := &File{ReaderAt: bytes.NewReader(fork), Size: int64(len(fork))}
		_, err := newCompressedFile(source, readerChunkSize+1, cmpRsrcLZVN)
		return err
	}
	fork := testFork([]byte{6, 'A'}, []byte{6, 'B'})
	if err := open(fork); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		offset int
		value  uint32
		want   string
	}{
		{0, 8, "invalid resource table"},                     // first chunk starts inside the table
		{8, uint32(len(fork)) + 1, "invalid resource table"}, // last chunk ends past the fork
		{4, 12, "invalid resource offsets"},                  // first chunk is empty
	} {
		bad := bytes.Clone(fork)
		le.PutUint32(bad[tc.offset:], tc.value)
		if err := open(bad); !errContains(err, tc.want) {
			t.Fatalf("offset %d: error = %v, want %q", tc.offset, err, tc.want)
		}
	}
	oversized := testFork(make([]byte, readerChunkSize+2), []byte{6, 'B'})
	if err := open(oversized); !errContains(err, "invalid resource offsets") {
		t.Fatalf("oversized chunk: error = %v, want an offsets error", err)
	}
	if err := open(fork[:8]); !errors.Is(err, io.EOF) {
		t.Fatalf("truncated table: error = %v, want io.EOF", err)
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

func TestReaderZlibResource(t *testing.T) {
	plain := bytes.Repeat([]byte("A"), readerChunkSize)
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	if _, err := w.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	// A zlib block, the largest stored block (marker plus 64 KiB), and a short stored block.
	stored := append([]byte{0xff}, bytes.Repeat([]byte("B"), readerChunkSize)...)
	blocks := [][]byte{buf.Bytes(), stored, {0xff, 'E', 'N', 'D'}}
	pos := 4 + 8*len(blocks)
	resource := le.AppendUint32(nil, uint32(len(blocks)))
	for _, block := range blocks {
		resource = le.AppendUint32(resource, uint32(pos))
		resource = le.AppendUint32(resource, uint32(len(block)))
		pos += len(block)
	}
	for _, block := range blocks {
		resource = append(resource, block...)
	}
	const dataOffset = 256
	fork := make([]byte, dataOffset)
	binary.BigEndian.PutUint32(fork, dataOffset)
	binary.BigEndian.PutUint32(fork[8:], uint32(len(resource)+4))
	fork = binary.BigEndian.AppendUint32(fork, uint32(len(resource)))
	fork = append(fork, resource...)
	open := func(data []byte) (*File, error) {
		source := &File{ReaderAt: bytes.NewReader(data), Size: int64(len(data))}
		return newCompressedFile(source, 2*readerChunkSize+3, cmpRsrcZlib)
	}
	file, err := open(fork)
	if err != nil {
		t.Fatal(err)
	}
	var out [7]byte
	n, err := file.ReadAt(out[:], readerChunkSize-3)
	if n != 7 || err != nil || string(out[:]) != "AAABBBB" {
		t.Fatalf("zlib-to-stored read = %q, %d, %v", out, n, err)
	}
	n, err = file.ReadAt(out[:], 2*readerChunkSize-3)
	if n != 6 || err != io.EOF || string(out[:n]) != "BBBEND" {
		t.Fatalf("read to end = %q, %d, %v", out, n, err)
	}
	be := binary.BigEndian
	for _, tc := range []struct {
		offset int
		order  binary.ByteOrder
		value  uint32
		want   string
	}{
		{0, be, 8, "data area"},                                  // data area overlaps the header
		{8, be, uint32(len(fork) - dataOffset + 1), "data area"}, // data area extends past the fork
		{8, be, 8, "data area"},                                  // data area too small for its table
		{dataOffset, be, uint32(len(resource) + 1), "table"},     // resource overruns the data area
		{dataOffset + 4, le, 2, "table"},                         // count disagrees with uncompressed size
		{dataOffset + 8, le, 0, "chunk 0"},                       // block starts inside the table
		{dataOffset + 12, le, 0, "chunk 0"},                      // block is empty
		{dataOffset + 12, le, readerChunkSize + 2, "chunk 0"},    // chunk() would allocate past a stored block
		{dataOffset + 28, le, 5, "chunk 2"},                      // last block ends past the resource
	} {
		bad := bytes.Clone(fork)
		tc.order.PutUint32(bad[tc.offset:], tc.value)
		if _, err := open(bad); !errContains(err, "invalid zlib resource "+tc.want) {
			t.Fatalf("field at offset %d: error = %v, want %q", tc.offset, err, tc.want)
		}
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
	fork := testFork(chunks...)
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
	if _, data := readTestFile(t, fs, 5); data != "hello" {
		t.Fatal(data)
	}

	// A resource fork whose last chunk ends exactly at the end of its source.
	fork := testFork(lzfse.EncodeBuffer(compressedPlain))
	file, err := newCompressedFile(&File{ReaderAt: eofReaderAt{fork}, Size: int64(len(fork))}, uint64(len(compressedPlain)), cmpRsrcLZFSE)
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, len(compressedPlain))
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
		// Fit other sizes to one block so that every mutation reaches the parser.
		if len(data) != readerBlockSize {
			if _, err := parseReaderNode(data); err == nil {
				t.Fatalf("parsed a node of %d bytes", len(data))
			}
			data = append(make([]byte, 0, readerBlockSize), data[:min(len(data), readerBlockSize)]...)[:readerBlockSize]
		}
		node, err := parseReaderNode(data)
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
	buf := make([]byte, 1<<16) // read scratch; its contents are never checked
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
