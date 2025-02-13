package hfsplus

// reference: https://developer.apple.com/library/archive/technotes/tn/tn1150.html

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"slices"
)

// HFSPlus represents a mounted HFS+ filesystem
type HFSPlus struct {
	volumeHdr VolumeHeader

	device io.ReaderAt
	closer io.Closer // Add this field to track closeable resources

	// Cache commonly accessed structures
	catalogBTree    *BTree
	extentsBTree    *BTree
	attributesBTree *BTree
	startupBTree    *BTree
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

func (fs *HFSPlus) Files() ([]*FileRecord, error) {
	var files []*FileRecord

	// Start from catalog B-tree root
	if fs.catalogBTree == nil {
		return nil, fmt.Errorf("catalog B-tree not initialized")
	}

	// Recursively traverse the B-tree
	if err := fs.listFilesInNode(fs.catalogBTree.Root, HFSRootFolderID, &files); err != nil {
		return nil, fmt.Errorf("failed to list files: %v", err)
	}

	return files, nil
}

func (fs *HFSPlus) listFilesInNode(node *BTNode, folderID CatalogNodeID, files *[]*FileRecord) error {
	// Handle different node types
	switch node.Descriptor.Kind {
	case BTIndexNodeKind:
		// For index nodes, recursively traverse child nodes that could contain our folder
		for _, record := range node.Records {
			if catalogRecord, ok := record.(*CatalogRecord); ok {
				// Check if this branch could contain our folder
				if catalogRecord.Key.ParentID <= folderID {
					// Get child node offset
					childOffset := fs.getNodeOffset(catalogRecord.Link, int(fs.catalogBTree.BTHeaderNode.Header.NodeSize))

					// Read child node
					childNode, err := fs.readBTreeNodeAtOffset(childOffset, int(fs.catalogBTree.BTHeaderNode.Header.NodeSize), fs.volumeHdr.CatalogFile)
					if err != nil {
						return fmt.Errorf("failed to read child node: %v", err)
					}

					// Recursively process child node
					if err := fs.listFilesInNode(childNode, folderID, files); err != nil {
						return err
					}
				}
			}
		}

	case BTLeafNodeKind:
		// For leaf nodes, collect file records that match our folder ID
		for _, record := range node.Records {
			switch r := record.(type) {
			case *FileRecord:
				if r.Key.ParentID == folderID {
					r.r = &fs.device
					r.blkSize = fs.volumeHdr.BlockSize
					*files = append(*files, r)
				}
			}
		}
	}

	return nil
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
func (fs *HFSPlus) readBTree(forkData ForkData) (btree *BTree, err error) {
	// The B-tree fork begins at the first extent.
	headerOffset := int64(forkData.Extents[0].StartBlock) * int64(fs.volumeHdr.BlockSize)

	btree = &BTree{}
	sr := io.NewSectionReader(fs.device, headerOffset, 1>>64-1)
	if err := binary.Read(sr, binary.BigEndian, &btree.BTHeaderNode.Descriptor); err != nil {
		return nil, fmt.Errorf("failed to read header node descriptor: %v", err)
	}
	if btree.BTHeaderNode.Descriptor.Kind != BTHeaderNodeKind {
		return nil, fmt.Errorf("invalid header node kind: %s", btree.BTHeaderNode.Descriptor.Kind)
	}
	if err := binary.Read(sr, binary.BigEndian, &btree.BTHeaderNode.Header); err != nil {
		return nil, fmt.Errorf("failed to read header record: %v", err)
	}
	if err := binary.Read(sr, binary.BigEndian, &btree.BTHeaderNode.UserDataRecord); err != nil {
		return nil, fmt.Errorf("failed to read user data record: %v", err)
	}
	btree.BTHeaderNode.Map = make([]uint8, btree.BTHeaderNode.Header.NodeSize-256)
	if err := binary.Read(sr, binary.BigEndian, &btree.BTHeaderNode.Map); err != nil {
		return nil, fmt.Errorf("failed to read map: %v", err)
	}
	if err := binary.Read(sr, binary.BigEndian, &btree.BTHeaderNode.Offsets); err != nil {
		return nil, fmt.Errorf("failed to read offsets: %v", err)
	}

	// Calculate the absolute offset to the root node:
	rootOffset := headerOffset + int64(btree.BTHeaderNode.Header.RootNode)*int64(btree.BTHeaderNode.Header.NodeSize)
	btree.Root, err = fs.readBTreeNodeAtOffset(rootOffset, int(btree.BTHeaderNode.Header.NodeSize), forkData)
	if err != nil {
		return nil, fmt.Errorf("failed to read root node: %v", err)
	}

	return btree, nil
}

func (fs *HFSPlus) getNodeOffset(link uint32, nodeSize int) int64 {
	localBlock := int64(nodeSize) * int64(link) / int64(fs.volumeHdr.BlockSize)
	driveBlock := int64(0) // Find the extent containing this block
	for _, extent := range fs.volumeHdr.CatalogFile.Extents {
		if localBlock < int64(extent.BlockCount) {
			driveBlock = int64(extent.StartBlock) + localBlock
			break
		}
		localBlock -= int64(extent.BlockCount)
	}
	return driveBlock * int64(fs.volumeHdr.BlockSize)
}

// readBTreeNodeAtOffset reads a B-tree node at the given offset with the given node size.
func (fs *HFSPlus) readBTreeNodeAtOffset(offset int64, nodeSize int, forkData ForkData) (node *BTNode, err error) {
	sr := io.NewSectionReader(fs.device, offset, int64(nodeSize))

	node = &BTNode{NodeSize: nodeSize}
	if err := binary.Read(sr, binary.BigEndian, &node.Descriptor); err != nil {
		return nil, fmt.Errorf("failed to read node descriptor: %v", err)
	}

	if node.Descriptor.Kind == BTHeaderNodeKind {
		return node, nil // header nodes do not contain records.
	}

	deltas := make([]uint16, node.Descriptor.NumRecords)
	if err := binary.Read(
		io.NewSectionReader(
			fs.device,
			// The record offset array is stored at the end of the node.
			offset+int64(nodeSize)-(2*int64(node.Descriptor.NumRecords)),
			2*int64(node.Descriptor.NumRecords),
		),
		binary.BigEndian,
		&deltas,
	); err != nil {
		return nil, fmt.Errorf("failed to read record offsets: %v", err)
	}
	slices.Reverse(deltas) // offset deltas are stored in reverse order.

	offsets := make([]int64, node.Descriptor.NumRecords)
	for i, detla := range deltas {
		offsets[i] = offset + int64(detla)
		// fmt.Printf("record offset: %x\n", offsets[i])
	}

	for _, offset := range offsets {
		var record BTRecord
		switch node.Descriptor.Kind {
		case BTIndexNodeKind:
			record = &CatalogRecord{}
			if err := record.Unmarshal(sr); err != nil {
				return nil, fmt.Errorf("failed to read record: %v", err)
			}
			childOffset := fs.getNodeOffset(record.(*CatalogRecord).Link, nodeSize)
			child, err := fs.readBTreeNodeAtOffset(childOffset, nodeSize, forkData)
			if err != nil {
				return nil, fmt.Errorf("failed to read child node: %v", err)
			}
			record.(*CatalogRecord).Child = *child
		case BTLeafNodeKind:
			record, err = fs.readBTRecordAt(offset)
			if err != nil {
				return nil, fmt.Errorf("failed to read record: %v", err)
			}
		default:
			return nil, fmt.Errorf("unsupported node kind: %s", node.Descriptor.Kind)
		}
		node.Records = append(node.Records, record)
	}

	return node, nil
}

func (fs *HFSPlus) readBTRecordAt(offset int64) (record BTRecord, err error) {
	sr := io.NewSectionReader(fs.device, offset, 1<<63-1)

	var keyLength uint16
	if err := binary.Read(sr, binary.BigEndian, &keyLength); err != nil {
		return nil, fmt.Errorf("failed to read key length: %v", err)
	}

	sr.Seek(int64(keyLength), io.SeekCurrent) // skip past key

	var recordType RecordType
	if err := binary.Read(sr, binary.BigEndian, &recordType); err != nil {
		return nil, fmt.Errorf("failed to read record type: %v", err)
	}

	sr.Seek(-int64(keyLength+uint16(binary.Size(keyLength)))-int64(binary.Size(recordType)), io.SeekCurrent) // rewind to start of record

	switch recordType {
	case HFSPlusFolderRecord:
		record = &FolderRecord{}
		if err := record.Unmarshal(sr); err != nil {
			return nil, fmt.Errorf("failed to read record: %v", err)
		}
	case HFSPlusFileRecord:
		record = &FileRecord{}
		if err := record.Unmarshal(sr); err != nil {
			return nil, fmt.Errorf("failed to read record: %v", err)
		}
	case HFSPlusFolderThreadRecord, HFSPlusFileThreadRecord:
		record = &ThreadRecord{}
		if err := record.Unmarshal(sr); err != nil {
			return nil, fmt.Errorf("failed to read record: %v", err)
		}
	default:
		return nil, fmt.Errorf("unsupported record type: %s", recordType)
	}

	return record, nil
}
