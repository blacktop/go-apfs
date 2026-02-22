package hfsplus

import (
	"encoding/binary"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf16"
)

type Signature uint16

const (
	HFSSigWord     Signature = 0x4244 // "BD"
	HFSPlusSigWord Signature = 0x482B // "H+"
	HFSXSigWord    Signature = 0x4858 // "HX"
)

func (s Signature) String() string {
	switch s {
	case HFSPlusSigWord:
		return "HFS+"
	case HFSXSigWord:
		return "HFSX"
	default:
		// Cast to uint16 to avoid infinite recursion
		return fmt.Sprintf("unknown(%#04x)", uint16(s))
	}
}

const (
	HFSPlusVersion uint32 = 0x0004 // 'H+' volumes are version 4 only
	HFSXVersion    uint32 = 0x0005 // 'HX' volumes start with version 5

	HFSPlusMountVersion uint32 = 0x31302E30 // '10.0' for Mac OS X
	HFSJMountVersion    uint32 = 0x4846534a // 'HFSJ' for journaled HFS+ on OS X
	FSKMountVersion     uint32 = 0x46534b21 // 'FSK!' for failed journal replay
	/*
	 * Indirect link files (hard links) have the following type/creator.
	 */
	HardLinkFileType = 0x686C6E6B // 'hlnk'
	HFSPlusCreator   = 0x6866732B //  'hfs+'
	/*
	 *	File type and creator for symbolic links
	 */
	SymLinkFileType uint32 = 0x736C6E6B // 'slnk'
	SymLinkCreator  uint32 = 0x72686170 // 'rhap'

	HFSMaxVolumeNameChars   = 27
	HFSMaxFileNameChars     = 31
	HFSPlusMaxFileNameChars = 255
)

/* HFS and HFS Plus volume attribute bits */
const (
	/* Bits 0-6 are reserved (always cleared by MountVol call) */
	HFSVolumeHardwareLockBit     = 7  /* volume is locked by hardware */
	HFSVolumeUnmountedBit        = 8  /* volume was successfully unmounted */
	HFSVolumeSparedBlocksBit     = 9  /* volume has bad blocks spared */
	HFSVolumeNoCacheRequiredBit  = 10 /* don't cache volume blocks (i.e. RAM or ROM disk) */
	HFSBootVolumeInconsistentBit = 11 /* boot volume is inconsistent (System 7.6 and later) */
	HFSCatalogNodeIDsReusedBit   = 12
	HFSVolumeJournaledBit        = 13 /* this volume has a journal on it */
	HFSVolumeInconsistentBit     = 14 /* serious inconsistencies detected at runtime */
	HFSVolumeSoftwareLockBit     = 15 /* volume is locked by software */
	/*
	 * HFS only has 16 bits of attributes in the MDB, but HFS Plus has 32 bits.
	 * Therefore, bits 16-31 can only be used on HFS Plus.
	 */
	HFSUnusedNodeFixBit     = 31 /* Unused nodes in the Catalog B-tree have been zero-filled.  See Radar #6947811. */
	HFSContentProtectionBit = 30 /* Volume has per-file content protection */
	HFSExpandedTimesBit     = 29 /* Volume has expanded / non-MacOS native timestamps */

	/***  Keep these in sync with the bits above ! ****/
	HFSVolumeHardwareLockMask     = 0x00000080
	HFSVolumeUnmountedMask        = 0x00000100
	HFSVolumeSparedBlocksMask     = 0x00000200
	HFSVolumeNoCacheRequiredMask  = 0x00000400
	HFSBootVolumeInconsistentMask = 0x00000800
	HFSCatalogNodeIDsReusedMask   = 0x00001000
	HFSVolumeJournaledMask        = 0x00002000
	HFSVolumeInconsistentMask     = 0x00004000
	HFSVolumeSoftwareLockMask     = 0x00008000

	/* Bits 16-31 are allocated from high to low */
	HFSExpandedTimesMask     = 0x20000000
	HFSContentProtectionMask = 0x40000000
	HFSUnusedNodeFixMask     = 0x80000000

	HFSMDBAttributesMask = 0x8380
)

