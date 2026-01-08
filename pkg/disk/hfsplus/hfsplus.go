package hfsplus

// reference: https://developer.apple.com/library/archive/technotes/tn/tn1150.html

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/apex/log"
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

	// Initialize B-trees
	if err := fs.initBTrees(); err != nil {
		return nil, err
	}

	return fs, nil
}

func (fs *HFSPlus) Files() ([]*FileRecord, error) {
	if fs.catalogBTree == nil {
		return nil, fmt.Errorf("catalog B-tree not initialized")
	}

	fmt.Fprintf(os.Stderr, "Starting B-tree traversal (collecting all records)...\n")

	// Collect all records in one pass through the tree
	allFiles := make(map[CatalogNodeID][]*FileRecord)
	allFolders := make(map[CatalogNodeID][]*FolderRecord)

	if err := fs.collectAllRecords(fs.catalogBTree.Root, allFiles, allFolders); err != nil {
		return nil, fmt.Errorf("failed to collect records: %v", err)
	}

	fmt.Fprintf(os.Stderr, "Collected %d file groups and %d folder groups, building file paths...\n", len(allFiles), len(allFolders))

	// Build folder paths
	folderPaths := make(map[CatalogNodeID]string)
	folderPaths[HFSRootFolderID] = ""

	if err := fs.buildFolderPaths(HFSRootFolderID, "", allFolders, folderPaths); err != nil {
		return nil, fmt.Errorf("failed to build folder paths: %v", err)
	}

	// Build final file list with paths
	var files []*FileRecord
	if err := fs.buildFileList(HFSRootFolderID, allFiles, allFolders, folderPaths, &files); err != nil {
		return nil, fmt.Errorf("failed to build file list: %v", err)
	}

	fmt.Fprintf(os.Stderr, "B-tree traversal complete, found %d files\n", len(files))

	return files, nil
}

// collectAllRecords traverses the B-tree once and collects all file and folder records
func (fs *HFSPlus) collectAllRecords(node *BTNode, allFiles map[CatalogNodeID][]*FileRecord, allFolders map[CatalogNodeID][]*FolderRecord) error {
	switch node.Descriptor.Kind {
	case BTIndexNodeKind:
		// For index nodes, recursively traverse all child nodes
		for _, record := range node.Records {
			if catalogRecord, ok := record.(*CatalogRecord); ok {
				// Read child node
				childOffset := fs.getNodeOffset(catalogRecord.Link, int(fs.catalogBTree.BTHeaderNode.Header.NodeSize))
				childNode, err := fs.readBTreeNodeAtOffset(childOffset, int(fs.catalogBTree.BTHeaderNode.Header.NodeSize), fs.volumeHdr.CatalogFile)
				if err != nil {
					return fmt.Errorf("failed to read child node: %v", err)
				}
				// Recursively collect from child
				if err := fs.collectAllRecords(childNode, allFiles, allFolders); err != nil {
					return err
				}
			}
		}

	case BTLeafNodeKind:
		// For leaf nodes, collect all file and folder records
		for _, record := range node.Records {
			switch r := record.(type) {
			case *FileRecord:
				parentID := r.Key.ParentID
				allFiles[parentID] = append(allFiles[parentID], r)
			case *FolderRecord:
				parentID := r.Key.ParentID
				allFolders[parentID] = append(allFolders[parentID], r)
			}
		}
	}

	return nil
}

// buildFolderPaths recursively builds full paths for all folders
func (fs *HFSPlus) buildFolderPaths(folderID CatalogNodeID, currentPath string, allFolders map[CatalogNodeID][]*FolderRecord, folderPaths map[CatalogNodeID]string) error {
	folders := allFolders[folderID]
	for _, folder := range folders {
		folderName := folder.Key.NodeName.String()
		var newPath string
		if currentPath == "" {
			newPath = "/" + folderName
		} else {
			newPath = filepath.Join(currentPath, folderName)
		}
		subFolderID := folder.FolderInfo.FolderID
		folderPaths[subFolderID] = newPath

		// Recursively build paths for subfolders
		if err := fs.buildFolderPaths(subFolderID, newPath, allFolders, folderPaths); err != nil {
			return err
		}
	}
	return nil
}

