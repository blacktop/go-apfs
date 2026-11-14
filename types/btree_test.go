package types

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"slices"
	"testing"
)

func TestChildOidFromIndexValue(t *testing.T) {
	for _, tt := range []struct {
		name    string
		val     any
		rootOid OidT
		hashed  bool
		want    OidT
		err     bool
	}{
		{"plain oid", uint64(0x100), 0x999, false, 0x100, false},
		{"hashed value is relative to the root oid", BTreeNodeIndexNodeValT{ChildOid: 0x20}, 0x510, true, 0x530, false},
		{"plain oid in a hashed tree is relative to the root oid", uint64(0x20), 0x510, true, 0x530, false},
		{"hashed value in a plain tree", BTreeNodeIndexNodeValT{ChildOid: 0x42}, 0x510, false, 0x42, false},
		{"hashed value with a zero root oid", BTreeNodeIndexNodeValT{ChildOid: 0x42}, 0, true, 0x42, false},
		{"unrecognized value", "not a child oid", 0, false, 0, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := childOidFromIndexValue(tt.val, tt.rootOid, tt.hashed)
			if (err != nil) != tt.err || got != tt.want {
				t.Fatalf("got (%#x, %v), want (%#x, error=%v)", got, err, tt.want, tt.err)
			}
		})
	}
}

func fextEntry(privateID, physBlock uint64) FextNodeEntry {
	return FextNodeEntry{
		Key: fext_tree_key_t{PrivateID: privateID},
		Val: fext_tree_val_t{LenAndFlags: 4096, PhysBlockNum: physBlock},
	}
}

func fextTree(flags btreeNodeFlag, entries ...FextNodeEntry) BTreeNodePhys {
	root := BTreeNodePhys{}
	root.Flags = flags
	root.Nkeys = uint32(len(entries))
	for _, e := range entries {
		root.Entries = append(root.Entries, e)
	}
	return root
}

func TestGetFextRecordsForPrivateID(t *testing.T) {
	tree := fextTree(BTNODE_ROOT|BTNODE_LEAF,
		fextEntry(10, 100),
		fextEntry(20, 200), fextEntry(20, 201), fextEntry(20, 202),
		fextEntry(30, 300),
	)
	for _, tt := range []struct {
		name string
		id   uint64
		want []uint64
		err  bool
	}{
		{"single extent", 10, []uint64{100}, false},
		{"several extents in order", 20, []uint64{200, 201, 202}, false},
		{"last entry", 30, []uint64{300}, false},
		{"absent between entries", 15, nil, false},
		{"absent before the first entry", 1, nil, false},
		{"absent past the last entry", 999, nil, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tree.GetFextRecordsForPrivateID(nil, tt.id)
			var blocks []uint64
			for _, r := range got {
				blocks = append(blocks, r.Val.PhysBlockNum)
			}
			if (err != nil) != tt.err || !slices.Equal(blocks, tt.want) {
				t.Fatalf("got (%v, %v), want (%v, error=%v)", blocks, err, tt.want, tt.err)
			}
		})
	}
}

