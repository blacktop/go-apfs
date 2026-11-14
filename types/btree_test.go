package types

import (
	"bytes"
	"encoding/binary"
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
		{"leaf entries in a node without the leaf flag", func() BTreeNodePhys {
			return fextTree(BTNODE_ROOT, fextEntry(10, 100))
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

func TestReadBTreeNodeNoHeader(t *testing.T) {
	BLOCK_SIZE = NX_DEFAULT_BLOCK_SIZE
	block := make([]byte, NX_DEFAULT_BLOCK_SIZE)
	binary.LittleEndian.PutUint16(block[32:], uint16(BTNODE_ROOT|BTNODE_LEAF|BTNODE_NOHEADER))

	o, err := ReadFSTreeNode(bytes.NewReader(block), 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if o.Hdr.GetType() != OBJECT_TYPE_BTREE_NODE || o.Hdr.GetSubType() != OBJECT_TYPE_FSTREE {
		t.Fatalf("got type %s/%s, want btree node/fstree", o.Hdr.GetType(), o.Hdr.GetSubType())
	}
	if o.Body.(BTreeNodePhys).sealed != true {
		t.Fatal("sealed flag was not kept on the root node")
	}
	if _, err := ReadFSTreeNode(bytes.NewReader(block), 0, false); err == nil {
		t.Fatal("unsealed volume accepted a header-less FS tree node")
	}
	if _, err := ReadBTreeNode(bytes.NewReader(block), 0, OBJECT_TYPE_FSTREE); err == nil {
		t.Fatal("strict reader accepted a header-less node")
	}
	if _, err := ReadObj(bytes.NewReader(block), 0); err == nil {
		t.Fatal("ReadObj decoded a header-less node")
	}

	block[0] = 1
	if _, err := ReadFSTreeNode(bytes.NewReader(block), 0, true); err == nil {
		t.Fatal("sealed volume accepted a node whose object header is not all zero")
	}
}
