package apfs

import (
	"bytes"
	"fmt"
	"github.com/blacktop/go-apfs/pkg/disk"
	"github.com/blacktop/go-apfs/types"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const copyTestData = "extracted contents"

func copyTestEntry(parent, oid uint64, name string, dir bool) types.NodeEntry {
	val := types.JDrecVal{}
	val.FileID = oid
	val.Flags = types.DT_REG
	if dir {
		val.Flags = types.DT_DIR
	}
	return types.NodeEntry{
		Hdr: types.JKeyT{ObjIDAndType: parent | uint64(types.APFS_TYPE_DIR_REC)<<types.OBJ_TYPE_SHIFT},
		Key: types.JDrecHashedKeyT{Name: name},
		Val: val,
	}
}

func copyTestInode(oid uint64, name string) types.NodeEntry {
	xname := types.Xfield{Field: name}
	xname.XType = types.INO_EXT_TYPE_NAME
	xstream := types.Xfield{Field: types.JDstreamT{TotalBytesWritten: uint64(len(copyTestData))}}
	xstream.XType = types.INO_EXT_TYPE_DSTREAM
	return types.NodeEntry{
		Hdr: types.JKeyT{ObjIDAndType: oid | uint64(types.APFS_TYPE_INODE)<<types.OBJ_TYPE_SHIFT},
		Val: types.JInodeVal{Xfields: []types.Xfield{xname, xstream}},
	}
}

func copyTestExtent(oid uint64) types.NodeEntry {
	return types.NodeEntry{
		Hdr: types.JKeyT{ObjIDAndType: oid | uint64(types.APFS_TYPE_FILE_EXTENT)<<types.OBJ_TYPE_SHIFT},
		Key: types.JFileExtentKeyT{},
		Val: types.JFileExtentValT{LenAndFlags: uint64(len(copyTestData))},
	}
}

func copyTestLink(oid uint64, target string) types.NodeEntry {
	return types.NodeEntry{
		Hdr: types.JKeyT{ObjIDAndType: oid | uint64(types.APFS_TYPE_XATTR)<<types.OBJ_TYPE_SHIFT},
		Key: types.JXattrKeyT{Name: types.XATTR_SYMLINK_EA_NAME},
		Val: types.JXattrValT{Data: []byte(target)},
	}
}

func copyTestFS(records ...types.NodeEntry) *APFS {
	records = append(records, copyTestEntry(types.FSROOT_OID, 10, "source", true))
	slices.SortStableFunc(records, func(a, b types.NodeEntry) int {
		if a.Hdr.GetID() < b.Hdr.GetID() {
			return -1
		}
		if a.Hdr.GetID() > b.Hdr.GetID() {
			return 1
		}
		return 0
	})
	root := types.BTreeNodePhys{}
	root.Flags = types.BTNODE_ROOT | types.BTNODE_LEAF
	root.Nkeys = uint32(len(records))
	for _, record := range records {
		root.Entries = append(root.Entries, record)
	}
	r := bytes.NewReader([]byte(copyTestData))
	return &APFS{FSRootBtree: root, fsOMapBtree: &types.BTreeNodePhys{}, r: r, dev: disk.NewGeneric(r)}
}

func TestCopyRejectsUnsafeNames(t *testing.T) {
	for _, name := range []string{"", ".", "..", "../escape", "/absolute", "nested/file", `nested\file`, "nul\x00name"} {
		for _, branch := range []string{"directory", "nested directory", "file inode", "file entry"} {
			t.Run(fmt.Sprintf("%s/%q", branch, name), func(t *testing.T) {
				var records []types.NodeEntry
				want := fmt.Sprintf("unsafe directory record name %q", name)
				switch branch {
				case "directory":
					records = []types.NodeEntry{copyTestEntry(10, 20, name, true)}
				case "nested directory":
					records = []types.NodeEntry{copyTestEntry(10, 20, "nested", true), copyTestEntry(20, 30, name, true)}
					want += " in nested"
				case "file inode":
					records = []types.NodeEntry{copyTestEntry(10, 20, "file", false), copyTestInode(20, name)}
					want = fmt.Sprintf("unsafe file record name %q in .", name)
					if name == "" {
						want = "missing inode name for file oid 0x14"
					}
				case "file entry":
					records = []types.NodeEntry{copyTestEntry(10, 20, name, false), copyTestInode(20, "file")}
					want = fmt.Sprintf("unsafe file record name %q in .", name)
				}
				err := copyTestFS(records...).Copy("/source", t.TempDir())
				if err == nil || err.Error() != want {
					t.Fatalf("Copy error = %v, want %q", err, want)
				}
			})
		}
	}
}

func TestCopyRejectsMissingInode(t *testing.T) {
	for _, missingName := range []bool{false, true} {
		t.Run(fmt.Sprint(missingName), func(t *testing.T) {
			records := []types.NodeEntry{copyTestEntry(10, 20, "file", false), copyTestExtent(20)}
			want := "missing inode for file oid 0x14"
			if missingName {
				inode := copyTestInode(20, "unused")
				inode.Val = types.JInodeVal{}
				records = append(records, inode)
				want = "missing inode name for file oid 0x14"
			}
			dest := t.TempDir()
			err := copyTestFS(records...).Copy("/source/file", dest)
			if err == nil || err.Error() != want {
				t.Fatalf("Copy error = %v, want %q", err, want)
			}
			entries, err := os.ReadDir(dest)
			if err != nil || len(entries) != 0 {
				t.Fatalf("destination entries = %v, error = %v", entries, err)
			}
		})
	}
}

func TestCopyConfinesSymlinks(t *testing.T) {
	for _, scenario := range []string{"existing file", "existing directory", "nested directory", "image file", "image directory"} {
		t.Run(scenario, func(t *testing.T) {
			dest, outside := t.TempDir(), t.TempDir()
			const original = "outside must stay unchanged"
			outsideFile := filepath.Join(outside, "file")
			if err := os.WriteFile(outsideFile, []byte(original), 0600); err != nil {
				t.Fatal(err)
			}
			var records []types.NodeEntry
			target := outside
			if strings.HasSuffix(scenario, "file") {
				target = outsideFile
				records = []types.NodeEntry{copyTestEntry(10, 30, "file", false), copyTestInode(30, "escape"), copyTestExtent(30)}
			} else {
				records = []types.NodeEntry{
					copyTestEntry(10, 30, "escape", true),
					copyTestEntry(30, 40, "new-directory", true),
					copyTestEntry(40, 50, "file", false), copyTestInode(50, "file"), copyTestExtent(50),
				}
			}
			if strings.HasPrefix(scenario, "image") {
				if scenario == "image file" {
					var err error
					target, err = filepath.Rel(dest, target)
					if err != nil {
						t.Fatal(err)
					}
				}
				records = append([]types.NodeEntry{copyTestEntry(10, 20, "escape", false), copyTestInode(20, "escape"), copyTestLink(20, target)}, records...)
			} else {
				linkPath := filepath.Join(dest, "escape")
				if scenario == "nested directory" {
					if err := os.Mkdir(filepath.Join(dest, "nested"), 0755); err != nil {
						t.Fatal(err)
					}
					linkPath = filepath.Join(dest, "nested", "escape")
					records[0] = copyTestEntry(20, 30, "escape", true)
					records = append([]types.NodeEntry{copyTestEntry(10, 20, "nested", true)}, records...)
				}
				if err := os.Symlink(target, linkPath); err != nil {
					t.Fatal(err)
				}
			}
			err := copyTestFS(records...).Copy("/source", dest)
			if err == nil || !strings.Contains(err.Error(), "path escapes from parent") {
				t.Fatalf("Copy error = %v, want root escape error", err)
			}
			got, err := os.ReadFile(outsideFile)
			if err != nil || string(got) != original {
				t.Fatalf("outside file = %q, error = %v", got, err)
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 1 || entries[0].Name() != "file" {
				t.Fatalf("outside entries = %v, error = %v", entries, err)
			}
		})
	}
}

func TestCopyNestedFilesAndSafeLinks(t *testing.T) {
	fs := copyTestFS(
		copyTestEntry(10, 20, "Versions", true),
		copyTestEntry(20, 30, "A", true),
		copyTestEntry(20, 40, "Current", false),
		copyTestEntry(20, 50, "through-link", true),
		copyTestEntry(30, 60, "file", false), copyTestInode(60, "file"), copyTestExtent(60),
		copyTestInode(40, "Current"), copyTestLink(40, "A"),
		copyTestEntry(50, 70, "new-file", false), copyTestInode(70, "new-file"), copyTestExtent(70),
	)
	dest := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dest, "Versions", "A"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("Current", filepath.Join(dest, "Versions", "through-link")); err != nil {
		t.Fatal(err)
	}
	if err := fs.Copy("/source", dest); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Versions/A/file", "Versions/Current/file", "Versions/A/new-file", "Versions/through-link/new-file"} {
		got, err := os.ReadFile(filepath.Join(dest, name))
		if err != nil || string(got) != copyTestData {
			t.Fatalf("%s = %q, error = %v", name, got, err)
		}
	}
	if target, err := os.Readlink(filepath.Join(dest, "Versions", "Current")); err != nil || target != "A" {
		t.Fatalf("link target = %q, error = %v", target, err)
	}
}

func TestCopyFileConfinesParentSymlink(t *testing.T) {
	dest, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dest, "escape")); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dest)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	entry := copyTestEntry(10, 20, "file", false)
	fs := copyTestFS(entry, copyTestInode(20, "file"), copyTestExtent(20))
	// Check file creation independently of copyDir's earlier directory check.
	err = fs.copyFile(root, entry, "escape")
	if err == nil || !strings.Contains(err.Error(), "path escapes from parent") {
		t.Fatalf("copyFile error = %v, want root escape error", err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("outside entries = %v, error = %v", entries, err)
	}
}

func TestCopyCreatesDirectoryDestination(t *testing.T) {
	fs := copyTestFS(copyTestEntry(10, 20, "nested", true), copyTestEntry(20, 30, "file", false), copyTestInode(30, "file"), copyTestExtent(30))
	dest := filepath.Join(t.TempDir(), "new", "destination")
	if err := fs.Copy("/source", dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "nested", "file"))
	if err != nil || string(got) != copyTestData {
		t.Fatalf("file = %q, error = %v", got, err)
	}
}