func TestGetFextRecordsForPrivateIDMalformedTree(t *testing.T) {
	for _, tt := range []struct {
		name  string
		build func() BTreeNodePhys
		err   bool
	}{
		{"root level 0xFFFF", func() BTreeNodePhys {
			root := fextTree(BTNODE_ROOT, FextNodeEntry{})
			root.Level = 0xFFFF
			return root
		}, true},
		{"no leaf within the root level", func() BTreeNodePhys {
			child := BTreeNodePhys{}
			child.Nkeys = 1
			child.Entries = []any{OMapNodeEntry{PAddr: 5}}
			root := BTreeNodePhys{}
			root.Flags = BTNODE_ROOT
			root.Nkeys = 1
			root.Entries = []any{OMapNodeEntry{PAddr: 5}}
			root.EnableNodeCache(4)
			root.nodeCache.Add(5, child)
			return root
		}, true},
		{"leaf root without the root flag", func() BTreeNodePhys {
			return fextTree(BTNODE_LEAF, fextEntry(10, 100))
		}, false},
		{"empty leaf root", func() BTreeNodePhys {
			return fextTree(BTNODE_ROOT | BTNODE_LEAF)
		}, false},
		{"level 0 node without the leaf flag", func() BTreeNodePhys {
			return fextTree(BTNODE_ROOT, fextEntry(10, 100))
		}, true},
		{"leaf node holding index entries", func() BTreeNodePhys {
			root := fextTree(BTNODE_ROOT | BTNODE_LEAF)
			root.Nkeys = 1
			root.Entries = []any{OMapNodeEntry{PAddr: 5}}
			return root
		}, true},
		{"entry that is neither a file-extent record nor an index entry", func() BTreeNodePhys {
			root := fextTree(BTNODE_ROOT | BTNODE_LEAF)
			root.Nkeys = 1
			root.Entries = []any{"entry"}
			return root
		}, true},
		{"more keys than entries", func() BTreeNodePhys {
			root := fextTree(BTNODE_ROOT|BTNODE_LEAF, fextEntry(10, 100))
			root.Nkeys = 4
			return root
		}, true},
		{"empty index root", func() BTreeNodePhys {
			root := fextTree(BTNODE_ROOT)
			root.Level = 1
			return root
		}, true},
		{"child without the leaf flag holding file-extent records", func() BTreeNodePhys {
			child := fextTree(0, fextEntry(10, 100))
			root := BTreeNodePhys{}
			root.Flags = BTNODE_ROOT
			root.Level = 1
			root.Nkeys = 1
			root.Entries = []any{OMapNodeEntry{PAddr: 5}}
			root.EnableNodeCache(4)
			root.nodeCache.Add(5, child)
			return root
		}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := tt.build()
			if _, err := root.GetFextRecordsForPrivateID(nil, 999); (err != nil) != tt.err {
				t.Fatalf("got error %v, want error=%v", err, tt.err)
			}
		})
	}
}

type searchLeaf struct{ key uint64 }

type searchIndex struct {
	key   uint64
	child string
}

func searchKey(entry any, leaf bool) (uint64, error) {
	if leaf {
		e, ok := entry.(searchLeaf)
		if !ok {
			return 0, fmt.Errorf("leaf holds a %T", entry)
		}
		return e.key, nil
	}
	e, ok := entry.(searchIndex)
	if !ok {
		return 0, fmt.Errorf("index holds a %T", entry)
	}
	return e.key, nil
}

func searchNode(level uint16, flags btreeNodeFlag, entries ...any) BTreeNodePhys {
	var n BTreeNodePhys
	n.Level = level
	n.Flags = flags
	n.Nkeys = uint32(len(entries))
	n.Entries = entries
	return n
}

func searchLeafNode(keys ...uint64) BTreeNodePhys {
	var entries []any
	for _, k := range keys {
		entries = append(entries, searchLeaf{k})
	}
	return searchNode(0, BTNODE_LEAF, entries...)
}

