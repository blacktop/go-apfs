package types

const (
	NX_EFI_JUMPSTART_MAGIC   = "RDSJ"
	NX_EFI_JUMPSTART_VERSION = 1
	/** Partition UUIDs **/
	APFS_GPT_PARTITION_UUID = "7C3457EF-0000-11AA-AA11-00306543ECAC"
)

// NxEfiJumpstartT is a nx_efi_jumpstart_t struct
type NxEfiJumpstartT struct {
	Obj        ObjPhysT   // The objectʼs header.
	Magic      magic      // A number that can be used to verify that youʼre reading an instance of nx_efi_jumpstart_t.
	Version    uint32     // The version of this data structure.
	EfiFileLen uint32     // The size, in bytes, of the embedded EFI driver.
	NumExtents uint32     // The number of extents in the array.
	Reserved   [16]uint64 // Reserved.
	// RecExtents []prange // The locations where the EFI driver is stored.
}

// EfiJumpstart is a nx_efi_jumpstart struct
type EfiJumpstart struct {
	NxEfiJumpstartT
	RecExtents []prange // The locations where the EFI driver is stored.
}
