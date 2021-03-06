// Code generated by "stringer -type=objType,objFlag -output object_string.go"; DO NOT EDIT.

package types

import "strconv"

func _() {
	// An "invalid array index" compiler error signifies that the constant values have changed.
	// Re-run the stringer command to generate them again.
	var x [1]struct{}
	_ = x[OBJECT_TYPE_NX_SUPERBLOCK-1]
	_ = x[OBJECT_TYPE_BTREE-2]
	_ = x[OBJECT_TYPE_BTREE_NODE-3]
	_ = x[OBJECT_TYPE_SPACEMAN-5]
	_ = x[OBJECT_TYPE_SPACEMAN_CAB-6]
	_ = x[OBJECT_TYPE_SPACEMAN_CIB-7]
	_ = x[OBJECT_TYPE_SPACEMAN_BITMAP-8]
	_ = x[OBJECT_TYPE_SPACEMAN_FREE_QUEUE-9]
	_ = x[OBJECT_TYPE_EXTENT_LIST_TREE-10]
	_ = x[OBJECT_TYPE_OMAP-11]
	_ = x[OBJECT_TYPE_CHECKPOINT_MAP-12]
	_ = x[OBJECT_TYPE_FS-13]
	_ = x[OBJECT_TYPE_FSTREE-14]
	_ = x[OBJECT_TYPE_BLOCKREFTREE-15]
	_ = x[OBJECT_TYPE_SNAPMETATREE-16]
	_ = x[OBJECT_TYPE_NX_REAPER-17]
	_ = x[OBJECT_TYPE_NX_REAP_LIST-18]
	_ = x[OBJECT_TYPE_OMAP_SNAPSHOT-19]
	_ = x[OBJECT_TYPE_EFI_JUMPSTART-20]
	_ = x[OBJECT_TYPE_FUSION_MIDDLE_TREE-21]
	_ = x[OBJECT_TYPE_NX_FUSION_WBC-22]
	_ = x[OBJECT_TYPE_NX_FUSION_WBC_LIST-23]
	_ = x[OBJECT_TYPE_ER_STATE-24]
	_ = x[OBJECT_TYPE_GBITMAP-25]
	_ = x[OBJECT_TYPE_GBITMAP_TREE-26]
	_ = x[OBJECT_TYPE_GBITMAP_BLOCK-27]
	_ = x[OBJECT_TYPE_ER_RECOVERY_BLOCK-28]
	_ = x[OBJECT_TYPE_SNAP_META_EXT-29]
	_ = x[OBJECT_TYPE_INTEGRITY_META-30]
	_ = x[OBJECT_TYPE_FEXT_TREE-31]
	_ = x[OBJECT_TYPE_RESERVED_20-32]
	_ = x[OBJECT_TYPE_INVALID-0]
	_ = x[OBJECT_TYPE_TEST-255]
}

const (
	_objType_name_0 = "OBJECT_TYPE_INVALIDOBJECT_TYPE_NX_SUPERBLOCKOBJECT_TYPE_BTREEOBJECT_TYPE_BTREE_NODE"
	_objType_name_1 = "OBJECT_TYPE_SPACEMANOBJECT_TYPE_SPACEMAN_CABOBJECT_TYPE_SPACEMAN_CIBOBJECT_TYPE_SPACEMAN_BITMAPOBJECT_TYPE_SPACEMAN_FREE_QUEUEOBJECT_TYPE_EXTENT_LIST_TREEOBJECT_TYPE_OMAPOBJECT_TYPE_CHECKPOINT_MAPOBJECT_TYPE_FSOBJECT_TYPE_FSTREEOBJECT_TYPE_BLOCKREFTREEOBJECT_TYPE_SNAPMETATREEOBJECT_TYPE_NX_REAPEROBJECT_TYPE_NX_REAP_LISTOBJECT_TYPE_OMAP_SNAPSHOTOBJECT_TYPE_EFI_JUMPSTARTOBJECT_TYPE_FUSION_MIDDLE_TREEOBJECT_TYPE_NX_FUSION_WBCOBJECT_TYPE_NX_FUSION_WBC_LISTOBJECT_TYPE_ER_STATEOBJECT_TYPE_GBITMAPOBJECT_TYPE_GBITMAP_TREEOBJECT_TYPE_GBITMAP_BLOCKOBJECT_TYPE_ER_RECOVERY_BLOCKOBJECT_TYPE_SNAP_META_EXTOBJECT_TYPE_INTEGRITY_METAOBJECT_TYPE_FEXT_TREEOBJECT_TYPE_RESERVED_20"
	_objType_name_2 = "OBJECT_TYPE_TEST"
)

var (
	_objType_index_0 = [...]uint8{0, 19, 44, 61, 83}
	_objType_index_1 = [...]uint16{0, 20, 44, 68, 95, 126, 154, 170, 196, 210, 228, 252, 276, 297, 321, 346, 371, 401, 426, 456, 476, 495, 519, 544, 573, 598, 624, 645, 668}
)

func (i objType) String() string {
	switch {
	case i <= 3:
		return _objType_name_0[_objType_index_0[i]:_objType_index_0[i+1]]
	case 5 <= i && i <= 32:
		i -= 5
		return _objType_name_1[_objType_index_1[i]:_objType_index_1[i+1]]
	case i == 255:
		return _objType_name_2
	default:
		return "objType(" + strconv.FormatInt(int64(i), 10) + ")"
	}
}
func _() {
	// An "invalid array index" compiler error signifies that the constant values have changed.
	// Re-run the stringer command to generate them again.
	var x [1]struct{}
	_ = x[OBJ_VIRTUAL-0]
	_ = x[OBJ_EPHEMERAL-2147483648]
	_ = x[OBJ_PHYSICAL-1073741824]
	_ = x[OBJ_NOHEADER-536870912]
	_ = x[OBJ_ENCRYPTED-268435456]
	_ = x[OBJ_NONPERSISTENT-134217728]
}

const (
	_objFlag_name_0 = "OBJ_VIRTUAL"
	_objFlag_name_1 = "OBJ_NONPERSISTENT"
	_objFlag_name_2 = "OBJ_ENCRYPTED"
	_objFlag_name_3 = "OBJ_NOHEADER"
	_objFlag_name_4 = "OBJ_PHYSICAL"
	_objFlag_name_5 = "OBJ_EPHEMERAL"
)

func (i objFlag) String() string {
	switch {
	case i == 0:
		return _objFlag_name_0
	case i == 134217728:
		return _objFlag_name_1
	case i == 268435456:
		return _objFlag_name_2
	case i == 536870912:
		return _objFlag_name_3
	case i == 1073741824:
		return _objFlag_name_4
	case i == 2147483648:
		return _objFlag_name_5
	default:
		return "objFlag(" + strconv.FormatInt(int64(i), 10) + ")"
	}
}
