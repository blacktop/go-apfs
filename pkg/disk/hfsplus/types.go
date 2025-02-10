package hfsplus

import (
	"encoding/binary"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf16"
)

const (
	HFSPlusSigWord = 0x482B // "H+"
	HFSXSigWord    = 0x4858 // "HX"
)

const (
	kHFSVolumeUnmountedBit    = 0x00000001 // volume was not unmounted cleanly
	kHFSVolumeJournaledBit    = 0x00000002 // volume uses journaling
	kHFSVolume32BitUIDsBit    = 0x00000004 // uses 32-bit UID/GID fields
	kHFSVolumeCompressionBit  = 0x00000008 // supports file compression
	kHFSVolumeHardwareLockBit = 0x00000010 // volume is hardware locked
	kHFSVolumeSoftwareLockBit = 0x00000020 // volume is software locked
	// You can add more flag constants here if needed.
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

type hfsAttributes uint32

func (attr hfsAttributes) String() string {
	var flags []string
	if attr&kHFSVolumeUnmountedBit != 0 {
		flags = append(flags, "Unmounted")
	}
	if attr&kHFSVolumeJournaledBit != 0 {
		flags = append(flags, "Journaled")
	}
	if attr&kHFSVolume32BitUIDsBit != 0 {
		flags = append(flags, "32BitUIDs")
	}
	if attr&kHFSVolumeCompressionBit != 0 {
		flags = append(flags, "Compressed")
	}
	if attr&kHFSVolumeHardwareLockBit != 0 {
		flags = append(flags, "HardwareLocked")
	}
	if attr&kHFSVolumeSoftwareLockBit != 0 {
		flags = append(flags, "SoftwareLocked")
	}
	if len(flags) == 0 {
		return "None"
	}
	return strings.Join(flags, ", ")
}

type hfsTime uint32

func (t hfsTime) Time() time.Time {
	// The HFS+ epoch starts at January 1, 1904, GMT.
	hfsEpoch := time.Date(1904, time.January, 1, 0, 0, 0, 0, time.UTC)
	return hfsEpoch.Add(time.Duration(t) * time.Second)
}

func (t hfsTime) String() string {
	if t == 0 {
		return "-"
	}
	return t.Time().Format(time.RFC1123)
}

type UniStr255 struct {
	Length  uint16
	UniChar []uint16
}

// UniStr255FromString creates a UniStr255 from a Go string
func UniStr255FromString(s string) UniStr255 {
	var us UniStr255
	us.Length = uint16(len(s))
	for i, r := range s {
		if i >= 255 {
			break
		}
		us.UniChar[i] = uint16(r)
	}
	return us
}

func (us *UniStr255) String() string {
	return string(utf16.Decode(us.UniChar[:us.Length]))
}

type BSDInfo struct {
	OwnerID    uint32
	GroupID    uint32
	AdminFlags uint8
	OwnerFlags uint8
	FileMode   uint16
	// union {
	// UInt32  iNodeNum;
	// UInt32  linkCount;
	// UInt32  rawDevice;
	// } special;
	Special uint32
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

func (id CatalogNodeID) String() string {
	switch id {
	case HFSRootParentID:
		return "RootParent"
	case HFSRootFolderID:
		return "RootFolder"
	case HFSExtentsFileID:
		return "ExtentsFile"
	case HFSCatalogFileID:
		return "CatalogFile"
	case HFSBadBlockFileID:
		return "BadBlockFile"
	case HFSAllocationFileID:
		return "AllocationFile"
	case HFSStartupFileID:
		return "StartupFile"
	case HFSAttributesFileID:
		return "AttributesFile"
	case HFSRepairCatalogFileID:
		return "RepairCatalogFile"
	case HFSBogusExtentFileID:
		return "BogusExtentFile"
	case HFSFirstUserCatalogNodeID:
		return "FirstUserCatalogNode"
	default:
		return fmt.Sprintf("Unknown CatalogNodeID(%d)", id)
	}
}

type ExtentRecord [8]ExtentDescriptor

type ExtentDescriptor struct {
	StartBlock uint32
	BlockCount uint32
}

type ForkData struct {
	LogicalSize uint64
	ClumpSize   uint32
	TotalBlocks uint32
	Extents     ExtentRecord
}

type VolumeHeader struct {
	Signature          uint16 // 'H+' or 'HX'
	Version            uint16 // 4 for HFS+ and 5 for HFSX
	Attributes         hfsAttributes
	LastMountedVersion [4]byte
	JournalInfoBlock   uint32
	CreateDate         hfsTime
	ModifyDate         hfsTime
	BackupDate         hfsTime
	CheckedDate        hfsTime
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
	_                  [512]uint8 // Corrected reserved space
}

func (hdr *VolumeHeader) String() string {
	var sig string
	switch hdr.Signature {
	case HFSPlusSigWord:
		sig = "H+"
	case HFSXSigWord:
		sig = "HX"
	default:
		sig = fmt.Sprintf("%x", hdr.Signature)
	}
	return fmt.Sprintf(
		"Signature:          %s\n"+
			"Version:            %d\n"+
			"Attributes:         %s\n"+
			"LastMountedVersion: %s\n"+
			"CreateDate:         %s\n"+
			"ModifyDate:         %s\n"+
			"BackupDate:         %s\n"+
			"CheckedDate:        %s\n",
		sig,
		hdr.Version,
		hdr.Attributes.String(),
		string(hdr.LastMountedVersion[:]),
		hdr.CreateDate.String(),
		hdr.ModifyDate.String(),
		hdr.BackupDate.String(),
		hdr.CheckedDate.String(),
	)
}

/* Catalog */

type Rect struct {
	Top    int16
	Left   int16
	Bottom int16
	Right  int16
}

type Point struct {
	V int16
	H int16
}

type FolderInfo struct {
	WindowBounds  Rect // The position and dimension of the folder's window
	FinderFlags   uint16
	Location      Point // Folder's location in the parent folder. If set to {0, 0}, the Finder will place the item automatically
	ReservedField uint16
}

type ExtendedFinderFlags uint16

const (
	ExtendedFlagsAreInvalid    ExtendedFinderFlags = 0x8000 // The other extended flags should be ignored
	ExtendedFlagHasCustomBadge ExtendedFinderFlags = 0x0100 // The file or folder has a badge resource
	ExtendedFlagHasRoutingInfo ExtendedFinderFlags = 0x0004 // The file contains routing info resource
)

type ExtendedFolderInfo struct {
	ScrollPosition      Point // Scroll position (for icon views)
	Reserved1           int32
	ExtendedFinderFlags ExtendedFinderFlags
	Reserved2           int16
	PutAwayFolderID     int32
}

type Catalog struct {
	Header BTHeaderNode
	Root   BTNode
}

type RecordType int16

const (
	HFSPlusNoneRecord         RecordType = 0x0000
	HFSPlusFolderRecord       RecordType = 0x0001
	HFSPlusFileRecord         RecordType = 0x0002
	HFSPlusFolderThreadRecord RecordType = 0x0003
	HFSPlusFileThreadRecord   RecordType = 0x0004
)

func (rt RecordType) String() string {
	switch rt {
	case HFSPlusFolderRecord:
		return "Folder"
	case HFSPlusFileRecord:
		return "File"
	case HFSPlusFolderThreadRecord:
		return "FolderThread"
	case HFSPlusFileThreadRecord:
		return "FileThread"
	default:
		return fmt.Sprintf("Unknown (%d)", rt)
	}
}

type CatalogFolder struct {
	RecordType       RecordType
	Flags            uint16
	Valence          uint32
	FolderID         CatalogNodeID
	CreateDate       hfsTime
	ContentModDate   hfsTime
	AttributeModDate hfsTime
	AccessDate       hfsTime
	BackupDate       hfsTime
	Permissions      BSDInfo
	UserInfo         FolderInfo
	FinderInfo       ExtendedFolderInfo
	TextEncoding     uint32
	Reserved         uint32
}

type ExtendedFileInfo struct {
	Reserved1           [4]int16
	ExtendedFinderFlags uint16
	Reserved2           int16
	PutAwayFolderID     int32
}

type FourCharCode uint32

type OSType FourCharCode

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

type FileInfo struct {
	FileType      OSType // The type of the file
	FileCreator   OSType // The file's creator
	FinderFlags   uint16
	Location      Point // File's location in the folder.
	ReservedField uint16
}

type CatalogFile struct {
	RecordType       RecordType
	Flags            uint16
	Reserved1        uint32
	FileID           CatalogNodeID
	CreateDate       hfsTime
	ContentModDate   hfsTime
	AttributeModDate hfsTime
	AccessDate       hfsTime
	BackupDate       hfsTime
	Permissions      BSDInfo
	UserInfo         FileInfo
	FinderInfo       ExtendedFileInfo
	TextEncoding     uint32
	Reserved2        uint32
	DataFork         ForkData
	ResourceFork     ForkData
}

type CatalogKey struct {
	KeyLength uint16
	ParentID  CatalogNodeID
	NodeName  UniStr255
}

/* File & Folder Records */

type FolderRecord struct {
	Key        CatalogKey
	FolderInfo CatalogFolder
	SubDir     []BTRecord // folders and files
}

func (fdr *FolderRecord) String() string {
	return fmt.Sprintf("'%s' parent=%d, folderID=%d, type=%s, created=%s",
		&fdr.Key.NodeName,
		fdr.Key.ParentID,
		fdr.FolderInfo.FolderID,
		fdr.FolderInfo.RecordType,
		fdr.FolderInfo.CreateDate,
	)
}

func (fdr *FolderRecord) Unmarshal(r io.Reader) error {
	if err := binary.Read(r, binary.BigEndian, &fdr.Key.ParentID); err != nil {
		return fmt.Errorf("failed to read folder key parent ID: %v", err)
	}
	if err := binary.Read(r, binary.BigEndian, &fdr.Key.NodeName.Length); err != nil {
		return fmt.Errorf("failed to read folder key name length: %v", err)
	}
	fdr.Key.NodeName.UniChar = make([]uint16, fdr.Key.NodeName.Length)
	if err := binary.Read(r, binary.BigEndian, &fdr.Key.NodeName.UniChar); err != nil {
		return fmt.Errorf("failed to read folder key name: %v", err)
	}
	if err := binary.Read(r, binary.BigEndian, &fdr.FolderInfo); err != nil {
		return fmt.Errorf("failed to read folder info: %v", err)
	}
	return nil
}

type FileBlock struct {
	Data  []byte
	Slack []byte
}

type FileData struct {
	Block FileBlock
}

type FileRecord struct {
	Key      CatalogKey
	FileInfo CatalogFile
	FileData FileData
}

func (fr *FileRecord) String() string {
	return fmt.Sprintf("'%s' parent=%d, fileID=%d, type=%s, created=%s",
		&fr.Key.NodeName,
		fr.Key.ParentID,
		fr.FileInfo.FileID,
		fr.FileInfo.RecordType,
		fr.FileInfo.CreateDate,
	)
}

func (fr *FileRecord) Unmarshal(r io.Reader) error {
	if err := binary.Read(r, binary.BigEndian, &fr.Key.ParentID); err != nil {
		return fmt.Errorf("failed to read file key parent ID: %v", err)
	}
	if err := binary.Read(r, binary.BigEndian, &fr.Key.NodeName.Length); err != nil {
		return fmt.Errorf("failed to read file key name length: %v", err)
	}
	fr.Key.NodeName.UniChar = make([]uint16, fr.Key.NodeName.Length)
	if err := binary.Read(r, binary.BigEndian, &fr.Key.NodeName.UniChar); err != nil {
		return fmt.Errorf("failed to read file key name: %v", err)
	}
	if err := binary.Read(r, binary.BigEndian, &fr.FileInfo); err != nil {
		return fmt.Errorf("failed to read file.fileinfo: %v", err)
	}
	return nil
}

/* B-tree */

type BTreeNodeKind int8

const (
	BTLeafNodeKind   BTreeNodeKind = -1
	BTIndexNodeKind  BTreeNodeKind = 0
	BTHeaderNodeKind BTreeNodeKind = 1
	BTMapNodeKind    BTreeNodeKind = 2
)

func (kind BTreeNodeKind) String() string {
	switch kind {
	case BTLeafNodeKind:
		return "Leaf"
	case BTIndexNodeKind:
		return "Index"
	case BTHeaderNodeKind:
		return "Header"
	case BTMapNodeKind:
		return "Map"
	default:
		return fmt.Sprintf("Unknown (%d)", kind)
	}
}

type BTNodeDescriptor struct {
	FLink      uint32
	BLink      uint32
	Kind       BTreeNodeKind
	Height     uint8
	NumRecords uint16
	Reserved   uint16
}

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

type BTHeaderNode struct {
	Descriptor     BTNodeDescriptor
	Header         BTHeaderRec
	UserDataRecord [128]uint8
	Map            []uint8
	Offsets        [4]uint16
}

// BTRecord represents a record in a B-tree node: CatalogRecord, FolderRecord, FileRecord
type BTRecord interface {
	// KeyLength() uint16
	// Key() []byte
	// RecordType() RecordType
	// RecordData() []byte
	Unmarshal(r io.Reader) error
	String() string
}

// BTNode represents a B-tree node in the filesystem
type BTNode struct {
	Descriptor BTNodeDescriptor
	Records    []BTRecord
	NodeSize   int // added: node size (in bytes) from the header record
}

type CatalogRecord struct {
	Key      CatalogKey
	Link     uint32
	ChildPos int64
	Child    BTNode
}

func (cr *CatalogRecord) String() string {
	return fmt.Sprintf("'%s' parent=%d, link=%d", &cr.Key.NodeName, cr.Key.ParentID, cr.Link)
}

func (cr *CatalogRecord) Unmarshal(r io.Reader) error {
	if err := binary.Read(r, binary.BigEndian, &cr.Key.ParentID); err != nil {
		return fmt.Errorf("failed to read catalog key parent ID: %v", err)
	}
	if err := binary.Read(r, binary.BigEndian, &cr.Key.NodeName.Length); err != nil {
		return fmt.Errorf("failed to read catalog key name length: %v", err)
	}
	cr.Key.NodeName.UniChar = make([]uint16, cr.Key.NodeName.Length)
	if err := binary.Read(r, binary.BigEndian, &cr.Key.NodeName.UniChar); err != nil {
		return fmt.Errorf("failed to read catalog key name: %v", err)
	}
	if err := binary.Read(r, binary.BigEndian, &cr.Link); err != nil {
		return fmt.Errorf("failed to read catalog record link: %v", err)
	}
	return nil
}
