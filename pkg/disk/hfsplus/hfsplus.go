package hfsplus

// reference: https://developer.apple.com/library/archive/technotes/tn/tn1150.html

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

// HFSPlus represents a mounted HFS+ filesystem
type HFSPlus struct {
	volumeHdr VolumeHeader

	device io.ReaderAt
	closer io.Closer // Add this field to track closeable resources

	// Cache commonly accessed structures
	catalogBTree    *BTNode
	extentsBTree    *BTNode
	attributesBTree *BTNode
	startupBTree    *BTNode
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
	if err := binary.Read(io.NewSectionReader(device, 1024, int64(binary.Size(fs.volumeHdr))), binary.BigEndian, &fs.volumeHdr); err != nil {
		return nil, fmt.Errorf("failed to read volume header: %v", err)
	}

	// Verify signature
	if fs.volumeHdr.Signature != HFSPlusSigWord && fs.volumeHdr.Signature != HFSXSigWord {
		return nil, fmt.Errorf("invalid HFS+ signature: %x", fs.volumeHdr.Signature)
	}

	fmt.Println(fs.volumeHdr.String())

	// Initialize B-trees
	if err := fs.initBTrees(); err != nil {
		return nil, err
	}

	return fs, nil
}

// initBTrees initializes the catalog, extents, attributes and startup B-trees
func (fs *HFSPlus) initBTrees() (err error) {
	// Initialize catalog B-tree
	fs.catalogBTree, err = fs.readBTree(fs.volumeHdr.CatalogFile)
	if err != nil {
		return fmt.Errorf("failed to read catalog B-tree: %v", err)
	}

	if fs.volumeHdr.ExtentsFile.LogicalSize > 0 {
		// Initialize extents B-tree
		fs.extentsBTree, err = fs.readBTree(fs.volumeHdr.ExtentsFile)
		if err != nil {
			return fmt.Errorf("failed to read extents B-tree: %v", err)
		}
	}

	if fs.volumeHdr.AttributesFile.LogicalSize > 0 {
		// Initialize attributes B-treeå
		fs.attributesBTree, err = fs.readBTree(fs.volumeHdr.AttributesFile)
		if err != nil {
			return fmt.Errorf("failed to read attributes B-tree: %v", err)
		}
	}

	if fs.volumeHdr.StartupFile.LogicalSize > 0 {
		// Initialize startup B-tree
		fs.startupBTree, err = fs.readBTree(fs.volumeHdr.StartupFile)
		if err != nil {
			return fmt.Errorf("failed to read startup B-tree: %v", err)
		}
	}

	return nil
}

// readBTree reads an HFS+ B-tree from the fork data following TN1150.
func (fs *HFSPlus) readBTree(forkData ForkData) (*BTNode, error) {
	// The B-tree fork begins at the first extent.
	headerOffset := int64(forkData.Extents[0].StartBlock) * int64(fs.volumeHdr.BlockSize)
	// Read header node descriptor.
	var headerNode BTHeaderNode
	sr := io.NewSectionReader(fs.device, headerOffset, 1>>64-1)
	if err := binary.Read(sr, binary.BigEndian, &headerNode.Descriptor); err != nil {
		return nil, fmt.Errorf("failed to read header node descriptor: %v", err)
	}
	if headerNode.Descriptor.Kind != BTHeaderNodeKind {
		return nil, fmt.Errorf("invalid header node kind: %s", headerNode.Descriptor.Kind)
	}
	if err := binary.Read(sr, binary.BigEndian, &headerNode.Header); err != nil {
		return nil, fmt.Errorf("failed to read header record: %v", err)
	}
	if err := binary.Read(sr, binary.BigEndian, &headerNode.UserDataRecord); err != nil {
		return nil, fmt.Errorf("failed to read user data record: %v", err)
	}
	headerNode.Map = make([]uint8, headerNode.Header.NodeSize-256)
	if err := binary.Read(sr, binary.BigEndian, &headerNode.Map); err != nil {
		return nil, fmt.Errorf("failed to read map: %v", err)
	}
	if err := binary.Read(sr, binary.BigEndian, &headerNode.Offsets); err != nil {
		return nil, fmt.Errorf("failed to read offsets: %v", err)
	}
	// Calculate the absolute offset to the root node:
	rootOffset := headerOffset + int64(headerNode.Header.RootNode)*int64(headerNode.Header.NodeSize)
	rootNode, err := fs.readBTreeNodeAtOffset(rootOffset, int(headerNode.Header.NodeSize), forkData)
	if err != nil {
		return nil, fmt.Errorf("failed to read root node: %v", err)
	}
	rootNode.NodeSize = int(headerNode.Header.NodeSize)
	return rootNode, nil
}

