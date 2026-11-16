package apfs

import (
	"bytes"
	"cmp"
	"compress/zlib"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

	"github.com/blacktop/go-apfs/types"
	"github.com/blacktop/lzfse-cgo"
	lru "github.com/hashicorp/golang-lru/v2"
)

const (
	readerBlockSize      = types.NX_DEFAULT_BLOCK_SIZE // only 4 KiB-block containers are supported
	readerBlockCache     = 4096                        // blocks cached per Reader
	readerMaxCheckpoints = 4096                        // checkpoint descriptor blocks scanned at open
	readerMaxNodes       = 4096                        // nodes visited per record lookup
	readerMaxDepth       = 32                          // B-tree levels
	readerChunkSize      = 65536                       // decmpfs resource chunk
	readerChunkCache     = 128                         // decoded chunks cached per compressed File
	readerMaxDecoded     = 64 << 20                    // inline or single-chunk decoded size
	readerMaxCompressed  = 64 << 30                    // decoded size of a resource-compressed file
	readerMaxLink        = 4096                        // symbolic link target

	decmpfsMagic = 0x636d7066 // types.DECMPFS_MAGIC ("cmpf") as a little-endian uint32
)

// Builtin-typed views of the constants in package types, whose named types
// are unexported and so cannot be used for fields or parameters here.
const (
	nodeRoot     = uint16(types.BTNODE_ROOT)
	nodeLeaf     = uint16(types.BTNODE_LEAF)
	nodeFixedKV  = uint16(types.BTNODE_FIXED_KV_SIZE)
	nodeHashed   = uint16(types.BTNODE_HASHED)
	nodeNoHeader = uint16(types.BTNODE_NOHEADER)

	kindInode      = uint64(types.APFS_TYPE_INODE)
	kindXattr      = uint64(types.APFS_TYPE_XATTR)
	kindFileExtent = uint64(types.APFS_TYPE_FILE_EXTENT)
	kindDirRec     = uint64(types.APFS_TYPE_DIR_REC)

	xattrStream   = uint16(types.XATTR_DATA_STREAM)
	xattrEmbedded = uint16(types.XATTR_DATA_EMBEDDED)

	cmpAttrZlib         = uint32(types.CMP_ATTR_ZLIB)
	cmpAttrLZVN         = uint32(types.CMP_ATTR_LZVN)
	cmpRsrcLZVN         = uint32(types.CMP_RSRC_LZVN)
	cmpAttrUncompressed = uint32(types.CMP_ATTR_UNCOMPRESSED)
	cmpAttrLZFSE        = uint32(types.CMP_ATTR_LZFSE)
	cmpRsrcLZFSE        = uint32(types.CMP_RSRC_LZFSE)
	cmpAttrLZBitmap     = uint32(types.CMP_ATTR_LZBITMAP)
	cmpRsrcLZBitmap     = uint32(types.CMP_RSRC_LZBITMAP)
)

var le = binary.LittleEndian

// Reader reads a single-volume, 4 KiB-block APFS container, such as the sealed
// system volume of a restore image, from an io.ReaderAt without mounting it.
//
// Every block must pass its object checksum. The one exception is a header-less
// node of the file-system or file-extent tree on a volume that declares
// APFS_INCOMPAT_SEALED_VOLUME. Children of a hashed file-system tree node are
// also verified against the SHA-256 their parent holds. The volume seal itself
// is not verified.
//
// Memory use is bounded by fixed-size caches. A Reader is not safe for
// concurrent use and never closes its source.
type Reader struct {
	source      io.ReaderAt
	size        int64
	omap        uint64
	transaction uint64
	rootOID     uint64
	root        uint64
	extentRoot  uint64
	sealed      bool // the volume sets APFS_INCOMPAT_SEALED_VOLUME
	cache       *lru.Cache[uint64, *readerBlock]
}

// readerBlock is a cached block. checksummed records whether its object
// checksum verified, so a header-less node cached for a sealed tree is never
// handed to a caller that requires a checksum. sum is its SHA-256 once a
// sealed parent has asked for it.
type readerBlock struct {
	data        []byte
	checksummed bool
	sum         [sha256.Size]byte
	summed      bool
}

