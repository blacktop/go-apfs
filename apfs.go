package apfs

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/apex/log"
	"github.com/blacktop/go-apfs/pkg/disk"
	"github.com/blacktop/go-apfs/pkg/disk/dmg"
	"github.com/blacktop/go-apfs/pkg/disk/raw"
	"github.com/blacktop/go-apfs/types"
)

// APFS apple file system object
type APFS struct {
	Container   *types.NxSuperblock
	Valid       *types.NxSuperblock
	Volume      *types.ApfsSuperblock
	FSRootBtree types.BTreeNodePhys

	nxsb           *types.Obj // Container
	checkPointDesc []*types.Obj
	volume         *types.Obj
	fsOMapBtree    *types.BTreeNodePhys

	dev    disk.Device
	r      io.ReaderAt
	closer io.Closer
}

// Open opens the named file using os.Open and prepares it for use as an APFS.
func Open(name string) (*APFS, error) {
	var ff *APFS

	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}

	fsType, err := detectFilesystem(f)
	if err != nil {
		return nil, err
	}

	switch fsType {
	case DMG:
		dev, err := dmg.NewDMG(f, &dmg.Config{DisableCache: true})
		if err != nil {
			return nil, fmt.Errorf("failed to open DMG: %v", err)
		}
		if err := dev.Load(); err != nil {
			return nil, fmt.Errorf("failed to load DMG: %v", err)
		}
		ff, err = NewAPFS(dev)
		if err != nil {
			f.Close()
			return nil, err
		}
		ff.closer = f
	case APFS_RAW:
		ra, err := raw.NewRaw(f)
		if err != nil {
			return nil, fmt.Errorf("failed to open APFS raw image: %v", err)
		}
		ff, err = NewAPFS(ra)
		if err != nil {
			f.Close()
			return nil, err
		}
	case HFS:
		return nil, fmt.Errorf("HFS+ is not supported")
	default:
		return nil, fmt.Errorf("unknown filesystem type")
	}

	ff.closer = f
	return ff, nil
}

// Close closes the APFS.
// If the APFS was created using NewFile directly instead of Open,
// Close has no effect.
func (a *APFS) Close() error {
	var err error
	if a.closer != nil {
		err = a.closer.Close()
		a.closer = nil
	}
	return err
}

func init() {
	types.BLOCK_SIZE = types.NX_DEFAULT_BLOCK_SIZE
}

