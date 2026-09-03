package apfs_test

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blacktop/go-apfs"
	"github.com/blacktop/go-apfs/pkg/disk/dmg"
)

// TestAPFSDMG is the path to an APFS-formatted test DMG (Fork.dmg is HFS+).
// Download with: curl -L -o testdata/Ghostty.dmg https://release.files.ghostty.org/1.3.1/Ghostty.dmg
const TestAPFSDMG = "testdata/Ghostty.dmg"

// openTestAPFS mirrors GAL's extraction path: find the Apple_APFS partition,
// dump it to a raw file, and open that with apfs.Open.
func openTestAPFS(b *testing.B) *apfs.APFS {
	if _, err := os.Stat(TestAPFSDMG); os.IsNotExist(err) {
		b.Skipf("test DMG not found at %s", TestAPFSDMG)
	}
	dmgFile, err := dmg.Open(TestAPFSDMG, &dmg.Config{})
	if err != nil {
		b.Fatalf("failed to open DMG: %v", err)
	}
	b.Cleanup(func() { dmgFile.Close() })

	var part *dmg.Partition
	for i := range dmgFile.Partitions {
		if strings.Contains(dmgFile.Partitions[i].Name, "Apple_APFS") {
			part = &dmgFile.Partitions[i]
			break
		}
	}
	if part == nil {
		b.Skip("no APFS partition found in DMG")
	}

	raw := filepath.Join(b.TempDir(), "partition.raw")
	out, err := os.Create(raw)
	if err != nil {
		b.Fatal(err)
	}
	buf := make([]byte, 1<<20)
	for off := int64(0); ; {
		n, err := part.ReadAt(buf, off)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				b.Fatal(werr)
			}
			off += int64(n)
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			b.Fatalf("failed to read partition: %v", err)
		}
	}
	out.Close()

	fs, err := apfs.Open(raw)
	if err != nil {
		b.Fatalf("failed to open APFS: %v", err)
	}
	b.Cleanup(func() { fs.Close() })
	return fs
}

// BenchmarkAPFSCopy measures a full extraction of the app bundle, the
// operation GAL performs on every APFS DMG. Each file copied does a per-file
// B-tree lookup via GetFSRecordsForOid.
func BenchmarkAPFSCopy(b *testing.B) {
	fs := openTestAPFS(b)
	volName := strings.TrimRight(string(fs.Volume.VolumeName[:]), "\x00")
	src := "/" + volName + ".app"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dest := filepath.Join(b.TempDir(), "out")
		if err := fs.Copy(src, dest); err != nil {
			b.Fatalf("failed to copy %s: %v", src, err)
		}
	}
}
