package types

const (
	/** Encryption-Rolling Checksum Block Sizes **/
	ER_512B_BLOCKSIZE  = 0
	ER_2KiB_BLOCKSIZE  = 1
	ER_4KiB_BLOCKSIZE  = 2
	ER_8KiB_BLOCKSIZE  = 3
	ER_16KiB_BLOCKSIZE = 4
	ER_32KiB_BLOCKSIZE = 5
	ER_64KiB_BLOCKSIZE = 6

	/** Encryption Rolling Flags **/
	ERSB_FLAG_ENCRYPTING   = 0x00000001
	ERSB_FLAG_DECRYPTING   = 0x00000002
	ERSB_FLAG_KEYROLLING   = 0x00000004
	ERSB_FLAG_PAUSED       = 0x00000008
	ERSB_FLAG_FAILED       = 0x00000010
	ERSB_FLAG_CID_IS_TWEAK = 0x00000020
	ERSB_FLAG_FREE_1       = 0x00000040
	ERSB_FLAG_FREE_2       = 0x00000080

	ERSB_FLAG_CM_BLOCK_SIZE_MASK  = 0x00000F00
	ERSB_FLAG_CM_BLOCK_SIZE_SHIFT = 8

	ERSB_FLAG_ER_PHASE_MASK  = 0x00003000
	ERSB_FLAG_ER_PHASE_SHIFT = 12
	ERSB_FLAG_FROM_ONEKEY    = 0x00004000

	/** Encryption-Rolling Constants **/
	ER_CHECKSUM_LENGTH = 8
	ER_MAGIC           = "FLAB"
	ER_VERSION         = 1

	ER_MAX_CHECKSUM_COUNT_SHIFT = 16
	ER_CUR_CHECKSUM_COUNT_MASK  = 0x0000ffff
)

// ErStatePhysHeader is a er_state_phys_header_t struct
type ErStatePhysHeader struct {
	O       ObjPhysT
	Magic   magic
	Version uint32
}

// ErStatePhys is a er_state_phys_t struct
type ErStatePhys struct {
	Header               ErStatePhysHeader
	Flags                uint64
	SnapXid              uint64
	CurrentFextObjId     uint64
	FileOffset           uint64
	Progress             uint64
	TotalBlkToEncrypt    uint64
	BlockmapOid          OidT
	TidemarkObjId        uint64
	RecoveryExtentsCount uint64
	RecoveryListOid      OidT
	RecoveryLength       uint64
}

// ErStatePhysV1T is a er_state_phys_v1_t struct
type ErStatePhysV1T struct {
	Header            ErStatePhysHeader
	Flags             uint64
	SnapXid           uint64
	CurrentFextObjId  uint64
	FileOffset        uint64
	FextPbn           uint64
	Paddr             uint64
	Progress          uint64
	TotalBlkToEncrypt uint64
	BlockmapOid       uint64
	ChecksumCount     uint64
	Reserved          uint64
	FextCid           uint64
	// Checksum [0]uint8
}

// ErStatePhysV1 is a er_state_phys (v1) struct
type ErStatePhysV1 struct {
	ErStatePhysV1T
	Checksum []byte
}

type erPhase uint32

const (
	ER_PHASE_OMAP_ROLL erPhase = 1
	ER_PHASE_DATA_ROLL erPhase = 2
	ER_PHASE_SNAP_ROLL erPhase = 3
)

// ErRecoveryBlockPhysT is a er_recovery_block_phys_t struct
type ErRecoveryBlockPhysT struct {
	O       ObjPhysT
	Offset  uint64
	NextOid OidT
	// Data []byte
}

// ErRecoveryBlockPhys is a er_recovery_block_physstruct
type ErRecoveryBlockPhys struct {
	ErRecoveryBlockPhysT
	Data []byte
}

// GbitmapBlockPhysT is a gbitmap_block_phys_t struct
type GbitmapBlockPhysT struct {
	O ObjPhysT
	// Field []uint64
}

// GbitmapBlockPhys is a gbitmap_block_phys
type GbitmapBlockPhys struct {
	GbitmapBlockPhysT
	Field []uint64
}

// GbitmapPhys is a gbitmap_phys struct
type GbitmapPhys struct {
	O        ObjPhysT
	TreeOid  OidT
	BitCount uint64
	Flags    uint64
}
