package apfs_test

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blacktop/go-apfs/pkg/disk/dmg"
	"github.com/blacktop/go-apfs/pkg/disk/hfsplus"
)

// TestDMG is the path to the test DMG file.
// Download with: curl -L -o testdata/Fork.dmg https://cdn.fork.dev/mac/Fork-2.60.4.dmg
const TestDMG = "testdata/Fork.dmg"

func skipIfNoTestDMG(t testing.TB) {
	if _, err := os.Stat(TestDMG); os.IsNotExist(err) {
		t.Skipf("test DMG not found at %s - download with: curl -L -o testdata/Fork.dmg https://cdn.fork.dev/mac/Fork-2.60.4.dmg", TestDMG)
	}
}

// BenchmarkDMGOpen measures the time to open and parse a DMG file.
// This includes reading the footer, plist, and partition table.
func BenchmarkDMGOpen(b *testing.B) {
	skipIfNoTestDMG(b)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dmgFile, err := dmg.Open(TestDMG, &dmg.Config{})
		if err != nil {
			b.Fatalf("failed to open DMG: %v", err)
		}
		dmgFile.Close()
	}
}

// BenchmarkHFSPlusMount measures the time to mount an HFS+ filesystem from a DMG.
func BenchmarkHFSPlusMount(b *testing.B) {
	skipIfNoTestDMG(b)

	dmgFile, err := dmg.Open(TestDMG, &dmg.Config{})
	if err != nil {
		b.Fatalf("failed to open DMG: %v", err)
	}
	defer dmgFile.Close()

	var hfsPartition *dmg.Partition
	for i := range dmgFile.Partitions {
		if strings.Contains(dmgFile.Partitions[i].Name, "Apple_HFS") {
			hfsPartition = &dmgFile.Partitions[i]
			break
		}
	}
	if hfsPartition == nil {
		b.Skip("no HFS+ partition found in DMG")
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hfs, err := hfsplus.New(hfsPartition)
		if err != nil {
			b.Fatalf("failed to mount HFS+: %v", err)
		}
		hfs.Close()
	}
}

// BenchmarkHFSPlusListFiles measures the time to list all files in an HFS+ filesystem.
// This is the most performance-critical operation for GAL.
func BenchmarkHFSPlusListFiles(b *testing.B) {
	skipIfNoTestDMG(b)

	dmgFile, err := dmg.Open(TestDMG, &dmg.Config{})
	if err != nil {
		b.Fatalf("failed to open DMG: %v", err)
	}
	defer dmgFile.Close()

	var hfsPartition *dmg.Partition
	for i := range dmgFile.Partitions {
		if strings.Contains(dmgFile.Partitions[i].Name, "Apple_HFS") {
			hfsPartition = &dmgFile.Partitions[i]
			break
		}
	}
	if hfsPartition == nil {
		b.Skip("no HFS+ partition found in DMG")
	}

	hfs, err := hfsplus.New(hfsPartition)
	if err != nil {
		b.Fatalf("failed to mount HFS+: %v", err)
	}
	defer hfs.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		files, err := hfs.Files()
		if err != nil {
			b.Fatalf("failed to list files: %v", err)
		}
		_ = files
	}
}

// BenchmarkHFSPlusReadFile measures the time to read a specific file from an HFS+ filesystem.
func BenchmarkHFSPlusReadFile(b *testing.B) {
	skipIfNoTestDMG(b)

	dmgFile, err := dmg.Open(TestDMG, &dmg.Config{})
	if err != nil {
		b.Fatalf("failed to open DMG: %v", err)
	}
	defer dmgFile.Close()

	var hfsPartition *dmg.Partition
	for i := range dmgFile.Partitions {
		if strings.Contains(dmgFile.Partitions[i].Name, "Apple_HFS") {
			hfsPartition = &dmgFile.Partitions[i]
			break
		}
	}
	if hfsPartition == nil {
		b.Skip("no HFS+ partition found in DMG")
	}

	hfs, err := hfsplus.New(hfsPartition)
	if err != nil {
		b.Fatalf("failed to mount HFS+: %v", err)
	}
	defer hfs.Close()

	files, err := hfs.Files()
	if err != nil {
		b.Fatalf("failed to list files: %v", err)
	}

	// Find a reasonably sized file to read (the main binary)
	var targetFile *hfsplus.FileRecord
	for _, f := range files {
		if strings.HasSuffix(f.Path(), "/Fork") && f.FileInfo.DataFork.LogicalSize > 0 {
			targetFile = f
			break
		}
	}
	if targetFile == nil {
		// Fall back to any file with content
		for _, f := range files {
			if f.FileInfo.DataFork.LogicalSize > 1000 {
				targetFile = f
				break
			}
		}
	}
	if targetFile == nil {
		b.Skip("no suitable file found for reading")
	}

	b.ResetTimer()
	b.SetBytes(int64(targetFile.FileInfo.DataFork.LogicalSize))

	for i := 0; i < b.N; i++ {
		reader := targetFile.Reader()
		if reader == nil {
			b.Fatal("failed to get file reader")
		}
		_, err := io.Copy(io.Discard, reader)
		if err != nil {
			b.Fatalf("failed to read file: %v", err)
		}
	}
}

