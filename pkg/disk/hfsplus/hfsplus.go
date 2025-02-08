package hfsplus

// reference: https://developer.apple.com/library/archive/technotes/tn/tn1150.html

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

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

// HFSPlus represents a mounted HFS+ filesystem
type HFSPlus struct {
	volume *VolumeHeader
	device io.ReaderAt
	closer io.Closer // Add this field to track closeable resources

	// Cache commonly accessed structures
	catalogBTree    *BTNode
	extentsBTree    *BTNode
	attributesBTree *BTNode
}

// BTNode represents a B-tree node in the filesystem
type BTNode struct {
	descriptor BTNodeDescriptor
	records    []BTRecord
}

// BTKeyedRecord represents a B-tree record with a key
type BTKeyedRecord interface {
	Key() any
}

// BTIndexRecord represents a B-tree record with a key and a child node
type BTIndexRecord interface {
	BTKeyedRecord
	ChildNode() uint32
}

// CatalogRecord represents a record in the catalog B-tree
type CatalogRecord interface {
	GetRecordType() int16
}

// Add GetRecordType methods to CatalogFile and CatalogFolder:
func (f *CatalogFile) GetRecordType() int16   { return f.RecordType }
func (f *CatalogFolder) GetRecordType() int16 { return f.RecordType }

// Add after CatalogRecord interface:

type CatalogRecordData struct {
	RecordType int16
	Data       []byte
}

func (r *CatalogRecordData) GetRecordType() int16 { return r.RecordType }
func (r *CatalogRecordData) ReadFrom(reader io.Reader, keyLength uint16) error {
	return binary.Read(reader, binary.BigEndian, &r.Data)
}

// Open creates a new HFSPlus instance from a file path
func Open(filename string) (*HFSPlus, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to open file %s: %v", filename, err)
	}
	// Create new HFSPlus instance
	fs, err := New(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	fs.closer = f
	return fs, nil
}

// Close releases any resources associated with the filesystem
func (fs *HFSPlus) Close() error {
	if fs.closer != nil {
		return fs.closer.Close()
	}
	return nil
}

// New creates a new HFSPlus instance from a device
func New(device io.ReaderAt) (*HFSPlus, error) {
	fs := &HFSPlus{
		device: device,
	}

	// Read volume header
	var header VolumeHeader
	if err := binary.Read(io.NewSectionReader(device, 1024, int64(binary.Size(header))), binary.BigEndian, &header); err != nil {
		return nil, fmt.Errorf("failed to read volume header: %v", err)
	}

	// Verify signature
	if header.Signature != HFSPlusSigWord && header.Signature != HFSXSigWord {
		return nil, fmt.Errorf("invalid HFS+ signature: %x", header.Signature)
	}

	fs.volume = &header

	fmt.Println(fs.volume)

	// Initialize B-trees
	if err := fs.initBTrees(); err != nil {
		return nil, err
	}

	return fs, nil
}

// initBTrees initializes the catalog, extents and attributes B-trees
func (fs *HFSPlus) initBTrees() (err error) {
	// Initialize catalog B-tree
	fs.catalogBTree, err = fs.readBTree(fs.volume.CatalogFile)
	if err != nil {
		return fmt.Errorf("failed to read catalog B-tree: %v", err)
	}

	// Initialize extents B-tree
	fs.extentsBTree, err = fs.readBTree(fs.volume.ExtentsFile)
	if err != nil {
		return fmt.Errorf("failed to read extents B-tree: %v", err)
	}

	// Initialize attributes B-tree if it exists
	if fs.volume.AttributesFile.LogicalSize > 0 {
		fs.attributesBTree, err = fs.readBTree(fs.volume.AttributesFile)
		if err != nil {
			return fmt.Errorf("failed to read attributes B-tree: %v", err)
		}
	}

	return nil
}