type record struct{ key, value []byte }

func (r record) object() uint64 { return le.Uint64(r.key) & types.OBJ_ID_MASK }
func (r record) kind() uint64   { return le.Uint64(r.key) >> types.OBJ_TYPE_SHIFT }

// treeID is the object a record sorts under: the whole first key word in the
// physical extent tree, the masked object ID in the file-system tree.
func (r record) treeID(physical bool) uint64 {
	if physical {
		return le.Uint64(r.key)
	}
	return r.object()
}

type readerNode struct {
	flags, level uint16
	records      []record
}

// Entry is a directory entry. ID is the inode number to pass to ReadDirID or
// OpenFileID, and Type is a Unix d_type value such as types.DT_DIR.
type Entry struct {
	Name string
	ID   uint64
	Type uint16
}

// File reads the logical contents of a file, decompressing as needed. Size is
// its length in bytes; reads that reach it return io.EOF.
type File struct {
	io.ReaderAt
	Size int64
}

// OpenReader opens the container held in the first size bytes of source, using
// its most recent valid checkpoint. It returns an error unless the container
// has 4 KiB blocks and exactly one volume.
func OpenReader(source io.ReaderAt, size int64) (*Reader, error) {
	if size < readerBlockSize {
		return nil, io.ErrUnexpectedEOF
	}
	cache, _ := lru.New[uint64, *readerBlock](readerBlockCache)
	r := &Reader{source: source, size: size, cache: cache}
	container, err := r.block(0, nil, false)
	if err != nil {
		return nil, err
	}
	if string(container[32:36]) != types.NX_MAGIC || le.Uint32(container[36:]) != readerBlockSize {
		return nil, fmt.Errorf("unsupported APFS container")
	}
	count := uint64(le.Uint32(container[104:]))
	base := le.Uint64(container[112:])
	blocks := uint64(size / readerBlockSize)
	if count > readerMaxCheckpoints || base > blocks || count > blocks-base {
		return nil, fmt.Errorf("unsupported APFS checkpoint area")
	}
	latest := le.Uint64(container[16:])
	candidate := make([]byte, readerBlockSize)
	for i := range count {
		if _, err := source.ReadAt(candidate, int64(base+i)*readerBlockSize); err != nil {
			return nil, err
		}
		if xid := le.Uint64(candidate[16:]); string(candidate[32:36]) == types.NX_MAGIC && xid > latest && validChecksum(candidate) {
			container, latest = candidate, xid
			candidate = make([]byte, readerBlockSize)
		}
	}
	volumeOID := le.Uint64(container[184:])
	volumes := 0
	for i := range types.NX_MAX_FILE_SYSTEMS {
		if le.Uint64(container[184+i*8:]) != 0 {
			volumes++
		}
	}
	if volumeOID == 0 || volumes != 1 {
		return nil, fmt.Errorf("expected a single APFS volume, found %d", volumes)
	}
	cmap, err := r.block(le.Uint64(container[160:]), nil, false)
	if err != nil {
		return nil, err
	}
	address, err := r.mapping(le.Uint64(cmap[48:]), volumeOID, latest)
	if err != nil {
		return nil, err
	}
	volume, err := r.block(address, nil, false)
	if err != nil {
		return nil, err
	}
	if string(volume[32:36]) != types.APFS_MAGIC {
		return nil, fmt.Errorf("invalid APFS volume")
	}
	omap, err := r.block(le.Uint64(volume[128:]), nil, false)
	if err != nil {
		return nil, err
	}
	r.omap = le.Uint64(omap[48:])
	r.transaction = le.Uint64(omap[16:])
	r.rootOID = le.Uint64(volume[136:])
	r.extentRoot = le.Uint64(volume[1032:])
	r.sealed = le.Uint64(volume[56:])&uint64(types.APFS_INCOMPAT_SEALED_VOLUME) != 0
	r.root, err = r.mapping(r.omap, r.rootOID, r.transaction)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// validChecksum reports whether a block's object header holds the Fletcher-64
// checksum of the rest of the block. types.VerifyChecksum is not used because
// it accepts any block whose words do not sum to zero.
func validChecksum(block []byte) bool {
	return le.Uint64(block) == types.CreateChecksum(block[8:])
}

// block returns the block at address. It must pass its object checksum unless
// treeNode is set, the volume is sealed, and the block is a header-less B-tree
// node: one flagged BTNODE_NOHEADER whose whole 32-byte object header is zero.
// A non-empty hash must also match the block's SHA-256.
func (r *Reader) block(address uint64, hash []byte, treeNode bool) ([]byte, error) {
	if address >= uint64(r.size/readerBlockSize) {
		return nil, fmt.Errorf("APFS block outside image")
	}
	block, cached := r.cache.Get(address)
	if !cached {
		data := make([]byte, readerBlockSize)
		if _, err := r.source.ReadAt(data, int64(address)*readerBlockSize); err != nil {
			return nil, err
		}
		block = &readerBlock{data: data, checksummed: validChecksum(data)}
	}
	if !block.checksummed {
		headerless := [32]byte(block.data[:32]) == [32]byte{} && le.Uint16(block.data[32:])&nodeNoHeader != 0
		if !treeNode || !r.sealed || !headerless {
			return nil, fmt.Errorf("APFS block checksum mismatch at %#x", address)
		}
	}
	if !cached {
		r.cache.Add(address, block)
	}
	if len(hash) > 0 {
		if !block.summed {
			block.sum, block.summed = sha256.Sum256(block.data), true
		}
		if !bytes.Equal(hash, block.sum[:]) {
			return nil, fmt.Errorf("APFS child hash mismatch")
		}
	}
	return block.data, nil
}

func parseReaderNode(data []byte) (readerNode, error) {
	var node readerNode
	if len(data) != readerBlockSize {
		return node, fmt.Errorf("invalid APFS node size")
	}
	node.flags = le.Uint16(data[32:])
	node.level = le.Uint16(data[34:])
	count := int(le.Uint32(data[36:]))
	tableOffset := int(le.Uint16(data[40:]))
	tableSize := int(le.Uint16(data[42:]))
	fixed := node.flags&nodeFixedKV != 0
	stride := 8
	if fixed {
		stride = 4
	}
	keyBase := 56 + tableSize
	valueEnd := readerBlockSize
	if node.flags&nodeRoot != 0 {
		valueEnd -= 40
	}
	if (node.level == 0) != (node.flags&nodeLeaf != 0) {
		return node, fmt.Errorf("APFS node level %d disagrees with its leaf flag", node.level)
	}
	if node.level > 0 && count == 0 {
		return node, fmt.Errorf("empty APFS index node")
	}
	if node.level >= readerMaxDepth || count > 1024 || tableOffset+count*stride > tableSize || keyBase > valueEnd {
		return node, fmt.Errorf("invalid APFS node table")
	}
	node.records = make([]record, 0, count)
	for i := range count {
		loc := 56 + tableOffset + i*stride
		ko := int(le.Uint16(data[loc:]))
		var ks, vo, vs int
		if fixed {
			ks = 16
			vo = int(le.Uint16(data[loc+2:]))
			vs = 16
			if node.level > 0 {
				vs = 8
			}
		} else {
			ks = int(le.Uint16(data[loc+2:]))
			vo = int(le.Uint16(data[loc+4:]))
			vs = int(le.Uint16(data[loc+6:]))
		}
		if ks < 8 || ko+ks > valueEnd-keyBase || vo > valueEnd-keyBase || vs > vo {
			return node, fmt.Errorf("invalid APFS record bounds")
		}
		node.records = append(node.records, record{data[keyBase+ko : keyBase+ko+ks], data[valueEnd-vo : valueEnd-vo+vs]})
	}
	return node, nil
}

func (r *Reader) node(address uint64, hash []byte, treeNode bool) (readerNode, error) {
	data, err := r.block(address, hash, treeNode)
	if err != nil {
		return readerNode{}, err
	}
	return parseReaderNode(data)
}

func (r *Reader) mapping(root, object, transaction uint64) (uint64, error) {
	for range readerMaxDepth {
		node, err := r.node(root, nil, false)
		if err != nil {
			return 0, err
		}
		selected := -1
		for i, rec := range node.records {
			if len(rec.key) < 16 || len(rec.value) < 8 {
				return 0, fmt.Errorf("short APFS mapping")
			}
			oid, xid := le.Uint64(rec.key), le.Uint64(rec.key[8:])
			if oid > object || (oid == object && xid > transaction) {
				break
			}
			selected = i
		}
		if selected < 0 {
			return 0, fmt.Errorf("missing APFS mapping %#x", object)
		}
		rec := node.records[selected]
		if node.level == 0 {
			if len(rec.value) < 16 || le.Uint64(rec.key) != object || le.Uint32(rec.value)&uint32(types.OMAP_VAL_DELETED) != 0 {
				return 0, fmt.Errorf("deleted or missing APFS mapping")
			}
			return le.Uint64(rec.value[8:]), nil
		}
		root = le.Uint64(rec.value)
	}
	return 0, fmt.Errorf("APFS map depth exceeded")
}

func (r *Reader) records(object uint64, physical bool) ([]record, error) {
	root := r.root
	if physical {
		root = r.extentRoot
	}
	return r.collect(nil, root, object, physical, map[uint64]bool{}, 0, nil)
}

// collect appends the records for object under the node at address to result.
// It is the one descent for both the file-system tree and the file-extent
// tree; physical selects the extent tree's key layout and direct child
// addresses. An object with no records yields none, without an error.
func (r *Reader) collect(result []record, address, object uint64, physical bool, visited map[uint64]bool, depth int, hash []byte) ([]record, error) {
	if depth >= readerMaxDepth || len(visited) >= readerMaxNodes || visited[address] {
		return nil, fmt.Errorf("APFS tree cycle or traversal limit")
	}
	visited[address] = true
	node, err := r.node(address, hash, true)
	if err != nil {
		return nil, err
	}
	if node.level == 0 {
		for _, rec := range node.records {
			if rec.treeID(physical) == object {
				result = append(result, rec)
			}
		}
		return result, nil
	}
	for i, rec := range node.records {
		lower := rec.treeID(physical)
		upper := ^uint64(0)
		if i+1 < len(node.records) {
			upper = node.records[i+1].treeID(physical)
		}
		if object < lower || object > upper {
			continue
		}
		if len(rec.value) < 8 {
			return nil, fmt.Errorf("short APFS child")
		}
		child := le.Uint64(rec.value)
		var childHash []byte
		if !physical {
			if node.flags&nodeHashed != 0 {
				if len(rec.value) < 8+sha256.Size || child > ^uint64(0)-r.rootOID {
					return nil, fmt.Errorf("invalid sealed child")
				}
				child += r.rootOID
				childHash = rec.value[8 : 8+sha256.Size]
			}
			child, err = r.mapping(r.omap, child, r.transaction)
			if err != nil {
				return nil, err
			}
		}
		result, err = r.collect(result, child, object, physical, visited, depth+1, childHash)
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

// scanDir validates every entry of a directory and passes each to visit in
// on-disk order. The name aliases a cached block and is only valid in visit.
func (r *Reader) scanDir(id uint64, visit func(name []byte, id uint64, kind uint16)) error {
	records, err := r.records(id, false)
	if err != nil {
		return err
	}
	for _, rec := range records {
		if rec.kind() != kindDirRec {
			continue
		}
		if len(rec.key) < 12 || len(rec.value) < 18 {
			return fmt.Errorf("short APFS directory record")
		}
		length := int(le.Uint32(rec.key[8:]) & types.J_DREC_LEN_MASK)
		if length > len(rec.key)-12 {
			return io.ErrUnexpectedEOF
		}
		name := bytes.TrimSuffix(rec.key[12:12+length], []byte{0})
		if len(name) == 0 || string(name) == "." || string(name) == ".." || bytes.ContainsAny(name, "/\\\x00") {
			return fmt.Errorf("unsafe APFS name %q", name)
		}
		visit(name, le.Uint64(rec.value), le.Uint16(rec.value[16:])&uint16(types.DREC_TYPE_MASK))
	}
	return nil
}

// ReadDirID returns the entries of the directory with inode number id, sorted
// by name. The root directory is types.ROOT_DIR_INO_NUM. An id with no directory
// records, including one that does not exist, yields no entries and no error.
func (r *Reader) ReadDirID(id uint64) ([]Entry, error) {
	var entries []Entry
	err := r.scanDir(id, func(name []byte, id uint64, kind uint16) {
		entries = append(entries, Entry{string(name), id, kind})
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(entries, func(a, b Entry) int { return strings.Compare(a.Name, b.Name) })
	return entries, nil
}

// Lookup resolves a slash-separated path from the root directory to its entry.
// Names are matched exactly, and symbolic links are not followed; use Readlink
// to resolve one.
func (r *Reader) Lookup(path string) (Entry, error) {
	entry := Entry{Name: "/", ID: types.ROOT_DIR_INO_NUM, Type: uint16(types.DT_DIR)}
	for _, name := range strings.FieldsFunc(path, func(c rune) bool { return c == '/' }) {
		var child *Entry
		err := r.scanDir(entry.ID, func(n []byte, id uint64, kind uint16) {
			if child == nil && string(n) == name {
				child = &Entry{name, id, kind}
			}
		})
		if err != nil {
			return entry, err
		}
		if child == nil {
			return entry, fmt.Errorf("APFS path component missing: %s", name)
		}
		entry = *child
	}
	return entry, nil
}

type readerExtent struct{ logical, physical, length uint64 }

// parseExtent decodes a file-extent record; the file-system tree and the
// physical extent tree share this layout.
func parseExtent(rec record) (readerExtent, error) {
	if len(rec.key) < 16 || len(rec.value) < 16 {
		return readerExtent{}, io.ErrUnexpectedEOF
	}
	return readerExtent{le.Uint64(rec.key[8:]), le.Uint64(rec.value[8:]), le.Uint64(rec.value) & types.J_FILE_EXTENT_LEN_MASK}, nil
}

func (r *Reader) extents(id uint64) ([]readerExtent, error) {
	physical := r.extentRoot != 0
	records, err := r.records(id, physical)
	if err != nil {
		return nil, err
	}
	var extents []readerExtent
	for _, rec := range records {
		if !physical && rec.kind() != kindFileExtent {
			continue
		}
		extent, err := parseExtent(rec)
		if err != nil {
			return nil, err
		}
		extents = append(extents, extent)
	}
	return extents, nil
}

func (r *Reader) extentFile(extents []readerExtent, size uint64) (*File, error) {
	slices.SortFunc(extents, func(a, b readerExtent) int { return cmp.Compare(a.logical, b.logical) })
	position := uint64(0)
	for _, e := range extents {
		if e.logical != position || e.length == 0 || e.physical > uint64(r.size/readerBlockSize) || e.length > uint64(r.size)-e.physical*readerBlockSize || e.length > ^uint64(0)-position {
			return nil, fmt.Errorf("invalid or sparse APFS extent")
		}
		position += e.length
	}
	if size > position || size > uint64(r.size) {
		return nil, fmt.Errorf("incomplete APFS extents")
	}
	return &File{&extentReader{r.source, extents, int64(size)}, int64(size)}, nil
}

// extentReader reads a file through extents that extentFile has sorted and
// checked to be contiguous from logical offset zero.
type extentReader struct {
	source  io.ReaderAt
	extents []readerExtent
	size    int64
}

func (r *extentReader) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fmt.Errorf("negative APFS offset")
	}
	if len(p) == 0 {
		return 0, nil
	}
	if off >= r.size {
		return 0, io.EOF
	}
	want := min(len(p), int(r.size-off))
	n := 0
	first := sort.Search(len(r.extents), func(i int) bool {
		return r.extents[i].logical+r.extents[i].length > uint64(off)
	})
	for _, e := range r.extents[first:] {
		if n == want {
			break
		}
		within := uint64(off) - e.logical
		length := min(want-n, int(e.length-within))
		count, err := r.source.ReadAt(p[n:n+length], int64(e.physical*readerBlockSize+within))
		n += count
		off += int64(count)
		if err != nil {
			return n, err
		}
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// parseXattr decodes an extended-attribute record into its name, flags, and
// embedded payload.
func parseXattr(rec record) (name string, flags uint16, data []byte, err error) {
	if len(rec.key) < 10 || len(rec.value) < 4 {
		return "", 0, nil, io.ErrUnexpectedEOF
	}
	length := int(le.Uint16(rec.key[8:]))
	size := int(le.Uint16(rec.value[2:]))
	if length > len(rec.key)-10 || size > len(rec.value)-4 {
		return "", 0, nil, io.ErrUnexpectedEOF
	}
	name = strings.TrimSuffix(string(rec.key[10:10+length]), "\x00")
	return name, le.Uint16(rec.value), rec.value[4 : 4+size], nil
}

// xattrStream opens the data stream that a stream-stored extended attribute's
// payload refers to.
func (r *Reader) xattrStream(data []byte) (*File, error) {
	if len(data) < 48 {
		return nil, io.ErrUnexpectedEOF
	}
	extents, err := r.extents(le.Uint64(data))
	if err != nil {
		return nil, err
	}
	return r.extentFile(extents, le.Uint64(data[8:]))
}

// xattrValue returns an extended attribute's value of at most limit bytes,
// reading it from its data stream when it is not embedded in the record.
func (r *Reader) xattrValue(flags uint16, data []byte, limit int64) ([]byte, error) {
	if flags&xattrEmbedded != 0 {
		return data, nil
	}
	if flags&xattrStream == 0 {
		return nil, fmt.Errorf("APFS extended attribute has no data")
	}
	stream, err := r.xattrStream(data)
	if err != nil {
		return nil, err
	}
	if stream.Size > limit {
		return nil, fmt.Errorf("APFS extended attribute exceeds limit")
	}
	value := make([]byte, stream.Size)
	if n, err := stream.ReadAt(value, 0); n != len(value) {
		return nil, err
	}
	return value, nil
}

// Readlink returns the target of the symbolic link with inode number id.
func (r *Reader) Readlink(id uint64) (string, error) {
	records, err := r.records(id, false)
	if err != nil {
		return "", err
	}
	for _, rec := range records {
		if rec.kind() != kindXattr {
			continue
		}
		name, flags, data, err := parseXattr(rec)
		if err != nil {
			return "", err
		}
		if name != types.XATTR_SYMLINK_EA_NAME {
			continue
		}
		target, err := r.xattrValue(flags, data, readerMaxLink)
		if err != nil {
			return "", err
		}
		return string(bytes.TrimSuffix(target, []byte{0})), nil
	}
	return "", fmt.Errorf("APFS inode %d is not a symbolic link", id)
}

// dstreamSize returns the logical size of an inode of at least 92 bytes,
// preferring the data-stream extended field over the fixed field.
func dstreamSize(inode []byte) (uint64, error) {
	size := le.Uint64(inode[84:])
	if len(inode) == 92 {
		return size, nil
	}
	if len(inode) < 96 {
		return 0, io.ErrUnexpectedEOF
	}
	count := int(le.Uint16(inode[92:]))
	position := 96 + count*4
	if position > len(inode) {
		return 0, io.ErrUnexpectedEOF
	}
	for i := range count {
		length := int(le.Uint16(inode[98+i*4:]))
		if length > len(inode)-position {
			return 0, io.ErrUnexpectedEOF
		}
		if inode[96+i*4] == byte(types.INO_EXT_TYPE_DSTREAM) {
			if length < 40 {
				return 0, io.ErrUnexpectedEOF
			}
			size = le.Uint64(inode[position:])
		}
		position += (length + 7) &^ 7
	}
	return size, nil
}

// OpenFileID opens the file with inode number id. It supports plain files and
// decmpfs-compressed files (zlib, LZVN, LZFSE, LZBITMAP and uncompressed inline;
// LZVN, LZFSE and LZBITMAP in the resource fork), and rejects sparse files. An
// id without an inode record is an error.
func (r *Reader) OpenFileID(id uint64) (*File, error) {
	records, err := r.records(id, false)
	if err != nil {
		return nil, err
	}
	var inode, compression, resource []byte
	var compressionFlags uint16
	var extents []readerExtent
	for _, rec := range records {
		switch rec.kind() {
		case kindInode:
			inode = rec.value
		case kindXattr:
			name, flags, data, err := parseXattr(rec)
			if err != nil {
				return nil, err
			}
			switch name {
			case types.XATTR_DECMPFS_EA_NAME:
				compression, compressionFlags = data, flags
			case types.XATTR_RESOURCEFORK_EA_NAME:
				if flags&xattrStream == 0 {
					return nil, fmt.Errorf("embedded resource fork unsupported")
				}
				resource = data
			}
		case kindFileExtent:
			extent, err := parseExtent(rec)
			if err != nil {
				return nil, err
			}
			extents = append(extents, extent)
		}
	}
	if len(inode) < 92 {
		return nil, fmt.Errorf("APFS file has no valid inode")
	}
	if compression != nil {
		// A stream-stored header carries an inline payload too large to embed.
		header, err := r.xattrValue(compressionFlags, compression, 16+1+readerMaxDecoded)
		if err != nil {
			return nil, err
		}
		return r.openCompressed(header, resource)
	}
	size, err := dstreamSize(inode)
	if err != nil {
		return nil, err
	}
	if len(extents) == 0 && size > 0 {
		extents, err = r.extents(le.Uint64(inode[8:]))
		if err != nil {
			return nil, err
		}
	}
	return r.extentFile(extents, size)
}

// openCompressed opens a decmpfs file whose payload is either inline in the
// header attribute or chunked in the resource fork.
func (r *Reader) openCompressed(header, resource []byte) (*File, error) {
	if len(header) < 16 || le.Uint32(header) != decmpfsMagic {
		return nil, fmt.Errorf("invalid decmpfs header")
	}
	method := le.Uint32(header[4:])
	size := le.Uint64(header[8:])
	switch method {
	case cmpAttrZlib, cmpAttrLZVN, cmpAttrUncompressed, cmpAttrLZFSE, cmpAttrLZBitmap:
		data, err := decodeInline(header[16:], size, method)
		if err != nil {
			return nil, err
		}
		return &File{bytes.NewReader(data), int64(len(data))}, nil
	}
	if len(resource) < 48 {
		return nil, fmt.Errorf("missing APFS resource stream")
	}
	fork, err := r.xattrStream(resource)
	if err != nil {
		return nil, err
	}
	return newCompressedFile(fork, size, method)
}

func decodeInline(data []byte, size uint64, method uint32) ([]byte, error) {
	if size > readerMaxDecoded {
		return nil, fmt.Errorf("inline APFS file exceeds limit")
	}
	// The codec attribute methods hold one block in the resource-chunk format.
	switch method {
	case cmpAttrLZVN:
		return decodeReaderBlock(data, int(size), cmpRsrcLZVN)
	case cmpAttrLZFSE:
		return decodeReaderBlock(data, int(size), cmpRsrcLZFSE)
	case cmpAttrLZBitmap:
		return decodeReaderBlock(data, int(size), cmpRsrcLZBitmap)
	}
	if len(data) == 0 {
		return nil, io.ErrUnexpectedEOF
	}
	var result []byte
	switch {
	case method == cmpAttrUncompressed, method == cmpAttrZlib && data[0]&15 == 15:
		result = bytes.Clone(data[1:])
	case method == cmpAttrZlib:
		zr, err := zlib.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		result, err = io.ReadAll(io.LimitReader(zr, int64(size)+1))
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported inline compression %d", method)
	}
	if uint64(len(result)) != size {
		return nil, fmt.Errorf("inline decoded size mismatch")
	}
	return result, nil
}

// decodeReaderBlock decodes one resource-format block, which is either stored
// behind a one-byte marker or compressed with the method's codec.
func decodeReaderBlock(data []byte, size int, method uint32) ([]byte, error) {
	if len(data) == 0 || size < 0 || size > readerMaxDecoded {
		return nil, fmt.Errorf("invalid APFS compressed block")
	}
	switch method {
	case cmpRsrcLZVN:
		if data[0] == 6 {
			return storedBlock(data, size)
		}
		result := make([]byte, size+1)
		if n := lzfse.DecodeLZVNBuffer(data, result); n != uint(size) {
			return nil, fmt.Errorf("LZVN size mismatch")
		}
		return result[:size], nil
	case cmpRsrcLZFSE:
		if data[0] != 0x62 {
			return storedBlock(data, size)
		}
		result := make([]byte, size+1)
		if n := lzfse.DecodeBufferInto(data, result); n != size {
			return nil, fmt.Errorf("LZFSE size mismatch")
		}
		return result[:size], nil
	case cmpRsrcLZBitmap:
		if data[0] == 0xff {
			return storedBlock(data, size)
		}
		result := make([]byte, size)
		if status := lzfse.LzBitMapDecompress(data, result); status != 0 {
			return nil, fmt.Errorf("LZBITMAP status %d", status)
		}
		return result, nil
	default:
		return nil, fmt.Errorf("unsupported APFS resource compression %d", method)
	}
}

// storedBlock returns the payload of an uncompressed block, minus its marker.
func storedBlock(data []byte, size int) ([]byte, error) {
	if len(data)-1 != size {
		return nil, fmt.Errorf("APFS decoded size mismatch")
	}
	return bytes.Clone(data[1:]), nil
}

type compressedReader struct {
	source  *File
	offsets []uint32
	size    int64
	method  uint32
	cache   *lru.Cache[int, []byte]
}

func newCompressedFile(source *File, size uint64, method uint32) (*File, error) {
	if size == 0 || size > readerMaxCompressed || (method != cmpRsrcLZVN && method != cmpRsrcLZFSE && method != cmpRsrcLZBitmap) {
		return nil, fmt.Errorf("unsupported resource compression %d", method)
	}
	count := (size + readerChunkSize - 1) / readerChunkSize
	if count > 1<<20 {
		return nil, fmt.Errorf("resource index exceeds limit")
	}
	table := make([]byte, (count+1)*4)
	if _, err := source.ReadAt(table, 0); err != nil {
		return nil, err
	}
	offsets := make([]uint32, count+1)
	for i := range offsets {
		offsets[i] = le.Uint32(table[i*4:])
		if i > 0 && (offsets[i] <= offsets[i-1] || offsets[i]-offsets[i-1] > readerChunkSize+1) {
			return nil, fmt.Errorf("invalid resource offsets")
		}
	}
	if offsets[0] != uint32(len(table)) || int64(offsets[len(offsets)-1]) != source.Size {
		return nil, fmt.Errorf("invalid resource table")
	}
	cache, _ := lru.New[int, []byte](readerChunkCache)
	return &File{&compressedReader{source, offsets, int64(size), method, cache}, int64(size)}, nil
}

func (r *compressedReader) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fmt.Errorf("negative compressed offset")
	}
	if len(p) == 0 {
		return 0, nil
	}
	n := 0
	for n < len(p) && off < r.size {
		data, err := r.chunk(int(off / readerChunkSize))
		if err != nil {
			return n, err
		}
		count := copy(p[n:], data[off%readerChunkSize:])
		n += count
		off += int64(count)
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// chunk returns the decoded chunk at index, decoding and caching it on a miss.
func (r *compressedReader) chunk(index int) ([]byte, error) {
	if data, ok := r.cache.Get(index); ok {
		return data, nil
	}
	encoded := make([]byte, r.offsets[index+1]-r.offsets[index])
	if _, err := r.source.ReadAt(encoded, int64(r.offsets[index])); err != nil {
		return nil, err
	}
	size := min(int64(readerChunkSize), r.size-int64(index)*readerChunkSize)
	data, err := decodeReaderBlock(encoded, int(size), r.method)
	if err != nil {
		return nil, err
	}
	r.cache.Add(index, data)
	return data, nil
}