// NewAPFS creates a new APFS for accessing a apple filesystem container or file in an underlying reader.
// The apfs is expected to start at position 0 in the ReaderAt.
func NewAPFS(dev disk.Device) (*APFS, error) {

	var err error

	a := new(APFS)
	a.dev = dev
	a.r = dev

	a.nxsb, err = types.ReadObj(a.r, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to read APFS container: %w", err)
	}

	if nxsb, ok := a.nxsb.Body.(types.NxSuperblock); ok {
		a.Container = &nxsb
	}

	if a.Container.BlockSize != types.NX_DEFAULT_BLOCK_SIZE {
		types.BLOCK_SIZE = uint64(a.Container.BlockSize)
		log.Warnf("found non-standard blocksize in APFS nx_superblock_t: %#x", types.BLOCK_SIZE)
	}

	log.WithFields(log.Fields{
		"checksum": fmt.Sprintf("%#x", a.nxsb.Hdr.Checksum()),
		"oid":      fmt.Sprintf("%#x", a.nxsb.Hdr.Oid),
		"xid":      fmt.Sprintf("%#x", a.nxsb.Hdr.Xid),
		"type":     a.nxsb.Hdr.GetType(),
		"sub_type": a.nxsb.Hdr.GetSubType(),
		"flag":     a.nxsb.Hdr.GetFlag(),
		"magic":    a.Container.Magic.String(),
	}).Debug("APFS Container")

	log.WithFields(log.Fields{
		"checksum": fmt.Sprintf("%#x", a.Container.OMap.Hdr.Checksum()),
		"type":     a.Container.OMap.Hdr.GetType(),
		"oid":      fmt.Sprintf("%#x", a.Container.OMap.Hdr.Oid),
		"xid":      fmt.Sprintf("%#x", a.Container.OMap.Hdr.Xid),
		"sub_type": a.Container.OMap.Hdr.GetSubType(),
		"flag":     a.Container.OMap.Hdr.GetFlag(),
	}).Debug("Object Map")

	log.Debug("Parsing Checkpoint Description")
	if err := a.getValidCSB(); err != nil {
		return nil, fmt.Errorf("failed to find the container superblock that has the largest transaction identifier and isnʼt malformed: %v", err)
	}

	if len(a.Valid.OMap.Body.(types.OMap).Tree.Body.(types.BTreeNodePhys).Entries) == 1 { // TODO: could be more than 1 for non IPSW APFS volumes?
		if entry, ok := a.Valid.OMap.Body.(types.OMap).Tree.Body.(types.BTreeNodePhys).Entries[0].(types.OMapNodeEntry); ok {
			a.volume, err = types.ReadObj(a.r, uint64(entry.Val.Paddr))
			if err != nil {
				return nil, fmt.Errorf("failed to read APFS omap.tree.entry.omap (volume): %v", err)
			}
			if vol, ok := a.volume.Body.(types.ApfsSuperblock); ok {
				log.WithFields(log.Fields{
					"checksum": fmt.Sprintf("%#x", a.volume.Hdr.Checksum()),
					"type":     a.volume.Hdr.GetType(),
					"oid":      fmt.Sprintf("%#x", a.volume.Hdr.Oid),
					"xid":      fmt.Sprintf("%#x", a.volume.Hdr.Xid),
					"sub_type": a.volume.Hdr.GetSubType(),
					"flag":     a.volume.Hdr.GetFlag(),
				}).Debug(fmt.Sprintf("APFS Volume (%s)", string(vol.VolumeName[:])))
				a.Volume = &vol
			}
		} else {
			return nil, fmt.Errorf("failed to parse omap.tree.entry.omap (volume)")
		}
	}

	log.Debugf("File System OMap Btree: %s", a.Volume.OMap.Body.(types.OMap).Tree)

	if ombtree, ok := a.Volume.OMap.Body.(types.OMap).Tree.Body.(types.BTreeNodePhys); ok {
		a.fsOMapBtree = &ombtree
	} else {
		return nil, fmt.Errorf("failed to parse omap.tree.body (btree)")
	}

	fsRootEntry, err := a.fsOMapBtree.GetOMapEntry(a.r, a.Volume.RootTreeOid, a.volume.Hdr.Xid)
	if err != nil {
		return nil, fmt.Errorf("failed to get root entry: %v", err)
	}

	log.Debugf("File System Root Entry: %s", fsRootEntry)

	// if fsRootEntry.Val.Flags&types.OMAP_VAL_NOHEADER == 0 {
	fsRootBtreeObj, err := types.ReadObj(a.r, fsRootEntry.Val.Paddr)
	if err != nil {
		return nil, fmt.Errorf("failed to read root btree: %v", err)
	}
	if root, ok := fsRootBtreeObj.Body.(types.BTreeNodePhys); ok {
		a.FSRootBtree = root
	} else {
		return nil, fmt.Errorf("failed to parse root btree")
	}
	// } else {
	// 	a.FSRootBtree = *a.fsOMapBtree // FIXME: this is prob where the bug is
	// }
	return a, nil
}

// getValidCSB returns the container superblock that has the largest transaction identifier and isnʼt malformed
func (a *APFS) getValidCSB() error {

	if (a.Container.XpDescBlocks >> 31) != 0 {
		return fmt.Errorf("unable to parse non-contiguous checkpoint descriptor area")
	}

	for i := uint32(0); i < a.Container.XpDescBlocks; i++ {
		o, err := types.ReadObj(a.r, a.Container.XpDescBase+uint64(i))
		if err != nil {
			if errors.Is(err, types.ErrBadBlockChecksum) {
				log.Debug(fmt.Sprintf("checkpoint block at index %d failed checksum validation. Skipping...", i))
				continue
			}
			return fmt.Errorf("failed to read XpDescBlock: %v", err)
		}
		a.checkPointDesc = append(a.checkPointDesc, o)
	}

	sort.Slice(a.checkPointDesc[:], func(i, j int) bool {
		return a.checkPointDesc[i].Hdr.Xid < a.checkPointDesc[j].Hdr.Xid
	})

	for i := len(a.checkPointDesc) - 1; i >= 0; i-- {
		if a.checkPointDesc[i].Hdr.GetType() == types.OBJECT_TYPE_NX_SUPERBLOCK {
			if nxsb, ok := a.checkPointDesc[i].Body.(types.NxSuperblock); ok {
				a.Valid = &nxsb
				break
			} else {
				return fmt.Errorf("invalid checkpoint descriptor")
			}
		}
	}

	return nil
}

