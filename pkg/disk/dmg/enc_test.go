package dmg

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/des"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// encryptedTestImage builds a legacy-password container that also accepts key.
func encryptedTestImage(t *testing.T, plain []byte) (hdr EncryptionHeader, data []byte, key string) {
	t.Helper()
	const blockSize = 512
	aesKey := bytes.Repeat([]byte{0x5a}, 16)
	hmacKey := bytes.Repeat([]byte{0xa5}, 20)
	hdr = EncryptionHeader{
		Version: 2, DataEncKeyBits: 128, HmacKeyBits: 160,
		Blocksize: blockSize, Datasize: uint64(len(plain)),
		KdfAlgorithm: 103, KdfIterationCount: 1, KdfSaltLen: 20,
		BlobEncIvSize: des.BlockSize, BlobEncKeyBits: 192,
		BlobEncAlgorithm: blobEncAlgorithm3DESLegacy,
		BlobEncPadding:   blobEncPaddingPKCS7, BlobEncMode: blobEncModeCBC,
		EncryptedKeyblobSize: 64,
	}
	copy(hdr.Magic[:], EncryptedMagic)
	hdr.Dataoffset = uint64(binary.Size(hdr))
	dk, err := pbkdf2.Key(sha1.New, "password", hdr.KdfSalt[:20], int(hdr.KdfIterationCount), 24)
	if err != nil {
		t.Fatal(err)
	}
	tdes, err := des.NewTripleDESCipher(dk)
	if err != nil {
		t.Fatal(err)
	}
	keyblob := make([]byte, 64)
	copy(keyblob, aesKey)
	copy(keyblob[len(aesKey):], hmacKey)
	cipher.NewCBCEncrypter(tdes, hdr.BlobEncIv[:des.BlockSize]).CryptBlocks(keyblob, keyblob)
	copy(hdr.EncryptedKeyblob1[:], keyblob[:32])
	copy(hdr.EncryptedKeyblob2[:], keyblob[32:])
	blk, err := aes.NewCipher(aesKey)
	if err != nil {
		t.Fatal(err)
	}
	data = make([]byte, (len(plain)+blockSize-1)/blockSize*blockSize)
	copy(data, plain)
	for offset := 0; offset < len(data); offset += blockSize {
		mac := hmac.New(sha1.New, hmacKey)
		if err := binary.Write(mac, binary.BigEndian, uint32(offset/blockSize)); err != nil {
			t.Fatal(err)
		}
		chunk := data[offset : offset+blockSize]
		cipher.NewCBCEncrypter(blk, mac.Sum(nil)[:aes.BlockSize]).CryptBlocks(chunk, chunk)
	}
	return hdr, data, hex.EncodeToString(aesKey) + hex.EncodeToString(hmacKey)
}

func writeEncryptedTestImage(t *testing.T, hdr EncryptionHeader, data []byte) (string, []byte) {
	t.Helper()
	var image bytes.Buffer
	if err := binary.Write(&image, binary.BigEndian, hdr); err != nil {
		t.Fatal(err)
	}
	image.Write(data)
	path := filepath.Join(t.TempDir(), "encrypted.dmg")
	if err := os.WriteFile(path, image.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, image.Bytes()
}

func decryptedTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	t.Setenv("TMP", dir)
	t.Setenv("TEMP", dir)
	return dir
}

func assertFileBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("file contents changed: %s", path)
	}
}

func assertTempOutputs(t *testing.T, dir string, want ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(want) {
		t.Fatalf("temporary directory has %d entries, want %d: %v", len(entries), len(want), entries)
	}
	for i, entry := range entries {
		if entry.Name() != filepath.Base(want[i]) {
			t.Errorf("unexpected temporary output %q, want %q", entry.Name(), want[i])
		}
	}
}

func TestDecryptTempOwnership(t *testing.T) {
	for _, password := range []bool{false, true} {
		name := "key"
		if password {
			name = "password"
		}
		t.Run(name, func(t *testing.T) {
			plain := bytes.Repeat([]byte("plaintext"), 100)
			hdr, data, key := encryptedTestImage(t, plain)
			path, original := writeEncryptedTestImage(t, hdr, data)
			dir := decryptedTestDir(t)
			var output string
			var err error
			if password {
				output, err = DecryptDMGWithPassword(path, "password")
			} else {
				output, err = DecryptDMGWithKey(path, key)
			}
			if err != nil {
				t.Fatal(err)
			}
			assertFileBytes(t, output, plain)
			assertTempOutputs(t, dir, output)
			if err := os.Remove(output); err != nil {
				t.Fatal(err)
			}
			assertTempOutputs(t, dir)
			assertFileBytes(t, path, original)
		})
	}
}

func TestDecryptFailureCleanup(t *testing.T) {
	for _, tc := range []struct {
		name     string
		password bool
		badKey   bool
		mutate   func(*EncryptionHeader)
		wantErr  string
	}{
		{name: "unsupported AES size", mutate: func(h *EncryptionHeader) { h.DataEncKeyBits = 192 }, wantErr: "unsupported AES key size"},
		{name: "invalid AES key", badKey: true, wantErr: "failed to create AES"},
		{name: "data seek", mutate: func(h *EncryptionHeader) { h.Dataoffset = ^uint64(0) }, wantErr: "failed to seek to data offset"},
		{name: "password KDF", password: true, mutate: func(h *EncryptionHeader) { h.KdfAlgorithm = 0 }, wantErr: "unsupported key derivation"},
		{name: "password keyblob alignment", password: true, mutate: func(h *EncryptionHeader) { h.EncryptedKeyblobSize = 31 }, wantErr: "not a multiple of block size"},
		{name: "password keyblob length", password: true, mutate: func(h *EncryptionHeader) { h.HmacKeyBits = 512 }, wantErr: "invalid keyblob size"},
		{name: "password delegated decrypt", password: true, mutate: func(h *EncryptionHeader) { h.DataEncKeyBits = 192 }, wantErr: "unsupported AES key size"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hdr, data, key := encryptedTestImage(t, bytes.Repeat([]byte{'x'}, 512))
			if tc.mutate != nil {
				tc.mutate(&hdr)
			}
			path, original := writeEncryptedTestImage(t, hdr, data)
			dir := decryptedTestDir(t)
			var output string
			var err error
			switch {
			case tc.password:
				output, err = DecryptDMGWithPassword(path, "password")
			case tc.badKey:
				output, err = decryptDMGWithKeys(path, []byte{1}, nil)
			default:
				output, err = DecryptDMGWithKey(path, key)
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) || output != "" {
				t.Errorf("output=%q error=%v, want %q", output, err, tc.wantErr)
			}
			assertFileBytes(t, path, original)
			assertTempOutputs(t, dir)
		})
	}
}