// readBTree reads a B-tree structure from the given fork data
func (fs *HFSPlus) readBTree(fork ForkData) (*BTNode, error) {
	// Read header node (always the first node)
	var nodeDesc BTNodeDescriptor
	headerOffset := int64(fork.Extents[0].StartBlock) * int64(fs.volume.BlockSize)

	if err := binary.Read(io.NewSectionReader(fs.device,
		headerOffset, // Start of the B-tree header node
		int64(binary.Size(nodeDesc))), binary.BigEndian, &nodeDesc); err != nil {
		return nil, fmt.Errorf("failed to read node descriptor: %v", err)
	}

	// Verify node kind is BTHeaderNode (1) for the header node
	if nodeDesc.Kind != BTHeaderNode {
		return nil, fmt.Errorf("invalid header node kind: %d", nodeDesc.Kind)
	}

	// Read B-tree header record
	var headerRec BTHeaderRec
	if err := binary.Read(io.NewSectionReader(fs.device,
		headerOffset+int64(binary.Size(nodeDesc)), // Skip over node descriptor
		int64(binary.Size(headerRec))), binary.BigEndian, &headerRec); err != nil {
		return nil, fmt.Errorf("failed to read header record: %v", err)
	}

	// Now read the root node
	rootOffset := int64(fork.Extents[0].StartBlock)*int64(fs.volume.BlockSize) +
		int64(headerRec.RootNode)*int64(headerRec.NodeSize)

	var rootDesc BTNodeDescriptor
	if err := binary.Read(io.NewSectionReader(fs.device,
		rootOffset,
		int64(binary.Size(rootDesc))), binary.BigEndian, &rootDesc); err != nil {
		return nil, fmt.Errorf("failed to read root node descriptor: %v", err)
	}

	// Create BTNode for root
	node := &BTNode{
		descriptor: rootDesc,
	}

	// Read records based on node kind
	switch node.descriptor.Kind {
	case BTLeafNode:
		// Read record offsets
		offsetReader := io.NewSectionReader(fs.device,
			rootOffset+int64(fs.volume.BlockSize)-2*int64(node.descriptor.NumRecords),
			2*int64(node.descriptor.NumRecords))

		offsets := make([]uint16, node.descriptor.NumRecords)
		if err := binary.Read(offsetReader, binary.BigEndian, &offsets); err != nil {
			return nil, fmt.Errorf("failed to read record offsets: %v", err)
		}

		// Read each record
		node.records = make([]BTRecord, node.descriptor.NumRecords)
		for i := uint16(0); i < node.descriptor.NumRecords; i++ {
			recordStart := offsets[i]
			var recordLength uint16
			if i == node.descriptor.NumRecords-1 {
				recordLength = uint16(fs.volume.BlockSize) - recordStart
			} else {
				recordLength = offsets[i+1] - recordStart
			}

			recordReader := io.NewSectionReader(fs.device,
				rootOffset+int64(recordStart),
				int64(recordLength))

			// Read key length and key
			var keyLength uint16
			if err := binary.Read(recordReader, binary.BigEndian, &keyLength); err != nil {
				return nil, fmt.Errorf("failed to read key length: %v", err)
			}

			// Create appropriate record type based on B-tree type
			var record BTRecord
			switch {
			case fork.LogicalSize == fs.volume.CatalogFile.LogicalSize:
				record = &CatalogRecordData{}
			case fork.LogicalSize == fs.volume.ExtentsFile.LogicalSize:
				record = &ExtentRecord{}
			case fork.LogicalSize == fs.volume.AttributesFile.LogicalSize:
				record = &AttributeRecord{}
			default:
				return nil, fmt.Errorf("unknown B-tree type")
			}

			if err := record.ReadFrom(recordReader, keyLength); err != nil {
				return nil, fmt.Errorf("failed to read record: %v", err)
			}
			node.records[i] = record
		}

	case BTIndexNode:
		// Similar logic for index nodes
		// Read pointers to child nodes and keys
		offsetReader := io.NewSectionReader(fs.device,
			rootOffset+int64(fs.volume.BlockSize)-2*int64(node.descriptor.NumRecords),
			2*int64(node.descriptor.NumRecords))

		offsets := make([]uint16, node.descriptor.NumRecords)
		if err := binary.Read(offsetReader, binary.BigEndian, &offsets); err != nil {
			return nil, fmt.Errorf("failed to read index record offsets: %v", err)
		}

		node.records = make([]BTRecord, node.descriptor.NumRecords)
		for i := uint16(0); i < node.descriptor.NumRecords; i++ {
			// Similar record reading logic as leaf nodes
			// But with BTIndexRecord type
			// ...
		}

	default:
		return nil, fmt.Errorf("invalid node kind: %d", node.descriptor.Kind)
	}

	return node, nil
}

// readBTNode reads a B-tree node at the given node number
func (fs *HFSPlus) readBTNode(nodeNum uint32) (*BTNode, error) {
	// Read node descriptor
	nodeDesc := &BTNodeDescriptor{}
	if err := binary.Read(io.NewSectionReader(fs.device,
		int64(nodeNum)*int64(fs.volume.BlockSize),
		int64(binary.Size(nodeDesc))), binary.BigEndian, nodeDesc); err != nil {
		return nil, err
	}

	// Create BTNode
	node := &BTNode{
		descriptor: *nodeDesc,
	}

	return node, nil
}