func (a *APFS) OidInfo(oid uint64) error {

	sr := io.NewSectionReader(a.r, 0, 1<<63-1)

	fsRecords, err := a.fsOMapBtree.GetFSRecordsForOid(sr, a.FSRootBtree, types.OidT(oid), types.XidT(^uint64(0)))
	if err != nil {
		return fmt.Errorf("failed to get FS records for oid %#x: %v", types.OidT(oid), err)
	}

	for _, rec := range fsRecords {
		switch rec.Hdr.GetType() {
		case types.APFS_TYPE_XATTR:
			switch rec.Key.(types.JXattrKeyT).Name {
			case types.XATTR_RESOURCEFORK_EA_NAME:
				if rec.Val.(types.JXattrValT).Flags.DataEmbedded() {
					fmt.Println(rec)
				} else if rec.Val.(types.JXattrValT).Flags.DataStream() {
					fmt.Println(rec)
					fsRecords, err = a.fsOMapBtree.GetFSRecordsForOid(sr, a.FSRootBtree, types.OidT(rec.Val.(types.JXattrValT).Data.(types.JXattrDstreamT).XattrObjID), types.XidT(^uint64(0)))
					if err != nil {
						return fmt.Errorf("failed to get fs records for dstream oid %#x: %v", types.OidT(rec.Val.(types.JXattrValT).Data.(types.JXattrDstreamT).XattrObjID), err)
					}
					for _, rec := range fsRecords {
						fmt.Printf("\t%s\n", rec)
					}
				}
			case types.XATTR_SYMLINK_EA_NAME:
				fmt.Println(rec)
				fmt.Printf("\tsymlink=%s\n", types.NameColor(string(rec.Val.(types.JXattrValT).Data.([]byte)[:])))
			}
		case types.APFS_TYPE_DIR_REC:
			fmt.Printf("\t%s\n", rec)
		default:
			fmt.Println(rec)
		}
	}

	return nil
}