// BenchmarkFullExtraction measures the complete extraction workflow.
// This is what GAL does: open DMG, mount, list, and extract all files.
func BenchmarkFullExtraction(b *testing.B) {
	skipIfNoTestDMG(b)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Open DMG
		dmgFile, err := dmg.Open(TestDMG, &dmg.Config{})
		if err != nil {
			b.Fatalf("failed to open DMG: %v", err)
		}

		// Find HFS+ partition
		var hfsPartition *dmg.Partition
		for j := range dmgFile.Partitions {
			if strings.Contains(dmgFile.Partitions[j].Name, "Apple_HFS") {
				hfsPartition = &dmgFile.Partitions[j]
				break
			}
		}
		if hfsPartition == nil {
			dmgFile.Close()
			b.Skip("no HFS+ partition found")
		}

		// Mount HFS+
		hfs, err := hfsplus.New(hfsPartition)
		if err != nil {
			dmgFile.Close()
			b.Fatalf("failed to mount: %v", err)
		}

		// List all files
		files, err := hfs.Files()
		if err != nil {
			hfs.Close()
			dmgFile.Close()
			b.Fatalf("failed to list: %v", err)
		}

		// Read first 10 files (simulate extraction)
		count := 0
		for _, f := range files {
			if f.FileInfo.DataFork.LogicalSize > 0 {
				reader := f.Reader()
				if reader != nil {
					io.Copy(io.Discard, reader)
					count++
					if count >= 10 {
						break
					}
				}
			}
		}

		hfs.Close()
		dmgFile.Close()
	}
}

// BenchmarkDMGReadAt measures random access read performance on the DMG.
// This tests the LRU cache and decompression performance.
func BenchmarkDMGReadAt(b *testing.B) {
	skipIfNoTestDMG(b)

	dmgFile, err := dmg.Open(TestDMG, &dmg.Config{})
	if err != nil {
		b.Fatalf("failed to open DMG: %v", err)
	}
	defer dmgFile.Close()

	buf := make([]byte, 4096)

	b.ResetTimer()
	b.SetBytes(4096)

	for i := 0; i < b.N; i++ {
		// Read at various offsets to test cache behavior
		offset := int64((i % 100) * 4096)
		_, err := dmgFile.ReadAt(buf, offset)
		if err != nil && err != io.EOF {
			b.Fatalf("failed to read at offset %d: %v", offset, err)
		}
	}
}

// BenchmarkDMGReadAtSequential measures sequential read performance.
func BenchmarkDMGReadAtSequential(b *testing.B) {
	skipIfNoTestDMG(b)

	dmgFile, err := dmg.Open(TestDMG, &dmg.Config{})
	if err != nil {
		b.Fatalf("failed to open DMG: %v", err)
	}
	defer dmgFile.Close()

	buf := make([]byte, 4096)

	b.ResetTimer()
	b.SetBytes(4096)

	offset := int64(0)
	for i := 0; i < b.N; i++ {
		_, err := dmgFile.ReadAt(buf, offset)
		if err != nil && err != io.EOF {
			b.Fatalf("failed to read at offset %d: %v", offset, err)
		}
		offset += 4096
		// Wrap around to avoid running off the end
		if offset > 10*1024*1024 {
			offset = 0
		}
	}
}

// TestIntegrationExtractDMG is an integration test that extracts files from the DMG.
// Run with: go test -v -run TestIntegrationExtractDMG
func TestIntegrationExtractDMG(t *testing.T) {
	skipIfNoTestDMG(t)

	dmgFile, err := dmg.Open(TestDMG, &dmg.Config{})
	if err != nil {
		t.Fatalf("failed to open DMG: %v", err)
	}
	defer dmgFile.Close()

	t.Logf("DMG opened successfully")
	t.Logf("Partitions:")
	for i, p := range dmgFile.Partitions {
		t.Logf("  %d: %s", i, p.Name)
	}

	var hfsPartition *dmg.Partition
	for i := range dmgFile.Partitions {
		if strings.Contains(dmgFile.Partitions[i].Name, "Apple_HFS") {
			hfsPartition = &dmgFile.Partitions[i]
			break
		}
	}
	if hfsPartition == nil {
		t.Skip("no HFS+ partition found in DMG")
	}

	hfs, err := hfsplus.New(hfsPartition)
	if err != nil {
		t.Fatalf("failed to mount HFS+: %v", err)
	}
	defer hfs.Close()

	files, err := hfs.Files()
	if err != nil {
		t.Fatalf("failed to list files: %v", err)
	}

	t.Logf("Found %d files", len(files))

	// Create temp dir for extraction
	tmpDir, err := os.MkdirTemp("", "go-apfs-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Extract a few files
	extracted := 0
	for _, f := range files {
		if f.FileInfo.DataFork.LogicalSize > 0 && f.FileInfo.DataFork.LogicalSize < 1*1024*1024 {
			reader := f.Reader()
			if reader == nil {
				continue
			}

			relPath := strings.TrimPrefix(f.Path(), string(filepath.Separator))
			destPath := filepath.Join(tmpDir, relPath)
			if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
				continue
			}

			outFile, err := os.Create(destPath)
			if err != nil {
				continue
			}

			n, err := io.Copy(outFile, reader)
			outFile.Close()

			if err == nil {
				t.Logf("Extracted %s (%d bytes)", f.Path(), n)
				extracted++
				if extracted >= 5 {
					break
				}
			}
		}
	}

	t.Logf("Successfully extracted %d files", extracted)
}
