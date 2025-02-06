package hfsplus

// reference: https://developer.apple.com/library/archive/technotes/tn/tn1150.html

type UniStr255 struct {
	Length  uint16
	UniChar [255]uint16
}

type BSDInfo struct {
	OwnerID    uint32
	GroupID    uint32
	AdminFlags uint8
	OwnerFlags uint8
	FileMode   uint16
	Special    uint32
}

type ForkData struct {
	LogicalSize uint64
	ClumpSize   uint32
	TotalBlocks uint32
	Extents     ExtentRecord
}

type ExtentRecord [8]ExtentDescriptor

type ExtentDescriptor struct {
	StartBlock uint32
	BlockCount uint32
}

const (
	HFSPlusSigWord = 0x482B // "H+"
	HFSXSigWord    = 0x4858 // "HX"
)

const (
	/* Bits 0-6 are reserved */
	HFSVolumeHardwareLockBit     = 7
	HFSVolumeUnmountedBit        = 8
	HFSVolumeSparedBlocksBit     = 9
	HFSVolumeNoCacheRequiredBit  = 10
	HFSBootVolumeInconsistentBit = 11
	HFSCatalogNodeIDsReusedBit   = 12
	HFSVolumeJournaledBit        = 13
	/* Bit 14 is reserved */
	HFSVolumeSoftwareLockBit = 15
	/* Bits 16-31 are reserved */
)

type VolumeHeader struct {
	Reserved           [1024]uint8
	Signature          uint16
	Version            uint16
	Attributes         uint32
	LastMountedVersion uint32
	JournalInfoBlock   uint32
	CreateDate         uint32
	ModifyDate         uint32
	BackupDate         uint32
	CheckedDate        uint32
	FileCount          uint32
	FolderCount        uint32
	BlockSize          uint32
	TotalBlocks        uint32
	FreeBlocks         uint32
	NextAllocation     uint32
	RsrcClumpSize      uint32
	DataClumpSize      uint32
	NextCatalogID      CatalogNodeID
	WriteCount         uint32
	EncodingsBitmap    uint64
	FinderInfo         [8]uint32
	AllocationFile     ForkData
	ExtentsFile        ForkData
	CatalogFile        ForkData
	AttributesFile     ForkData
	StartupFile        ForkData
}

const (
	BTLeafNode   = -1
	BTIndexNode  = 0
	BTHeaderNode = 1
	BTMapNode    = 2
)

type BTNodeDescriptor struct {
	FLink      uint32
	BLink      uint32
	Kind       int8
	Height     uint8
	NumRecords uint16
	Reserved   uint16
}

const (
	HFSBTreeType       = 0   // control file
	kUserBTreeType     = 128 // user btree type starts from 128
	kReservedBTreeType = 255
)

const (
	BTBadCloseMask          = 0x00000001
	BTBigKeysMask           = 0x00000002
	BTVariableIndexKeysMask = 0x00000004
)

type BTHeaderRec struct {
	TreeDepth      uint16
	RootNode       uint32
	LeafRecords    uint32
	FirstLeafNode  uint32
	LastLeafNode   uint32
	NodeSize       uint16
	MaxKeyLength   uint16
	TotalNodes     uint32
	FreeNodes      uint32
	Reserved1      uint16
	ClumpSize      uint32 // misaligned
	BtreeType      uint8
	KeyCompareType uint8
	Attributes     uint32 // long aligned again
	Reserved3      [16]uint32
}

type CatalogNodeID uint32

const (
	HFSRootParentID           CatalogNodeID = 1
	HFSRootFolderID           CatalogNodeID = 2
	HFSExtentsFileID          CatalogNodeID = 3
	HFSCatalogFileID          CatalogNodeID = 4
	HFSBadBlockFileID         CatalogNodeID = 5
	HFSAllocationFileID       CatalogNodeID = 6
	HFSStartupFileID          CatalogNodeID = 7
	HFSAttributesFileID       CatalogNodeID = 8
	HFSRepairCatalogFileID    CatalogNodeID = 14
	HFSBogusExtentFileID      CatalogNodeID = 15
	HFSFirstUserCatalogNodeID CatalogNodeID = 16
)

type CatalogKey struct {
	KeyLength uint16
	ParentID  CatalogNodeID
	NodeName  UniStr255
}

const (
	HFSPlusFolderRecord       = 0x0001
	HFSPlusFileRecord         = 0x0002
	HFSPlusFolderThreadRecord = 0x0003
	HFSPlusFileThreadRecord   = 0x0004
)

const (
	HFSFolderRecord       = 0x0100
	HFSFileRecord         = 0x0200
	HFSFolderThreadRecord = 0x0300
	HFSFileThreadRecord   = 0x0400
)

type CatalogFolder struct {
	RecordType       int16
	Flags            uint16
	Valence          uint32
	FolderID         CatalogNodeID
	CreateDate       uint32
	ContentModDate   uint32
	AttributeModDate uint32
	AccessDate       uint32
	BackupDate       uint32
	Permissions      BSDInfo
	UserInfo         FolderInfo
	FinderInfo       ExtendedFolderInfo
	TextEncoding     uint32
	Reserved         uint32
}

type CatalogFile struct {
	RecordType       int16
	Flags            uint16
	Reserved1        uint32
	FileID           CatalogNodeID
	CreateDate       uint32
	ContentModDate   uint32
	AttributeModDate uint32
	AccessDate       uint32
	BackupDate       uint32
	Permissions      BSDInfo
	UserInfo         FileInfo
	FinderInfo       ExtendedFileInfo
	TextEncoding     uint32
	Reserved2        uint32
	DataFork         ForkData
	ResourceFork     ForkData
}