// Cat prints a file at a given path to stdout
func (a *APFS) Cat(path string) error {
	var fsRecords types.FSRecords
	var decmpfsHdr *types.DecmpfsDiskHeader

	sr := io.NewSectionReader(a.r, 0, 1<<63-1)

	files, err := a.find(path)
	if err != nil {
		return fmt.Errorf("failed to find %s: %v", path, err)
	}

	for _, rec := range files {

		fsRecords, err = a.fsOMapBtree.GetFSRecordsForOid(sr, a.FSRootBtree, types.OidT(rec.Val.(types.JDrecVal).FileID), types.XidT(^uint64(0)))
		if err != nil {
			return fmt.Errorf("failed to get fs records for %s: %v", path, err)
		}

		var symlink string
		var fileName string
		// var uncompressedSize uint64
		var totalBytesWritten uint64
		var fexts []types.FileExtent

		compressed := false

		for _, rec := range fsRecords {
			switch rec.Hdr.GetType() {
			case types.APFS_TYPE_INODE:
				if rec.Val.(types.JInodeVal).InternalFlags&types.INODE_HAS_UNCOMPRESSED_SIZE != 0 {
					compressed = true
					// uncompressedSize = rec.Val.(types.JInodeVal).UncompressedSize
				}
				for _, xf := range rec.Val.(types.JInodeVal).Xfields {
					switch xf.XType {
					case types.INO_EXT_TYPE_NAME:
						fileName = xf.Field.(string)
					case types.INO_EXT_TYPE_DSTREAM:
						totalBytesWritten = xf.Field.(types.JDstreamT).TotalBytesWritten
					}
				}
			case types.APFS_TYPE_FILE_EXTENT:
				fexts = append(fexts, types.FileExtent{
					Address: rec.Key.(types.JFileExtentKeyT).LogicalAddr,
					Block:   rec.Val.(types.JFileExtentValT).PhysBlockNum,
					Length:  rec.Val.(types.JFileExtentValT).Length(),
				})
			case types.APFS_TYPE_XATTR:
				switch rec.Key.(types.JXattrKeyT).Name {
				case types.XATTR_RESOURCEFORK_EA_NAME:
					if rec.Val.(types.JXattrValT).Flags.DataEmbedded() {
						binary.Read(bytes.NewReader(rec.Val.(types.JXattrValT).Data.([]byte)), binary.LittleEndian, &decmpfsHdr)
					} else if rec.Val.(types.JXattrValT).Flags.DataStream() {
						fsRecords, err = a.fsOMapBtree.GetFSRecordsForOid(
							sr,
							a.FSRootBtree,
							types.OidT(rec.Val.(types.JXattrValT).Data.(types.JXattrDstreamT).XattrObjID),
							types.XidT(^uint64(0)))
						if err != nil {
							return fmt.Errorf("failed to get fs records for oid %#x: %v", types.OidT(rec.Val.(types.JXattrValT).Data.(uint64)), err)
						}
						for _, rec := range fsRecords {
							switch rec.Hdr.GetType() {
							case types.APFS_TYPE_FILE_EXTENT:
								fexts = append(fexts, types.FileExtent{
									Address: rec.Key.(types.JFileExtentKeyT).LogicalAddr,
									Block:   rec.Val.(types.JFileExtentValT).PhysBlockNum,
									Length:  rec.Val.(types.JFileExtentValT).Length(),
								})
							}
						}
					}
				case types.XATTR_DECMPFS_EA_NAME:
					decmpfsHdr, err = types.GetDecmpfsHeader(rec)
					if err != nil {
						return fmt.Errorf("failed to get decmpfs header: %v", err)
					}
				case types.XATTR_SYMLINK_EA_NAME:
					symlink = string(rec.Val.(types.JXattrValT).Data.([]byte)[:])
					fmt.Println(symlink)
				}
			}
		}

		if compressed {
			w := bufio.NewWriter(os.Stdout)
			if err := decmpfsHdr.DecompressFile(a.r, w, fexts, true); err != nil {
				return fmt.Errorf("failed to decompress %s: %v", fileName, err)
			}
			w.Flush()
		} else {
			for _, fext := range fexts {
				if err := a.dev.ReadFile(bufio.NewWriter(os.Stdout), int64(fext.Block*types.BLOCK_SIZE), int64(totalBytesWritten)); err != nil {
					return fmt.Errorf("failed to write file data from device: %v", err)
				}
			}
		}
	}

	return nil
}

