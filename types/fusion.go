package types

const (
	/** Address Markers **/
	FUSION_TIER2_DEVICE_BYTE_ADDR = 0x4000000000000000

	/** Fusion Middle-Tree Flags **/
	FUSION_MT_DIRTY    = (1 << 0)
	FUSION_MT_TENANT   = (1 << 1)
	FUSION_MT_ALLFLAGS = (FUSION_MT_DIRTY | FUSION_MT_TENANT)
)

// FUSION_TIER2_DEVICE_BLOCK_ADDR(_blksize) \
// 	(FUSION_TIER2_DEVICE_BYTE_ADDR >> __builtin_ctzl(_blksize))

// FUSION_BLKNO(_fusion_tier2, _blkno, _blksize)   ( \
// 	(_fusion_tier2) \
// 	? ( FUSION_TIER2_DEVICE_BLOCK_ADDR(_blksize) | (_blkno) ) \
// 	: (_blkno) \
// )

type FusionMtKey paddr_t

// FusionWbcPhys is a fusion_wbc_phys_t struct
type FusionWbcPhys struct {
	ObjHdr           ObjPhysT
	Version          uint64
	ListHeadOid      OidT
	ListTailOid      OidT
	StableHeadOffset uint64
	StableTailOffset uint64
	ListBlocksCount  uint32
	Reserved         uint32
	UsedByRc         uint64
	RcStash          prange
}

// FusionWbcListEntry is a fusion_wbc_list_entry_t struct
type FusionWbcListEntry struct {
	WbcLba    paddr_t
	TargetLba paddr_t
	Length    uint64
}

// FusionWbcListPhysT is a fusion_wbc_list_phys_t struct
type FusionWbcListPhysT struct {
	ObjHdr     ObjPhysT
	Version    uint64
	TailOffset uint64
	IndexBegin uint32
	IndexEnd   uint32
	IndexMax   uint32
	Reserved   uint32
	// ListEntries []FusionWbcListEntry
}

/*
	This mapping keeps track of data from the hard drive thatʼs cached on the solid-state drive. For read caching,
	the same data is stored on both the hard drive and the solid-state drive. For write caching, the data is stored
	on the solid-state drive, but space for the data has been allocated on the hard drive, and the data will eventually
	be copied to that space
*/

// FusionWbcListPhys is a fusion_wbc_list_phys struct
type FusionWbcListPhys struct {
	FusionWbcListPhysT
	ListEntries []FusionWbcListEntry
}

// FusionMtVal is a fusion_mt_val_t struct
type FusionMtVal struct {
	Lba    paddr_t
	Length uint32
	Flags  uint32
}