const (
	HFSFileLockedBit    = 0x0000
	HFSFileLockedMask   = 0x0001
	HFSThreadExistsBit  = 0x0001
	HFSThreadExistsMask = 0x0002
)

type CatalogThread struct {
	RecordType int16
	Reserved   int16
	ParentID   CatalogNodeID
	NodeName   UniStr255
}

type Point struct {
	V int16
	H int16
}

type Rect struct {
	Top    int16
	Left   int16
	Bottom int16
	Right  int16
}

const (
	/* Finder flags (finderFlags, fdFlags and frFlags) */
	IsOnDesk = 0x0001 /* Files and folders (System 6) */
	Color    = 0x000E /* Files and folders */
	IsShared = 0x0040 /* Files only (Applications only) If */
	/* clear, the application needs */
	/* to write to its resource fork, */
	/* and therefore cannot be shared */
	/* on a server */
	HasNoINITs = 0x0080 /* Files only (Extensions/Control */
	/* Panels only) */
	/* This file contains no INIT resource */
	HasBeenInited = 0x0100 /* Files only.  Clear if the file */
	/* contains desktop database resources */
	/* ('BNDL', 'FREF', 'open', 'kind'...) */
	/* that have not been added yet.  Set */
	/* only by the Finder. */
	/* Reserved for folders */
	HasCustomIcon = 0x0400 /* Files and folders */
	kIsStationery = 0x0800 /* Files only */
	kNameLocked   = 0x1000 /* Files and folders */
	HasBundle     = 0x2000 /* Files only */
	kIsInvisible  = 0x4000 /* Files and folders */
	kIsAlias      = 0x8000 /* Files only */
)

const (
	ExtendedFlagsAreInvalid    = 0x8000 // The other extended flags should be ignored
	ExtendedFlagHasCustomBadge = 0x0100 // The file or folder has a badge resource
	ExtendedFlagHasRoutingInfo = 0x0004 // The file contains routing info resource
)

type FourCharCode uint32

type OSType FourCharCode

type FileInfo struct {
	FileType      OSType // The type of the file
	FileCreator   OSType // The file's creator
	FinderFlags   uint16
	Location      Point // File's location in the folder.
	ReservedField uint16
}

type ExtendedFileInfo struct {
	Reserved1           [4]int16
	ExtendedFinderFlags uint16
	Reserved2           int16
	PutAwayFolderID     int32
}

type FolderInfo struct {
	WindowBounds  Rect // The position and dimension of the folder's window
	FinderFlags   uint16
	Location      Point // Folder's location in the parent folder. If set to {0, 0}, the Finder will place the item automatically
	ReservedField uint16
}

type ExtendedFolderInfo struct {
	ScrollPosition      Point // Scroll position (for icon views)
	Reserved1           int32
	ExtendedFinderFlags uint16
	Reserved2           int16
	PutAwayFolderID     int32
}

type ExtentKey struct {
	KeyLength  uint16
	ForkType   uint8
	Pad        uint8
	FileID     CatalogNodeID
	StartBlock uint32
}

const (
	HFSPlusAttrInlineData = 0x10
	HFSPlusAttrForkData   = 0x20
	HFSPlusAttrExtents    = 0x30
)

type AttrForkData struct {
	RecordType uint32
	Reserved   uint32
	TheFork    ForkData
}

type AttrExtents struct {
	RecordType uint32
	Reserved   uint32
	Extents    ExtentRecord
}

const (
	HardLinkFileType = 0x686C6E6B /* 'hlnk' */
	HFSPlusCreator   = 0x6866732B /* 'hfs+' */
)

const (
	SymLinkFileType = 0x736C6E6B /* 'slnk' */
	SymLinkCreator  = 0x72686170 /* 'rhap' */
)

type JournalInfoBlock struct {
	Flags           uint32
	DeviceSignature [8]uint32
	Offset          uint64
	Size            uint64
	Reserved        [32]uint32
}

const (
	JIJournalInFSMask          = 0x00000001
	JIJournalOnOtherDeviceMask = 0x00000002
	JIJournalNeedInitMask      = 0x00000004
)

const (
	JOURNAL_HEADER_MAGIC = 0x4a4e4c78
	ENDIAN_MAGIC         = 0x12345678
)

type JournalHeader struct {
	Magic     uint32
	Endian    uint32
	Start     uint64
	End       uint64
	Size      uint64
	BlhdrSize uint32
	Checksum  uint32
	JhdrSize  uint32
}

type BlockListHeader struct {
	MaxBlocks uint16
	NumBlocks uint16
	BytesUsed uint32
	Checksum  uint32
	Pad       uint32
	Binfo     [1]BlockInfo
}

type BlockInfo struct {
	Bnum  uint64
	Bsize uint32
	Next  uint32
}

const (
	HFC_MAGIC               = 0xFF28FF26
	HFC_VERSION             = 1
	HFC_DEFAULT_DURATION    = (3600 * 60)
	HFC_MINIMUM_TEMPERATURE = 16
	HFC_MAXIMUM_FILESIZE    = (10 * 1024 * 1024)
	HfcTag                  = "CLUSTERED HOT FILES B-TREE     "
)

type HotFilesInfo struct {
	Magic       uint32
	Version     uint32
	Duration    uint32 // duration of sample period
	Timebase    uint32 // recording period start time
	Timeleft    uint32 // recording period stop time
	Threshold   uint32
	Maxfileblks uint32
	Maxfilecnt  uint32
	Tag         [32]uint8
}

const HFC_LOOKUPTAG = 0xFFFFFFFF

// #define HFC_KEYLENGTH   (sizeof(HotFileKey) - sizeof(UInt32))

type HotFileKey struct {
	KeyLength   uint16
	ForkType    uint8
	Pad         uint8
	Temperature uint32
	FileID      uint32
}