// List lists files at a given path
func (a *APFS) List(path string) error {

	sr := io.NewSectionReader(a.r, 0, 1<<63-1)

	fsRecords, err := a.fsOMapBtree.GetFSRecordsForOid(sr, a.FSRootBtree, types.OidT(types.FSROOT_OID), types.XidT(^uint64(0)))
	if err != nil {
		return fmt.Errorf("failed to get FS records for FSROOT_OID: %v", err)
	}

	parts := strings.FieldsFunc(path, func(c rune) bool {
		return c == filepath.Separator
	})

	for idx, part := range parts {
		if len(part) > 0 {
			for _, rec := range fsRecords {
				switch rec.Hdr.GetType() {
				case types.APFS_TYPE_DIR_REC:
					if rec.Key.(types.JDrecHashedKeyT).Name == part {
						fsRecords, err = a.fsOMapBtree.GetFSRecordsForOid(sr, a.FSRootBtree, types.OidT(rec.Val.(types.JDrecVal).FileID), types.XidT(^uint64(0)))
						if err != nil {
							return fmt.Errorf("failed to get FS records for oid %#x: %v", types.OidT(rec.Val.(types.JDrecVal).FileID), err)
						}
						if idx == len(parts)-1 { // last part
							switch rec.Val.(types.JDrecVal).Flags {
							case types.DT_REG:
								var rFile types.RegFile
								for _, regRec := range fsRecords {
									switch regRec.Hdr.GetType() {
									case types.APFS_TYPE_INODE:
										rFile.Owner = regRec.Val.(types.JInodeVal).Owner
										rFile.Group = regRec.Val.(types.JInodeVal).Group
										rFile.Mode = regRec.Val.(types.JInodeVal).Mode
										rFile.CreateTime = regRec.Val.(types.JInodeVal).CreateTime

										for _, xf := range regRec.Val.(types.JInodeVal).Xfields {
											switch xf.XType {
											case types.INO_EXT_TYPE_NAME:
												rFile.Name = xf.Field.(string)
											case types.INO_EXT_TYPE_DSTREAM:
												rFile.Size = xf.Field.(types.JDstreamT).Size
											}
										}

										if regRec.Val.(types.JInodeVal).InternalFlags&types.INODE_HAS_UNCOMPRESSED_SIZE != 0 {
											rFile.Size = regRec.Val.(types.JInodeVal).UncompressedSize
										}
									}
								}
								fmt.Println(rFile)
								return nil
							case types.DT_LNK:
								var rFile types.RegFile
								for _, lnkRec := range fsRecords {
									switch lnkRec.Hdr.GetType() {
									case types.APFS_TYPE_INODE:
										for _, regRec := range fsRecords {
											switch regRec.Hdr.GetType() {
											case types.APFS_TYPE_INODE:
												rFile.Owner = regRec.Val.(types.JInodeVal).Owner
												rFile.Group = regRec.Val.(types.JInodeVal).Group
												rFile.Mode = regRec.Val.(types.JInodeVal).Mode
												rFile.CreateTime = regRec.Val.(types.JInodeVal).CreateTime

												for _, xf := range regRec.Val.(types.JInodeVal).Xfields {
													switch xf.XType {
													case types.INO_EXT_TYPE_NAME:
														rFile.Name = types.DirColor(xf.Field.(string)) // TODO: should this always be dir colored?
													case types.INO_EXT_TYPE_DSTREAM:
														rFile.Size = xf.Field.(types.JDstreamT).Size
													}
												}

												if regRec.Val.(types.JInodeVal).InternalFlags&types.INODE_HAS_UNCOMPRESSED_SIZE != 0 {
													rFile.Size = regRec.Val.(types.JInodeVal).UncompressedSize
												}
											}
										}
									case types.APFS_TYPE_XATTR:
										switch lnkRec.Key.(types.JXattrKeyT).Name {
										case types.XATTR_SYMLINK_EA_NAME:
											fmt.Printf("%s -> %s\n", rFile, string(lnkRec.Val.(types.JXattrValT).Data.([]byte)[:]))
										}
									}
								}
							}
						}
					}
					// default:
					// 	log.Warnf("found fs record type %s", rec.Hdr.GetType())
				}
			}
		}
	}

	fmt.Print(fsRecords)

	return nil
}

// Tree list contents of directories in a tree-like format. TODO: finish this
func (a *APFS) Tree(path string) error {

	sr := io.NewSectionReader(a.r, 0, 1<<63-1)

	fsOMapBtree := a.Volume.OMap.Body.(types.OMap).Tree.Body.(types.BTreeNodePhys)

	fsRootEntry, err := fsOMapBtree.GetOMapEntry(sr, a.Volume.RootTreeOid, a.volume.Hdr.Xid)
	if err != nil {
		return err
	}

	fsRootBtreeObj, err := types.ReadObj(sr, fsRootEntry.Val.Paddr)
	if err != nil {
		return err
	}

	fsRootBtree := fsRootBtreeObj.Body.(types.BTreeNodePhys)

	fsRecords, err := fsOMapBtree.GetFSRecordsForOid(sr, fsRootBtree, types.OidT(types.FSROOT_OID), types.XidT(^uint64(0)))
	if err != nil {
		return err
	}

	fstree := types.NewFSTree("/")

	for _, part := range strings.Split(path, string(filepath.Separator)) {
		if len(part) > 0 {
			for _, rec := range fsRecords {
				switch rec.Hdr.GetType() {
				case types.APFS_TYPE_DIR_REC:
					if rec.Key.(types.JDrecHashedKeyT).Name == part {
						fsRecords, err = fsOMapBtree.GetFSRecordsForOid(sr, fsRootBtree, types.OidT(rec.Val.(types.JDrecVal).FileID), types.XidT(^uint64(0)))
						if err != nil {
							return err
						}
						fstree.AddTree(fsRecords.Tree())
					} else {
						fstree.Add(rec.Key.(types.JDrecHashedKeyT).Name)
					}
				// case types.APFS_TYPE_INODE:
				// 	for _, xf := range rec.Val.(j_inode_val).Xfields {
				// 		if xf.XType == INO_EXT_TYPE_NAME {
				// 			if xf.Field.(string) == "root" {
				// 				t = NewFSTree("/")
				// 			} else {
				// 				t = NewFSTree(xf.Field.(string))
				// 				}
				// 			}
				// 		}
				// 	}
				default:
					log.Warnf("found fs record type %s", rec.Hdr.GetType())
				}
			}
		}
	}

	fmt.Println(fstree.Print())

	return nil
}

