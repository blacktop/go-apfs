package types

//go:generate stringer -type=nrFlags,rlFlags -output reaper_string.go

type nrFlags uint32
type rlFlags uint32

const (
	/** Volume Reaper States **/
	APFS_REAP_PHASE_START        = 0
	APFS_REAP_PHASE_SNAPSHOTS    = 1
	APFS_REAP_PHASE_ACTIVE_FS    = 2
	APFS_REAP_PHASE_DESTROY_OMAP = 3
	APFS_REAP_PHASE_DONE         = 4

	/** Reaper Flags **/
	NR_BHM_FLAG nrFlags = 0x00000001 // Reserved.
	NR_CONTINUE nrFlags = 0x00000002 // The current object is being reaped.

	/** Reaper List Entry Flags **/
	NRLE_VALID          rlFlags = 0x00000001
	NRLE_REAP_ID_RECORD rlFlags = 0x00000002
	NRLE_CALL           rlFlags = 0x00000004
	NRLE_COMPETITION    rlFlags = 0x00000008
	NRLE_CLEANUP        rlFlags = 0x00000010

	/** Reaper List Flags **/
	NRL_INDEX_INVALID = 0xffffffff
)

// NxReaperPhysT is a nx_reaper_phys_t struct
type NxReaperPhysT struct {
	Obj             ObjPhysT
	NextReapID      uint64
	CompletedID     uint64
	Head            OidT
	Tail            OidT
	Flags           nrFlags
	RlCount         uint32
	Type            uint32 // TODO: this might be the "Volume Reaper States"
	Size            uint32
	FsOid           OidT
	Oid             OidT
	Xid             XidT
	NrleFlags       uint32
	StateBufferSize uint32
	// StateBuffer     []uint8
}

// ReaperPhys is a nx_reaper_phys struct
type ReaperPhys struct {
	NxReaperPhysT
	StateBuffer []uint8
}

// NxReapListPhysT is a nx_reap_list_phys_t struct
type NxReapListPhysT struct {
	Obj   ObjPhysT
	Next  OidT
	Flags uint32
	Max   uint32
	Count uint32
	First uint32
	Last  uint32
	Free  uint32
}

// ReapListPhys is a nx_reap_list_phys struct
type ReapListPhys struct {
	NxReapListPhysT
	Entries []ReapListEntry
}

// ReapListEntry is a nx_reap_list_entry_t struct
type ReapListEntry struct {
	Next  uint32
	Flags rlFlags
	Type  uint32
	Size  uint32
	FsOid OidT
	Oid   OidT
	Xid   XidT
}

// OmapReapState is a omap_reap_state_t struct
type OmapReapState struct {
	Phase omapReapPhase // The current reaping phase.
	Ok    OMapKey       // The key of the most recently freed entry in the object map.
}

// OmapCleanupState is a omap_cleanup_state_t struct
type OmapCleanupState struct {
	Cleaning  uint32  // A flag that indicates whether the structure has valid data in it.
	OmsFlags  uint32  // The flags for the snapshot being deleted.
	SxIDPrev  XidT    // The transaction identifier of the snapshot prior to the snapshots being deleted.
	SxIDStart XidT    // The transaction identifier of the first snapshot being deleted
	SxIDEnf   XidT    // The transaction identifier of the last snapshot being deleted.
	SxIDNext  XidT    // The transaction identifier of the snapshot after the snapshots being deleted.
	CurKey    OMapKey // The key of the next object mapping to consider for deletion.
}

// ApfsReapState is a apfs_reap_state_t struct
type ApfsReapState struct {
	LastPbn    uint64
	CurSnapXid XidT
	Phase      uint32
}