// readBTreeNodeAtOffset reads a B-tree node at the given offset with the given node size.
// It follows the HFS+ node layout per TN1150.
func (fs *HFSPlus) readBTreeNodeAtOffset(offset int64, nodeSize int, forkData ForkData) (*BTNode, error) {
	sr := io.NewSectionReader(fs.device, offset, int64(nodeSize))
	node := BTNode{NodeSize: nodeSize}
	if err := binary.Read(sr, binary.BigEndian, &node.Descriptor); err != nil {
		return nil, fmt.Errorf("failed to read node descriptor: %v", err)
	}
	if node.Descriptor.Kind == BTHeaderNodeKind {
		// header nodes do not contain records.
		return &node, nil
	}
	// The record offset array is stored at the end of the node.
	offsetArrayStart := offset + int64(nodeSize) - (2 * int64(node.Descriptor.NumRecords))
	offReader := io.NewSectionReader(fs.device, offsetArrayStart, 2*int64(node.Descriptor.NumRecords))
	offsets := make([]uint16, node.Descriptor.NumRecords)
	if err := binary.Read(offReader, binary.BigEndian, &offsets); err != nil {
		return nil, fmt.Errorf("failed to read record offsets: %v", err)
	}

	node.Records = make([]BTRecord, node.Descriptor.NumRecords)
	for i := uint16(0); i < node.Descriptor.NumRecords; i++ {
		// Offsets are stored in reverse order.
		recordStart := int64(offsets[node.Descriptor.NumRecords-1-i])
		var recordLength int64
		if i == 0 {
			// Length of the first (logical) record.
			recordLength = int64(nodeSize) - recordStart - (2 * int64(node.Descriptor.NumRecords))
		} else {
			recordLength = int64(offsets[node.Descriptor.NumRecords-i] - offsets[node.Descriptor.NumRecords-1-i])
		}
		if recordLength < 0 {
			return nil, fmt.Errorf("invalid record length: %d", recordLength)
		}

		recReader := io.NewSectionReader(fs.device, offset+recordStart, recordLength)
		// First read key length.
		var keyLength uint16
		if err := binary.Read(recReader, binary.BigEndian, &keyLength); err != nil {
			return nil, fmt.Errorf("failed to read key length: %v", err)
		}

		// Create the proper record type based on node kind and fork type.
		var record BTRecord
		switch node.Descriptor.Kind {
		case BTIndexNodeKind:
			record = &CatalogRecord{
				Key: CatalogKey{
					KeyLength: keyLength,
				},
			}
		case BTLeafNodeKind:
			var rtype RecordType
			if err := binary.Read(recReader, binary.BigEndian, &rtype); err != nil {
				return nil, fmt.Errorf("failed to read record type: %v", err)
			}
			switch rtype {
			case HFSPlusFolderRecord:
				record = &FolderRecord{}
			case HFSPlusFileRecord:
				record = &FileRecord{}
			default:
				return nil, fmt.Errorf("unknown B-tree record type for leaf node")
			}
		default:
			return nil, fmt.Errorf("unsupported node kind: %s", node.Descriptor.Kind)
		}
		if err := record.Unmarshal(recReader); err != nil {
			return nil, fmt.Errorf("failed to read record: %v", err)
		}
		node.Records[i] = record
	}
	return &node, nil
}

func (root *BTNode) Files(folderID uint32) ([]*FileRecord, error) {
	var files []*FileRecord
	// for _, record := range root.Records {
	// 	if record.Type() == HFSPlusFileRecord {
	// 		files = append(files, record.(*FileRecord))
	// 	}
	// }
	return files, nil
}