type hfsAttributes uint32

func (attr hfsAttributes) String() string {
	var flags []string
	if attr&HFSVolumeHardwareLockMask != 0 {
		flags = append(flags, "HardwareLocked")
	}
	if attr&HFSVolumeUnmountedMask != 0 {
		flags = append(flags, "Unmounted")
	}
	if attr&HFSVolumeSparedBlocksMask != 0 {
		flags = append(flags, "SparedBlocks")
	}
	if attr&HFSVolumeNoCacheRequiredMask != 0 {
		flags = append(flags, "NoCacheRequired")
	}
	if attr&HFSBootVolumeInconsistentMask != 0 {
		flags = append(flags, "Inconsistent")
	}
	if attr&HFSCatalogNodeIDsReusedMask != 0 {
		flags = append(flags, "NodeIDsReused")
	}
	if attr&HFSVolumeJournaledMask != 0 {
		flags = append(flags, "Journaled")
	}
	if attr&HFSVolumeInconsistentMask != 0 {
		flags = append(flags, "Inconsistent")
	}
	if attr&HFSVolumeSoftwareLockMask != 0 {
		flags = append(flags, "SoftwareLocked")
	}
	if attr&HFSUnusedNodeFixMask != 0 {
		flags = append(flags, "UnusedNodeFix")
	}
	if attr&HFSContentProtectionMask != 0 {
		flags = append(flags, "ContentProtection")
	}
	if attr&HFSExpandedTimesMask != 0 {
		flags = append(flags, "ExpandedTimes")
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
	HFSRootParentID           CatalogNodeID = 1  // Parent ID of the root folder
	HFSRootFolderID           CatalogNodeID = 2  // Folder ID of the root folder
	HFSExtentsFileID          CatalogNodeID = 3  // File ID of the extents file
	HFSCatalogFileID          CatalogNodeID = 4  // File ID of the catalog file
	HFSBadBlockFileID         CatalogNodeID = 5  // File ID of the bad allocation block file
	HFSAllocationFileID       CatalogNodeID = 6  // File ID of the allocation file (HFS Plus only)
	HFSStartupFileID          CatalogNodeID = 7  // File ID of the startup file (HFS Plus only)
	HFSAttributesFileID       CatalogNodeID = 8  // File ID of the attribute file (HFS Plus only)
	HFSAttributeDataFileID    CatalogNodeID = 13 // Used in Mac OS X runtime for extent based attributes. (never stored on disk)
	HFSRepairCatalogFileID    CatalogNodeID = 14 // Used when rebuilding Catalog B-tree
	HFSBogusExtentFileID      CatalogNodeID = 15 // Used for exchanging extents in extents file
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

type ExtentDescriptor struct {
	StartBlock uint32
	BlockCount uint32
}

const (
	HFSExtentDensity     = 3
	HFSPlusExtentDensity = 8
)

type ExtentRecord [HFSPlusExtentDensity]ExtentDescriptor

type ForkData struct {
	LogicalSize uint64
	ClumpSize   uint32
	TotalBlocks uint32
	Extents     ExtentRecord
}

type VolumeHeader struct {
	Signature          Signature // 'H+' or 'HX'
	Version            uint16    // 4 for HFS+ and 5 for HFSX
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
	return fmt.Sprintf(
		"Volume Header:\n"+
			"    Signature:           %s\n"+
			"    Version:             %d\n"+
			"    Attributes:          %s\n"+
			"    LastMountedVersion:  %s\n"+
			"    CreateDate:          %s\n"+
			"    ModifyDate:          %s\n"+
			"    BackupDate:          %s\n"+
			"    CheckedDate:         %s\n",
		hdr.Signature,
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
	WindowBounds Rect // The position and dimension of the folder's window
	FinderFlags  uint16
	Location     Point // Folder's location in the parent folder. If set to {0, 0}, the Finder will place the item automatically
	Opaque       uint16
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

type FinderOpaqueInfo struct {
	Opaque [16]int8
}

type RecordType int16

const (
	/* HFS Catalog Records */
	HFSFolderRecord       RecordType = 0x0100
	HFSFileRecord         RecordType = 0x0200
	HFSFolderThreadRecord RecordType = 0x0300
	HFSFileThreadRecord   RecordType = 0x0400

	/* HFS Plus Catalog Records */
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

type CatalogFlags uint16

const (
	HFSFileLockedBit  = 0x0000 /* file is locked and cannot be written to */
	HFSFileLockedMask = 0x0001

	HFSThreadExistsBit  = 0x0001 /* a file thread record exists for this file */
	HFSThreadExistsMask = 0x0002

	HFSHasAttributesBit  = 0x0002 /* object has extended attributes */
	HFSHasAttributesMask = 0x0004

	HFSHasSecurityBit  = 0x0003 /* object has security data (ACLs) */
	HFSHasSecurityMask = 0x0008

	HFSHasFolderCountBit  = 0x0004 /* only for HFSX, folder maintains a separate sub-folder count */
	HFSHasFolderCountMask = 0x0010 /* (sum of folder records and directory hard links) */

	HFSHasLinkChainBit  = 0x0005 /* has hardlink chain (inode or link) */
	HFSHasLinkChainMask = 0x0020

	HFSHasChildLinkBit  = 0x0006 /* folder has a child that's a dir link */
	HFSHasChildLinkMask = 0x0040

	HFSHasDateAddedBit  = 0x0007 /* File/Folder has the date-added stored in the finder info. */
	HFSHasDateAddedMask = 0x0080

	HFSFastDevPinnedBit  = 0x0008 /* this file has been pinned to the fast-device by the hot-file code on cooperative fusion */
	HFSFastDevPinnedMask = 0x0100

	HFSDoNotFastDevPinBit  = 0x0009 /* this file can not be pinned to the fast-device */
	HFSDoNotFastDevPinMask = 0x0200

	HFSFastDevCandidateBit  = 0x000a /* this item is a potential candidate for fast-dev pinning (as are any of its descendents */
	HFSFastDevCandidateMask = 0x0400

	HFSAutoCandidateBit  = 0x000b /* this item was automatically marked as a fast-dev candidate by the kernel */
	HFSAutoCandidateMask = 0x0800

	HFSCatExpandedTimesBit  = 0x000c /* this item has expanded timestamps */
	HFSCatExpandedTimesMask = 0x1000

	// There are only 3 flag bits remaining: 0x2000, 0x4000, 0x8000
)

func (flags CatalogFlags) FileLocked() bool {
	return flags&HFSFileLockedMask != 0
}

func (flags CatalogFlags) ThreadExists() bool {
	return flags&HFSThreadExistsMask != 0
}

func (flags CatalogFlags) HasAttributes() bool {
	return flags&HFSHasAttributesMask != 0
}

func (flags CatalogFlags) HasSecurity() bool {
	return flags&HFSHasSecurityMask != 0
}

func (flags CatalogFlags) HasFolderCount() bool {
	return flags&HFSHasFolderCountMask != 0
}

func (flags CatalogFlags) HasLinkChain() bool {
	return flags&HFSHasLinkChainMask != 0
}

func (flags CatalogFlags) HasChildLink() bool {
	return flags&HFSHasChildLinkMask != 0
}

func (flags CatalogFlags) HasDateAdded() bool {
	return flags&HFSHasDateAddedMask != 0
}

func (flags CatalogFlags) FastDevPinned() bool {
	return flags&HFSFastDevPinnedMask != 0
}

func (flags CatalogFlags) DoNotFastDevPin() bool {
	return flags&HFSDoNotFastDevPinMask != 0
}

func (flags CatalogFlags) FastDevCandidate() bool {
	return flags&HFSFastDevCandidateMask != 0
}

func (flags CatalogFlags) AutoCandidate() bool {
	return flags&HFSAutoCandidateMask != 0
}

func (flags CatalogFlags) CatExpandedTimes() bool {
	return flags&HFSCatExpandedTimesMask != 0
}

func (flags CatalogFlags) String() string {
	var flagStrings []string
	if flags.FileLocked() {
		flagStrings = append(flagStrings, "FileLocked")
	}
	if flags.ThreadExists() {
		flagStrings = append(flagStrings, "ThreadExists")
	}
	if flags.HasAttributes() {
		flagStrings = append(flagStrings, "HasAttributes")
	}
	if flags.HasSecurity() {
		flagStrings = append(flagStrings, "HasSecurity")
	}
	if flags.HasFolderCount() {
		flagStrings = append(flagStrings, "HasFolderCount")
	}
	if flags.HasLinkChain() {
		flagStrings = append(flagStrings, "HasLinkChain")
	}
	if flags.HasChildLink() {
		flagStrings = append(flagStrings, "HasChildLink")
	}
	if flags.HasDateAdded() {
		flagStrings = append(flagStrings, "HasDateAdded")
	}
	if flags.FastDevPinned() {
		flagStrings = append(flagStrings, "FastDevPinned")
	}
	if flags.DoNotFastDevPin() {
		flagStrings = append(flagStrings, "DoNotFastDevPin")
	}
	if flags.FastDevCandidate() {
		flagStrings = append(flagStrings, "FastDevCandidate")
	}
	if flags.AutoCandidate() {
		flagStrings = append(flagStrings, "AutoCandidate")
	}
	if flags.CatExpandedTimes() {
		flagStrings = append(flagStrings, "CatExpandedTimes")
	}
	return strings.Join(flagStrings, ", ")
}

// HFSPlusCatalogFolder is a HFS Plus catalog folder record - 88 bytes
type HFSPlusCatalogFolder struct {
	RecordType       RecordType
	Flags            CatalogFlags
	Valence          uint32
	FolderID         CatalogNodeID
	CreateDate       hfsTime
	ContentModDate   hfsTime
	AttributeModDate hfsTime
	AccessDate       hfsTime
	BackupDate       hfsTime
	BSDInfo          BSDInfo
	UserInfo         FolderInfo
	FinderInfo       FinderOpaqueInfo
	TextEncoding     uint32 // hint for name conversions
	FolderCount      uint32 // number of enclosed folders, active when HasFolderCount is set
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

type FinderFileInfo struct {
	FileType    OSType // The type of the file
	FileCreator OSType // The file's creator
	FinderFlags uint16
	Location    Point // File's location in the folder.
	Opaque      uint16
}

// HFSPlusCatalogFile is a HFS Plus catalog file record - 248 bytes
type HFSPlusCatalogFile struct {
	RecordType       RecordType // == HFSPlusFileRecord
	Flags            CatalogFlags
	Reserved1        uint32 // reserved - initialized as zero
	FileID           CatalogNodeID
	CreateDate       hfsTime
	ContentModDate   hfsTime
	AttributeModDate hfsTime
	AccessDate       hfsTime
	BackupDate       hfsTime
	BSDInfo          BSDInfo
	UserInfo         FinderFileInfo
	FinderInfo       FinderOpaqueInfo
	TextEncoding     uint32
	Reserved2        uint32 // reserved - initialized as zero
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
	FolderInfo HFSPlusCatalogFolder
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
	if err := binary.Read(r, binary.BigEndian, &fdr.Key.KeyLength); err != nil {
		return fmt.Errorf("failed to read folder key length: %v", err)
	}
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

func (fdr *FolderRecord) Type() BTreeNodeKind { return BTLeafNodeKind }

type FileBlock struct {
	Data  []byte
	Slack []byte
}

type FileData struct {
	Block FileBlock
}

type FileRecord struct {
	Key      CatalogKey
	FileInfo HFSPlusCatalogFile
	FileData FileData

	path    string
	blkSize uint32
	r       *io.ReaderAt
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

func (fr *FileRecord) Path() string {
	return fr.path
}

func (fr *FileRecord) Unmarshal(r io.Reader) error {
	if err := binary.Read(r, binary.BigEndian, &fr.Key.KeyLength); err != nil {
		return fmt.Errorf("failed to read file key length: %v", err)
	}
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

func (fr *FileRecord) Type() BTreeNodeKind { return BTLeafNodeKind }

func (fr *FileRecord) Reader() io.Reader {
	// Create a multi-reader that reads from all extents
	var readers []io.Reader
	remainingSize := fr.FileInfo.DataFork.LogicalSize

	for _, extent := range fr.FileInfo.DataFork.Extents {
		if extent.BlockCount == 0 {
			break // No more extents
		}

		extentSize := min(uint64(extent.BlockCount)*uint64(fr.blkSize), remainingSize)

		// HFS+ blocks are relative to the HFS+ volume start, which is at byte 1024 in the partition
		// However, the partition reader expects partition-relative offsets, so we don't add 1024 here
		// The HFS+ volume header at 1024 is just metadata - blocks start at 0 within the volume
		offset := int64(extent.StartBlock) * int64(fr.blkSize)

		readers = append(readers, io.NewSectionReader(*fr.r, offset, int64(extentSize)))

		remainingSize -= extentSize
		if remainingSize == 0 {
			break
		}
	}

	return io.MultiReader(readers...)
}

type ThreadKey struct {
	KeyLength uint16
	ParentID  CatalogNodeID
	Reserved  uint16
}

// HFSPlusCatalogThread is a HFS Plus catalog thread record -- 264 bytes
type HFSPlusCatalogThread struct {
	RecordType RecordType    //  == HFSPlusFolderThreadRecord or HFSPlusFileThreadRecord
	Reserved   int16         // reserved - initialized as zero
	ParentID   CatalogNodeID // parent ID for this catalog node
	NodeName   UniStr255     // name of this catalog node (variable length)
}

type ThreadRecord struct {
	Key ThreadKey
	HFSPlusCatalogThread
}

func (thread *ThreadRecord) String() string {
	return fmt.Sprintf("Thread Record: '%s', ParentID: %d, RecordType: %s", &thread.NodeName, thread.ParentID, thread.RecordType)
}

func (t *ThreadRecord) Unmarshal(r io.Reader) error {
	if err := binary.Read(r, binary.BigEndian, &t.Key); err != nil {
		return fmt.Errorf("failed to read thread key: %v", err)
	}
	if err := binary.Read(r, binary.BigEndian, &t.RecordType); err != nil {
		return fmt.Errorf("failed to read thread record type: %v", err)
	}
	if err := binary.Read(r, binary.BigEndian, &t.Reserved); err != nil {
		return fmt.Errorf("failed to read thread reserved field: %v", err)
	}
	if err := binary.Read(r, binary.BigEndian, &t.ParentID); err != nil {
		return fmt.Errorf("failed to read thread parent ID: %v", err)
	}
	if err := binary.Read(r, binary.BigEndian, &t.NodeName.Length); err != nil {
		return fmt.Errorf("failed to read thread name length: %v", err)
	}
	t.NodeName.UniChar = make([]uint16, t.NodeName.Length)
	if err := binary.Read(r, binary.BigEndian, &t.NodeName.UniChar); err != nil {
		return fmt.Errorf("failed to read thread name: %v", err)
	}
	return nil
}

func (thread *ThreadRecord) Type() BTreeNodeKind { return BTLeafNodeKind }

// #ifdef __APPLE_API_UNSTABLE
/*
 * 	These are the types of records in the attribute B-tree.  The values were
 * 	chosen so that they wouldn't conflict with the catalog record types.
 */
const (
	HFSPlusAttrInlineData = 0x10 /* attributes whose data fits in a b-tree node */
	HFSPlusAttrForkData   = 0x20 /* extent based attributes (data lives in extents) */
	HFSPlusAttrExtents    = 0x30 /* overflow extents for large attributes */
)

// TODO: add the rest of the __APPLE_API_UNSTABLE types from 'Xcode.app/Contents/Developer/Platforms/MacOSX.platform/Developer/SDKs/MacOSX.sdk/usr/include/hfs/hfs_format.h'

/* B-tree structures */

const BTreeMaxKeyLength = 520

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
	FLink      uint32        // next node at this level
	BLink      uint32        // previous node at this level
	Kind       BTreeNodeKind //  kind of node (leaf, index, header, map)
	Height     uint8         // zero for header, map; child is one more than parent
	NumRecords uint16        // number of records in this node
	Reserved   uint16
}

type BtreeType uint8

const (
	BtHFS      BtreeType = 0   // control file
	BtUser     BtreeType = 128 // user btree type starts from 128
	BtReserved BtreeType = 255 // reserved
)

/* Constants for BTHeaderRec attributes */
type BTHeaderAttributes uint32

const (
	BTBadCloseMask          BTHeaderAttributes = 0x00000001 /* reserved */
	BTBigKeysMask           BTHeaderAttributes = 0x00000002 /* key length field is 16 bits */
	BTVariableIndexKeysMask BTHeaderAttributes = 0x00000004 /* keys in index nodes are variable length */
)

/* Catalog Key Name Comparison Type */
type BTHeaderKeyCompareType uint8

const (
	HFSCaseFolding   BTHeaderKeyCompareType = 0xCF /* case folding (case-insensitive) */
	HFSBinaryCompare BTHeaderKeyCompareType = 0xBC /* binary compare (case-sensitive) */
)

// The first record of a B-tree header node
type BTHeaderRec struct {
	TreeDepth      uint16 //  maximum height (usually leaf nodes)
	RootNode       uint32 //  node number of root node
	LeafRecords    uint32 //  number of leaf records in all leaf nodes
	FirstLeafNode  uint32 // node number of first leaf node
	LastLeafNode   uint32 // node number of last leaf node
	NodeSize       uint16 // size of each node in bytes
	MaxKeyLength   uint16 // maximum key length
	TotalNodes     uint32 // total number of nodes in the tree
	FreeNodes      uint32 // number of unused (free) nodes in tree
	Reserved1      uint16
	ClumpSize      uint32
	BtreeType      BtreeType
	KeyCompareType BTHeaderKeyCompareType // Key string Comparison Type
	Attributes     BTHeaderAttributes     // persistent attributes about the tree
	Reserved3      [16]uint32
}

type BTHeaderNode struct {
	Descriptor     BTNodeDescriptor
	Header         BTHeaderRec
	UserDataRecord [128]uint8
	Map            []uint8
	Offsets        [4]uint16
}

func (hdr BTHeaderNode) OffsetsOffset() uint16 {
	return hdr.Offsets[0]
}
func (hdr BTHeaderNode) MapOffset() uint16 {
	return hdr.Offsets[1]
}
func (hdr BTHeaderNode) UserDataRecordsOffset() uint16 {
	return hdr.Offsets[2]
}
func (hdr BTHeaderNode) HeaderOffset() uint16 {
	return hdr.Offsets[3]
}

// BTRecord represents a record in a B-tree node: CatalogRecord, FolderRecord, FileRecord
type BTRecord interface {
	Unmarshal(r io.Reader) error
	String() string
	Type() BTreeNodeKind
}

type BTNode struct {
	Descriptor BTNodeDescriptor
	Records    []BTRecord
	NodeSize   int // added: node size (in bytes) from the header record
}

// BTNode represents a B-tree node in the filesystem
type BTree struct {
	BTHeaderNode
	Root *BTNode
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
	if err := binary.Read(r, binary.BigEndian, &cr.Key.KeyLength); err != nil {
		return fmt.Errorf("failed to read catalog key length: %v", err)
	}
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

func (cr *CatalogRecord) Type() BTreeNodeKind { return BTIndexNodeKind }