// Lookup looks up a file or directory by path
func (fs *HFSPlus) Lookup(path string) (*CatalogFile, error) {
	// Start at root directory
	parentID := HFSRootFolderID

	// Split path into components
	components := strings.Split(path, "/")

	for _, component := range components {
		if component == "" {
			continue
		}

		// Search catalog B-tree for component
		key := CatalogKey{
			ParentID: parentID,
			NodeName: UniStr255FromString(component),
		}

		// Read catalog B-tree node
		node, err := fs.readBTree(fs.volume.CatalogFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read catalog B-tree: %v", err)
		}

		// Traverse B-tree to find record
		var record CatalogRecord
		found := false

		for !found {
			switch node.descriptor.Kind {
			case BTIndexNode:
				// Search through index records to find next node
				nextNode := uint32(0)
				for i := uint16(0); i < node.descriptor.NumRecords; i++ {
					// Read index record
					var indexKey CatalogKey
					if err := binary.Read(io.NewSectionReader(fs.device,
						int64(node.descriptor.FLink)*int64(fs.volume.BlockSize),
						int64(binary.Size(indexKey))), binary.BigEndian, &indexKey); err != nil {
						return nil, fmt.Errorf("failed to read index key: %v", err)
					}

					// Compare keys
					if indexKey.Compare(&key) > 0 {
						// Found next node to traverse
						nextNode = node.descriptor.FLink
						break
					}
				}

				if nextNode == 0 {
					return nil, fmt.Errorf("record not found")
				}

				// Read next node
				node, err = fs.readBTree(ForkData{
					Extents: ExtentRecord{{StartBlock: nextNode, BlockCount: 1}},
				})
				if err != nil {
					return nil, fmt.Errorf("failed to read next node: %v", err)
				}

			case BTLeafNode:
				// Search leaf records for match
				for i := uint16(0); i < node.descriptor.NumRecords; i++ {
					// Read record key
					var recordKey CatalogKey
					if err := binary.Read(io.NewSectionReader(fs.device,
						int64(node.descriptor.FLink)*int64(fs.volume.BlockSize),
						int64(binary.Size(recordKey))), binary.BigEndian, &recordKey); err != nil {
						return nil, fmt.Errorf("failed to read record key: %v", err)
					}

					// Compare keys
					if recordKey.Compare(&key) == 0 {
						// Found matching record
						if err := binary.Read(io.NewSectionReader(fs.device,
							int64(node.descriptor.FLink)*int64(fs.volume.BlockSize),
							int64(binary.Size(record))), binary.BigEndian, &record); err != nil {
							return nil, fmt.Errorf("failed to read record: %v", err)
						}
						found = true
						break
					}
				}

				if !found {
					return nil, fmt.Errorf("record not found")
				}

			default:
				return nil, fmt.Errorf("invalid node kind: %d", node.descriptor.Kind)
			}
		}

		// Update parent ID for next iteration
		switch r := record.(type) {
		case *CatalogFile:
			if r.RecordType != kHFSPlusFileRecord {
				return nil, fmt.Errorf("not a file")
			}
			return r, nil
		case *CatalogFolder:
			if r.RecordType != kHFSPlusFolderRecord {
				return nil, fmt.Errorf("not a folder")
			}
			parentID = r.FolderID
		default:
			return nil, fmt.Errorf("invalid record type")
		}
	}

	return nil, fmt.Errorf("path not found: %s", path)
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

// ReadFile reads the contents of a file
func (fs *HFSPlus) ReadFile(file *CatalogFile) ([]byte, error) {
	// Calculate total size
	size := int64(file.DataFork.LogicalSize)
	if size == 0 {
		return nil, nil
	}

	// Create buffer
	data := make([]byte, size)

	// Read from extents
	var offset int64
	for _, extent := range file.DataFork.Extents {
		if extent.BlockCount == 0 {
			continue
		}

		// Calculate size to read
		readSize := int64(extent.BlockCount) * int64(fs.volume.BlockSize)
		if readSize+offset > size {
			readSize = size - offset
		}

		// Read blocks
		if _, err := fs.device.ReadAt(data[offset:offset+readSize],
			int64(extent.StartBlock)*int64(fs.volume.BlockSize)); err != nil {
			return nil, err
		}

		offset += readSize
	}

	return data, nil
}

// searchBTree searches a B-tree for a given key
func (fs *HFSPlus) searchBTree(node *BTNode, key any) (any, error) {
	switch node.descriptor.Kind {
	case BTLeafNode:
		// Search leaf node records
		for _, record := range node.records {
			// Get key from record
			recordKey, ok := record.(BTKeyedRecord)
			if !ok {
				continue
			}

			// Compare keys based on type
			switch k := key.(type) {
			case *CatalogKey:
				if rk, ok := recordKey.Key().(*CatalogKey); ok {
					// Compare parent ID first
					if k.ParentID < rk.ParentID {
						continue
					}
					if k.ParentID > rk.ParentID {
						continue
					}

					// Compare node names
					if k.NodeName.Length < rk.NodeName.Length {
						continue
					}
					if k.NodeName.Length > rk.NodeName.Length {
						continue
					}

					// Compare Unicode characters
					match := true
					for i := uint16(0); i < k.NodeName.Length; i++ {
						if k.NodeName.UniChar[i] != rk.NodeName.UniChar[i] {
							match = false
							break
						}
					}
					if match {
						return record, nil
					}
				}
			case *ExtentKey:
				if rk, ok := recordKey.Key().(*ExtentKey); ok {
					// Compare file ID first
					if k.FileID < rk.FileID {
						continue
					}
					if k.FileID > rk.FileID {
						continue
					}

					// Compare fork type
					if k.ForkType != rk.ForkType {
						continue
					}

					// Compare start block
					if k.StartBlock < rk.StartBlock {
						continue
					}
					if k.StartBlock > rk.StartBlock {
						continue
					}

					return record, nil
				}
			}
		}
		return nil, fmt.Errorf("key not found")

	case BTIndexNode:
		// Find child node to traverse
		var nextNode uint32
		for i := uint16(0); i < node.descriptor.NumRecords; i++ {
			record := node.records[i]
			recordKey := record.(BTKeyedRecord)

			// Compare keys to find appropriate child node
			switch k := key.(type) {
			case *CatalogKey:
				if rk, ok := recordKey.Key().(*CatalogKey); ok {
					if k.ParentID > rk.ParentID {
						continue
					}
					// Found the right branch to follow
					nextNode = record.(BTIndexRecord).ChildNode()
					break
				}
			case *ExtentKey:
				if rk, ok := recordKey.Key().(*ExtentKey); ok {
					if k.FileID > rk.FileID {
						continue
					}
					// Found the right branch to follow
					nextNode = record.(BTIndexRecord).ChildNode()
					break
				}
			}
		}

		if nextNode == 0 {
			return nil, fmt.Errorf("key not found in index node")
		}

		// Read and search the child node
		childNode, err := fs.readBTNode(nextNode)
		if err != nil {
			return nil, fmt.Errorf("failed to read child node: %v", err)
		}
		return fs.searchBTree(childNode, key)

	default:
		return nil, fmt.Errorf("invalid node kind: %d", node.descriptor.Kind)
	}
}

const (
	kHFSPlusFolderRecord    = 1
	kHFSPlusFileRecord      = 2
	kHFSPlusFolderThreadRec = 3
	kHFSPlusFileThreadRec   = 4
)

// Compare compares this key with another CatalogKey
func (k *CatalogKey) Compare(other *CatalogKey) int {
	// Compare parent ID first
	if k.ParentID < other.ParentID {
		return -1
	}
	if k.ParentID > other.ParentID {
		return 1
	}

	// Compare node names
	if k.NodeName.Length < other.NodeName.Length {
		return -1
	}
	if k.NodeName.Length > other.NodeName.Length {
		return 1
	}

	// Compare Unicode characters
	for i := uint16(0); i < k.NodeName.Length; i++ {
		if k.NodeName.UniChar[i] < other.NodeName.UniChar[i] {
			return -1
		}
		if k.NodeName.UniChar[i] > other.NodeName.UniChar[i] {
			return 1
		}
	}
	return 0
}

// BTRecord represents a record in a B-tree node
type BTRecord interface {
	ReadFrom(r io.Reader, keyLength uint16) error
}

// ReadFrom implements BTRecord interface
func (r *ExtentRecord) ReadFrom(reader io.Reader, keyLength uint16) error {
	return binary.Read(reader, binary.BigEndian, r)
}

// Also add AttributeRecord type and implementation:
type AttributeRecord struct {
	Data []byte
}

func (r *AttributeRecord) ReadFrom(reader io.Reader, keyLength uint16) error {
	return binary.Read(reader, binary.BigEndian, &r.Data)
}