// Copy copies the contents of the src file to the dest file TODO: finish this
func (a *APFS) Copy(src, dest string) (err error) {

	var fsRecords types.FSRecords
	var decmpfsHdr *types.DecmpfsDiskHeader

	sr := io.NewSectionReader(a.r, 0, 1<<63-1)

	files, err := a.find(src)
	if err != nil {
		return fmt.Errorf("failed to find %s: %v", src, err)
	}

	for _, rec := range files {

		fsRecords, err = a.fsOMapBtree.GetFSRecordsForOid(sr, a.FSRootBtree, types.OidT(rec.Val.(types.JDrecVal).FileID), types.XidT(^uint64(0)))
		if err != nil {
			return fmt.Errorf("failed to get fs records for %s: %v", src, err)
		}

		var symlink string
		var fileName string
		var uncompressedSize uint64
		var totalBytesWritten uint64
		var fexts []types.FileExtent

		compressed := false

		for _, rec := range fsRecords {
			switch rec.Hdr.GetType() {
			case types.APFS_TYPE_INODE:
				if rec.Val.(types.JInodeVal).InternalFlags&types.INODE_HAS_UNCOMPRESSED_SIZE != 0 {
					compressed = true
					uncompressedSize = rec.Val.(types.JInodeVal).UncompressedSize
				}
				for _, xf := range rec.Val.(types.JInodeVal).Xfields {
					switch xf.XType {
					case types.INO_EXT_TYPE_NAME:
						fileName = xf.Field.(string)
					case types.INO_EXT_TYPE_DSTREAM:
						totalBytesWritten = xf.Field.(types.JDstreamT).TotalBytesWritten
					}
				}
			case types.APFS_TYPE_FILE_EXTENT:
				fexts = append(fexts, types.FileExtent{
					Address: rec.Key.(types.JFileExtentKeyT).LogicalAddr,
					Block:   rec.Val.(types.JFileExtentValT).PhysBlockNum,
					Length:  rec.Val.(types.JFileExtentValT).Length(),
				})
			case types.APFS_TYPE_XATTR:
				switch rec.Key.(types.JXattrKeyT).Name {
				case types.XATTR_RESOURCEFORK_EA_NAME:
					if rec.Val.(types.JXattrValT).Flags.DataEmbedded() {
						binary.Read(bytes.NewReader(rec.Val.(types.JXattrValT).Data.([]byte)), binary.LittleEndian, &decmpfsHdr)
					} else if rec.Val.(types.JXattrValT).Flags.DataStream() {
						fsRecords, err = a.fsOMapBtree.GetFSRecordsForOid(
							sr,
							a.FSRootBtree,
							types.OidT(rec.Val.(types.JXattrValT).Data.(types.JXattrDstreamT).XattrObjID),
							types.XidT(^uint64(0)))
						if err != nil {
							return fmt.Errorf("failed to get fs records for oid %#x: %v", types.OidT(rec.Val.(types.JXattrValT).Data.(uint64)), err)
						}
						for _, rec := range fsRecords {
							switch rec.Hdr.GetType() {
							case types.APFS_TYPE_FILE_EXTENT:
								fexts = append(fexts, types.FileExtent{
									Address: rec.Key.(types.JFileExtentKeyT).LogicalAddr,
									Block:   rec.Val.(types.JFileExtentValT).PhysBlockNum,
									Length:  rec.Val.(types.JFileExtentValT).Length(),
								})
							}
						}
					}
				case types.XATTR_DECMPFS_EA_NAME:
					decmpfsHdr, err = types.GetDecmpfsHeader(rec)
					if err != nil {
						return fmt.Errorf("failed to get decmpfs header: %v", err)
					}
				case types.XATTR_SYMLINK_EA_NAME:
					symlink = string(rec.Val.(types.JXattrValT).Data.([]byte)[:])
					fmt.Println(symlink)
				}
			}
		}

		fo, err := os.Create(filepath.Join(dest, fileName))
		if err != nil {
			return fmt.Errorf("failed to create %s: %v", filepath.Join(dest, fileName), err)
		}
		defer fo.Close()

		if compressed {
			w := bufio.NewWriter(fo)
			if err := decmpfsHdr.DecompressFile(a.r, w, fexts, false); err != nil {
				return fmt.Errorf("failed to decompress and write %s: %v", filepath.Join(dest, fileName), err)
			}
			w.Flush()

			if info, err := fo.Stat(); err == nil {
				if info.Size() != int64(uncompressedSize) {
					log.Errorf("final file size %d did NOT match expected size of %d", info.Size(), uncompressedSize)
				}
			}
			log.Infof("Created %s", filepath.Join(dest, fileName))
		} else {
			for _, fext := range fexts {
				if err := a.dev.ReadFile(bufio.NewWriter(fo), int64(fext.Block*types.BLOCK_SIZE), int64(totalBytesWritten)); err != nil {
					return fmt.Errorf("failed to write file data from device: %v", err)
				}
			}
			if info, err := fo.Stat(); err == nil {
				if info.Size() != int64(totalBytesWritten) {
					log.Errorf("final file size %d did NOT match expected size of %d", info.Size(), totalBytesWritten)
				}
			}
			log.Infof("Created %s", filepath.Join(dest, fileName))
		}
	}

	return nil
}