func TestSearchTree(t *testing.T) {
	nodes := map[string]BTreeNodePhys{
		"L0": searchLeafNode(1, 2),
		"L1": searchLeafNode(3, 4, 4),
		"L2": searchLeafNode(4, 5),
		"L3": searchLeafNode(5, 5, 6),
		"L4": searchLeafNode(7),
		"I0": searchNode(1, 0, searchIndex{1, "L0"}, searchIndex{3, "L1"}, searchIndex{4, "L2"}),
		"I1": searchNode(1, 0, searchIndex{5, "L3"}, searchIndex{7, "L4"}),
	}
	root := searchNode(2, BTNODE_ROOT, searchIndex{1, "I0"}, searchIndex{5, "I1"})
	readChild := func(entry any) (BTreeNodePhys, error) {
		node, ok := nodes[entry.(searchIndex).child]
		if !ok {
			return BTreeNodePhys{}, fmt.Errorf("no node %q", entry.(searchIndex).child)
		}
		return node, nil
	}
	for _, tt := range []struct {
		name   string
		target uint64
		want   int
	}{
		{"first key", 1, 1},
		{"last key", 7, 1},
		{"duplicates inside a leaf", 4, 3},
		{"duplicates across leaves", 5, 3},
		{"key that starts a leaf", 3, 1},
		{"key that ends the tree", 7, 1},
		{"absent before the first key", 0, 0},
		{"absent between keys", 8, 0},
		{"absent past the last key", 99, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := searchTree(root, tt.target, searchKey, readChild)
			if err != nil || len(got) != tt.want {
				t.Fatalf("got (%d records, %v), want %d records", len(got), err, tt.want)
			}
			for _, e := range got {
				if e.(searchLeaf).key != tt.target {
					t.Fatalf("got a record with key %d for target %d", e.(searchLeaf).key, tt.target)
				}
			}
		})
	}

	for _, tt := range []struct {
		name string
		root BTreeNodePhys
	}{
		{"level overflow", searchNode(0xFFFF, BTNODE_ROOT, searchIndex{1, "L0"})},
		{"no leaf within the root level", searchNode(0, BTNODE_ROOT, searchIndex{1, "I0"})},
		{"index node holding a leaf entry", searchNode(1, BTNODE_ROOT, searchLeaf{1})},
		{"leaf node holding an index entry", searchNode(0, BTNODE_ROOT|BTNODE_LEAF, searchIndex{1, "L0"})},
		{"child that does not exist", searchNode(1, BTNODE_ROOT, searchIndex{1, "missing"})},
		{"child with more keys than entries", func() BTreeNodePhys {
			nodes["bad"] = searchLeafNode(1)
			bad := nodes["bad"]
			bad.Nkeys = 3
			nodes["bad"] = bad
			return searchNode(1, BTNODE_ROOT, searchIndex{1, "bad"})
		}()},
	} {
		t.Run("malformed "+tt.name, func(t *testing.T) {
			if _, err := searchTree(tt.root, 1, searchKey, readChild); err == nil {
				t.Fatal("got no error")
			}
		})
	}
}

func fsEntry(oid uint64) NodeEntry {
	return NodeEntry{Hdr: JKeyT{ObjIDAndType: oid | uint64(APFS_TYPE_INODE)<<OBJ_TYPE_SHIFT}}
}

func TestGetFSRecordsForOid(t *testing.T) {
	root := searchNode(0, BTNODE_ROOT|BTNODE_LEAF, fsEntry(10), fsEntry(20), fsEntry(20), fsEntry(30))
	var omap BTreeNodePhys
	for _, tt := range []struct {
		name string
		oid  OidT
		want int
	}{
		{"single record", 10, 1},
		{"several records", 20, 2},
		{"absent between records", 15, 0},
		{"absent before the first record", 1, 0},
		{"absent past the last record", 99, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := omap.GetFSRecordsForOid(nil, root, tt.oid, 0)
			if err != nil || len(got) != tt.want {
				t.Fatalf("got (%d records, %v), want %d records", len(got), err, tt.want)
			}
		})
	}
	t.Run("entry that is not a record", func(t *testing.T) {
		bad := searchNode(0, BTNODE_ROOT|BTNODE_LEAF, searchLeaf{10})
		if _, err := omap.GetFSRecordsForOid(nil, bad, 10, 0); err == nil {
			t.Fatal("got no error")
		}
	})
}

func headerlessNode(flags btreeNodeFlag, mutate func([]byte)) []byte {
	block := make([]byte, NX_DEFAULT_BLOCK_SIZE)
	binary.LittleEndian.PutUint16(block[32:], uint16(flags))
	if mutate != nil {
		mutate(block)
	}
	return block
}

