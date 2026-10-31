package dmg

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blacktop/go-plist"
)

func TestOpen(t *testing.T) {
	type args struct {
		name string
		c    *Config
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		{
			name: "secure",
			args: args{
				name: "../../../testdata/secure.dmg",
				c:    &Config{Password: "password"},
			},
		},
		{
			name: "test",
			args: args{
				name: "../../../testdata/test.dmg",
				c:    &Config{},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Open(tt.args.name, tt.args.c)
			if (err != nil) != tt.wantErr {
				t.Errorf("Open() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != nil {
				defer got.Close()
			}
		})
	}
}

func openTestImage(t *testing.T, partitionName string) []byte {
	t.Helper()
	var blk bytes.Buffer
	if err := binary.Write(&blk, binary.BigEndian, udifBlockData{Signature: udifSignature{'m', 'i', 's', 'h'}}); err != nil {
		t.Fatal(err)
	}
	p, err := plist.Marshal(resourceFork{ResourceFork: map[string][]block{
		"blkx": {{Name: partitionName, Data: blk.Bytes()}},
	}}, plist.XMLFormat)
	if err != nil {
		t.Fatal(err)
	}
	var image bytes.Buffer
	image.Write(make([]byte, 512))
	image.Write(p)
	footer := UDIFResourceFile{
		Signature: udifSignature{'k', 'o', 'l', 'y'}, Version: 4, HeaderSize: 512,
		PlistOffset: 512, PlistLength: uint64(len(p)),
	}
	if err := binary.Write(&image, binary.BigEndian, footer); err != nil {
		t.Fatal(err)
	}
	return image.Bytes()
}

func TestOpenDecryptedTempOwnership(t *testing.T) {
	for _, tc := range []struct {
		name    string
		plain   []byte
		wantErr string
	}{
		{name: "NewDMG failure", plain: bytes.Repeat([]byte{'x'}, 512), wantErr: "unexpected UDIFResourceFile"},
		{name: "Load failure", plain: openTestImage(t, "Primary GPT Header"), wantErr: "failed to read gpt.Header"},
		{name: "success", plain: openTestImage(t, "synthetic partition")},
	} {
		for _, password := range []bool{false, true} {
			method := "key"
			if password {
				method = "password"
			}
			t.Run(tc.name+"/"+method, func(t *testing.T) {
				hdr, data, key := encryptedTestImage(t, tc.plain)
				path, original := writeEncryptedTestImage(t, hdr, data)
				dir := decryptedTestDir(t)
				config := &Config{Key: key}
				if password {
					config = &Config{Password: "password"}
				}
				d, err := Open(path, config)
				if tc.wantErr != "" {
					if d != nil {
						_ = d.Close()
						t.Error("Open returned a DMG on failure")
					}
					if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
						t.Errorf("Open error=%v, want %q", err, tc.wantErr)
					}
				} else {
					if err != nil {
						t.Fatal(err)
					}
					defer d.Close()
					assertFileBytes(t, d.DecryptedTemp(), tc.plain)
					assertTempOutputs(t, dir, d.DecryptedTemp())
					if err := d.Close(); err != nil {
						t.Fatal(err)
					}
				}
				assertFileBytes(t, path, original)
				assertTempOutputs(t, dir)
			})
		}
	}
}

func TestOpenResourceForkFailureCleanup(t *testing.T) {
	const childArg = "go-apfs-resource-fork-child"
	const returned = "Open returned the unsupported resource fork error"
	if len(os.Args) >= 4 && os.Args[len(os.Args)-3] == childArg {
		d, err := Open(os.Args[len(os.Args)-2], &Config{Key: os.Args[len(os.Args)-1]})
		if d != nil {
			_ = d.Close()
			t.Error("Open returned a DMG on failure")
		}
		if err == nil || !strings.Contains(err.Error(), "resource fork parsing is not supported") {
			t.Fatalf("Open error=%v, want unsupported resource fork error", err)
		}
		t.Log(returned)
		return
	}

	var plain bytes.Buffer
	plain.Write(make([]byte, 512))
	footer := UDIFResourceFile{
		Signature: udifSignature{'k', 'o', 'l', 'y'}, Version: 4, HeaderSize: 512,
		RsrcForkOffset: 1, RsrcForkLength: 1,
	}
	if err := binary.Write(&plain, binary.BigEndian, footer); err != nil {
		t.Fatal(err)
	}
	hdr, data, key := encryptedTestImage(t, plain.Bytes())
	path, original := writeEncryptedTestImage(t, hdr, data)
	dir := decryptedTestDir(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, self, "-test.run=^TestOpenResourceForkFailureCleanup$", "-test.v", "--", childArg, path, key)
	output, err := cmd.CombinedOutput()
	if err != nil || !bytes.Contains(output, []byte(returned)) {
		t.Errorf("Open child did not return normally: %v\n%s", err, output)
	}
	assertFileBytes(t, path, original)
	assertTempOutputs(t, dir)
}

func TestOpenUnencryptedPreservesInput(t *testing.T) {
	plain := openTestImage(t, "synthetic partition")
	path := filepath.Join(t.TempDir(), "plain.dmg")
	if err := os.WriteFile(path, plain, 0o600); err != nil {
		t.Fatal(err)
	}
	dir := decryptedTestDir(t)
	d, err := Open(path, &Config{})
	if err != nil {
		t.Fatal(err)
	}
	if d.DecryptedTemp() != "" {
		t.Errorf("unexpected decrypted path %q", d.DecryptedTemp())
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	assertFileBytes(t, path, plain)
	assertTempOutputs(t, dir)
}