func (a *APFS) find(path string) ([]types.NodeEntry, error) {

	var files []types.NodeEntry

	sr := io.NewSectionReader(a.r, 0, 1<<63-1)

	fsRecords, err := a.fsOMapBtree.GetFSRecordsForOid(sr, a.FSRootBtree, types.OidT(types.FSROOT_OID), types.XidT(^uint64(0)))
	if err != nil {
		return nil, fmt.Errorf("failed to get fs records for FSROOT_OID: %v", err)
	}

	parts := strings.FieldsFunc(path, func(c rune) bool {
		return c == filepath.Separator
	})

	for idx, part := range parts {
		if len(part) > 0 {
			for _, rec := range fsRecords {
				switch rec.Hdr.GetType() {
				case types.APFS_TYPE_DIR_REC:
					if rec.Key.(types.JDrecHashedKeyT).Name == part {
						fsRecords, err = a.fsOMapBtree.GetFSRecordsForOid(sr, a.FSRootBtree, types.OidT(rec.Val.(types.JDrecVal).FileID), types.XidT(^uint64(0)))
						if err != nil {
							return nil, fmt.Errorf("failed to get fs records for oid %#x: %v", types.OidT(rec.Val.(types.JDrecVal).FileID), err)
						}
						if idx == len(parts)-1 { // last part
							switch rec.Val.(types.JDrecVal).Flags {
							case types.DT_REG:
								for _, regRec := range fsRecords {
									switch regRec.Hdr.GetType() {
									case types.APFS_TYPE_INODE:
										return append(files, rec), nil
									}
								}
							case types.DT_DIR:
								fsRecords, err = a.fsOMapBtree.GetFSRecordsForOid(sr, a.FSRootBtree, types.OidT(rec.Val.(types.JDrecVal).FileID), types.XidT(^uint64(0)))
								if err != nil {
									return nil, fmt.Errorf("failed to get fs records for oid %#x: %v", types.OidT(rec.Val.(types.JDrecVal).FileID), err)
								}
								for _, dirRec := range fsRecords {
									switch dirRec.Hdr.GetType() {
									case types.APFS_TYPE_DIR_REC:
										fsRecords, err = a.fsOMapBtree.GetFSRecordsForOid(sr, a.FSRootBtree, types.OidT(dirRec.Val.(types.JDrecVal).FileID), types.XidT(^uint64(0)))
										if err != nil {
											return nil, fmt.Errorf("failed to get fs records for oid %#x: %v", types.OidT(dirRec.Val.(types.JDrecVal).FileID), err)
										}
										for _, rec := range fsRecords {
											switch rec.Hdr.GetType() {
											case types.APFS_TYPE_INODE:
												files = append(files, dirRec)
											}
										}
									}
								}
								return files, nil
							}
						}
					}
				}
			}
		}
	}

	return nil, fmt.Errorf("did not find file %s", path)
}