// buildFileList recursively builds the final file list with full paths
func (fs *HFSPlus) buildFileList(folderID CatalogNodeID, allFiles map[CatalogNodeID][]*FileRecord, allFolders map[CatalogNodeID][]*FolderRecord, folderPaths map[CatalogNodeID]string, files *[]*FileRecord) error {
	currentPath := folderPaths[folderID]

	// Add all files in this folder
	for _, file := range allFiles[folderID] {
		fileName := file.Key.NodeName.String()
		var filePath string
		if currentPath == "" {
			filePath = "/" + fileName
		} else {
			filePath = filepath.Join(currentPath, fileName)
		}
		file.path = filePath
		file.r = &fs.device
		file.blkSize = fs.volumeHdr.BlockSize
		*files = append(*files, file)
	}

	// Recursively process subfolders
	for _, folder := range allFolders[folderID] {
		subFolderID := folder.FolderInfo.FolderID
		if err := fs.buildFileList(subFolderID, allFiles, allFolders, folderPaths, files); err != nil {
			return err
		}
	}

	return nil
}

func (fs *HFSPlus) listFilesInNodeWithVisited(node *BTNode, folderID CatalogNodeID, currentPath string, files *[]*FileRecord, visited map[CatalogNodeID]bool, callCount *int) error {
	// Prevent infinite recursion by tracking visited folders
	if visited[folderID] {
		return nil
	}
	visited[folderID] = true

	// Limit total files to prevent hanging on corrupted filesystems
	if len(*files) > 100000 {
		return fmt.Errorf("file limit exceeded (possible filesystem corruption)")
	}

	return fs.listFilesInNodeInternal(node, folderID, currentPath, files, visited, callCount)
}

func (fs *HFSPlus) listFilesInNode(node *BTNode, folderID CatalogNodeID, currentPath string, files *[]*FileRecord) error {
	// This is kept for backward compatibility but should use visited tracking
	visited := make(map[CatalogNodeID]bool)
	callCount := 0
	return fs.listFilesInNodeInternal(node, folderID, currentPath, files, visited, &callCount)
}

func (fs *HFSPlus) listFilesInNodeInternal(node *BTNode, folderID CatalogNodeID, currentPath string, files *[]*FileRecord, visited map[CatalogNodeID]bool, callCount *int) error {
	// Increment call counter and check for infinite loops
	*callCount++

	// Detect infinite loops - if we've made too many calls, bail out
	if *callCount > 100000 {
		return fmt.Errorf("call count exceeded (possible infinite loop), made %d calls", *callCount)
	}

	// Log progress every 1000 calls
	if *callCount%1000 == 0 {
		fmt.Fprintf(os.Stderr, "  Made %d calls, found %d files, visited %d folders...\n", *callCount, len(*files), len(visited))
	}

	// Progress logging every 500 files
	if len(*files)%500 == 0 && len(*files) > 0 {
		fmt.Fprintf(os.Stderr, "  Listed %d files so far...\n", len(*files))
	}

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
					if err := fs.listFilesInNodeInternal(childNode, folderID, currentPath, files, visited, callCount); err != nil {
						return err
					}
				}
			}
		}

	case BTLeafNodeKind:
		// For leaf nodes, process file and folder records for the current folder.
		for _, record := range node.Records {
			switch r := record.(type) {
			case *FileRecord:
				if r.Key.ParentID == folderID {
					fileName := r.Key.NodeName.String()
					var filePath string
					if currentPath == "" {
						filePath = "/" + fileName
					} else {
						filePath = filepath.Join(currentPath, fileName)
					}
					r.path = filePath
					r.r = &fs.device
					r.blkSize = fs.volumeHdr.BlockSize
					*files = append(*files, r)
				}
			case *FolderRecord:
				if r.Key.ParentID == folderID {
					folderName := r.Key.NodeName.String()
					var newPath string
					if currentPath == "" {
						newPath = "/" + folderName
					} else {
						newPath = filepath.Join(currentPath, folderName)
					}
					// Recursively process the subfolder's own files.
					subFolderID := r.FolderInfo.FolderID

					// Recursively process subfolder through the wrapper to properly track visits and calls
					if err := fs.listFilesInNodeWithVisited(fs.catalogBTree.Root, subFolderID, newPath, files, visited, callCount); err != nil {
						return err
					}
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
		// Initialize attributes B-tree (optional, used for extended attributes)
		fs.attributesBTree, err = fs.readBTree(fs.volumeHdr.AttributesFile)
		if err != nil {
			// Attributes B-tree is optional - log warning but continue
			log.Warnf("failed to read attributes B-tree (continuing without extended attributes): %v", err)
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
			// NOTE: Avoid log.Debugf here as it allocates even when disabled
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