func TestReadFSTreeNodeNoHeader(t *testing.T) {
	BLOCK_SIZE = NX_DEFAULT_BLOCK_SIZE
	block := headerlessNode(BTNODE_ROOT|BTNODE_LEAF|BTNODE_NOHEADER, nil)

	o, err := ReadFSTreeNode(bytes.NewReader(block), 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if o.Hdr.GetType() != OBJECT_TYPE_BTREE_NODE || o.Hdr.GetSubType() != OBJECT_TYPE_FSTREE {
		t.Fatalf("got type %s/%s, want btree node/fstree", o.Hdr.GetType(), o.Hdr.GetSubType())
	}
	if node, ok := o.Body.(BTreeNodePhys); !ok || !node.sealed {
		t.Fatal("node read from a sealed volume is not marked sealed")
	}
	if _, err := ReadFSTreeNode(bytes.NewReader(block), 0, false); err == nil {
		t.Fatal("ReadFSTreeNode decoded a header-less node on an unsealed volume")
	}
	if _, err := ReadBTreeNode(bytes.NewReader(block), 0, OBJECT_TYPE_FSTREE); err == nil {
		t.Fatal("ReadBTreeNode decoded a header-less node")
	}
	if _, err := ReadObj(bytes.NewReader(block), 0); err == nil {
		t.Fatal("ReadObj decoded a header-less node")
	}
}

func TestHeaderlessNodeIsRestricted(t *testing.T) {
	BLOCK_SIZE = NX_DEFAULT_BLOCK_SIZE
	const flags = BTNODE_ROOT | BTNODE_LEAF | BTNODE_NOHEADER
	for _, tt := range []struct {
		name    string
		block   []byte
		subtype objType
		sealed  bool
		ok      bool
	}{
		{"sealed fs tree", headerlessNode(flags, nil), OBJECT_TYPE_FSTREE, true, true},
		{"sealed fext tree", headerlessNode(flags, nil), OBJECT_TYPE_FEXT_TREE, true, true},
		{"unsealed fs tree", headerlessNode(flags, nil), OBJECT_TYPE_FSTREE, false, false},
		{"unsealed fext tree", headerlessNode(flags, nil), OBJECT_TYPE_FEXT_TREE, false, false},
		{"sealed omap", headerlessNode(flags, nil), OBJECT_TYPE_OMAP, true, false},
		{"sealed extent-ref tree", headerlessNode(flags, nil), OBJECT_TYPE_BLOCKREFTREE, true, false},
		{"sealed snap-meta tree", headerlessNode(flags, nil), OBJECT_TYPE_SNAPMETATREE, true, false},
		{"sealed without the no-header flag", headerlessNode(BTNODE_ROOT|BTNODE_LEAF, nil), OBJECT_TYPE_FSTREE, true, false},
		{"sealed with a non-zero checksum", headerlessNode(flags, func(b []byte) { b[0] = 1 }), OBJECT_TYPE_FSTREE, true, false},
		{"sealed with a non-zero oid", headerlessNode(flags, func(b []byte) { b[8] = 1 }), OBJECT_TYPE_FSTREE, true, false},
		{"sealed with a non-zero xid", headerlessNode(flags, func(b []byte) { b[16] = 1 }), OBJECT_TYPE_FSTREE, true, false},
		{"sealed with a non-zero type", headerlessNode(flags, func(b []byte) { b[24] = 1 }), OBJECT_TYPE_FSTREE, true, false},
		{"sealed with a non-zero subtype", headerlessNode(flags, func(b []byte) { b[28] = 1 }), OBJECT_TYPE_FSTREE, true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := readObj(bytes.NewReader(tt.block), 0, tt.subtype, tt.sealed)
			if (err == nil) != tt.ok {
				t.Fatalf("got error %v, want ok=%v", err, tt.ok)
			}
		})
	}
}

func TestReadNodeCachedHonoursSealed(t *testing.T) {
	BLOCK_SIZE = NX_DEFAULT_BLOCK_SIZE
	block := headerlessNode(BTNODE_ROOT|BTNODE_LEAF|BTNODE_NOHEADER, nil)
	var root BTreeNodePhys
	if _, err := root.readNodeCached(bytes.NewReader(block), 0, OBJECT_TYPE_FSTREE, true); err != nil {
		t.Fatal(err)
	}
	if _, err := root.readNodeCached(bytes.NewReader(block), 0, OBJECT_TYPE_FSTREE, false); err == nil {
		t.Fatal("readNodeCached decoded a header-less node without the sealed flag")
	}
}
