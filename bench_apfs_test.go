package apfs_test

import (
	"bufio"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/blacktop/go-apfs"
	"github.com/blacktop/go-apfs/pkg/disk/dmg"
)

// TestAPFSDMG is the path to an APFS-formatted test DMG (Fork.dmg is HFS+).
// Download with: curl -L -o testdata/Ghostty.dmg https://release.files.ghostty.org/1.3.1/Ghostty.dmg
const TestAPFSDMG = "testdata/Ghostty.dmg"

// openTestAPFS finds the Apple_APFS partition,
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

	i := slices.IndexFunc(dmgFile.Partitions, func(p dmg.Partition) bool { return strings.Contains(p.Name, "Apple_APFS") })
	if i < 0 {
		b.Skip("no APFS partition found in DMG")
	}
	part := &dmgFile.Partitions[i]

	raw := filepath.Join(b.TempDir(), "partition.raw")
	out, err := os.Create(raw)
	if err != nil {
		b.Fatal(err)
	}
	bw := bufio.NewWriter(out)
	if err := part.Write(bw); err != nil {
		b.Fatalf("failed to dump partition: %v", err)
	}
	if err := bw.Flush(); err != nil {
		b.Fatal(err)
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
// slowest path for app-bundle DMGs. Each file copied does a per-file
// B-tree lookup via GetFSRecordsForOid.
func BenchmarkAPFSCopy(b *testing.B) {
	fs := openTestAPFS(b)
	volName := strings.TrimRight(string(fs.Volume.VolumeName[:]), "\x00")
	src := "/" + volName + ".app"

	dest := filepath.Join(b.TempDir(), "out")
	for b.Loop() {
		b.StopTimer()
		if err := os.RemoveAll(dest); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		if err := fs.Copy(src, dest); err != nil {
			b.Fatalf("failed to copy %s: %v", src, err)
		}
	}
}
