package dmg

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/des"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

const (
	EncryptedMagic = "encrcdsa"
)

var ErrNotEncrypted = fmt.Errorf("not an encrypted DMG")

type EncryptionHeader struct {
	Magic                [8]byte // "encrcdsa"
	Version              uint32  // 2
	EncIvSize            uint32
	Unknown1             uint32
	Unknown2             uint32
	DataEncKeyBits       uint32
	Unknown3             uint32
	HmacKeyBits          uint32
	UUID                 [16]byte
	Blocksize            uint32
	Datasize             uint64
	Dataoffset           uint64
	Unknown4             [24]byte
	KdfAlgorithm         uint32
	KdfPrngAlgorithm     uint32
	KdfIterationCount    uint32
	KdfSaltLen           uint32
	KdfSalt              [32]byte
	BlobEncIvSize        uint32
	BlobEncIv            [32]byte
	BlobEncKeyBits       uint32
	BlobEncAlgorithm     uint32 // 17
	BlobEncPadding       uint32 // 7
	BlobEncMode          uint32 // 6
	EncryptedKeyblobSize uint32
	EncryptedKeyblob1    [32]byte
	EncryptedKeyblob2    [32]byte
}

func DecryptDMG(path string, password string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	var hdr EncryptionHeader
	if err := binary.Read(f, binary.BigEndian, &hdr); err != nil {
		return "", fmt.Errorf("failed to read encryption header: %w", err)
	}

	if string(hdr.Magic[:]) != EncryptedMagic {
		return "", ErrNotEncrypted
	}

	if hdr.Version != 2 {
		return "", fmt.Errorf("unsupported encryption version: %d", hdr.Version)
	}

	if hdr.BlobEncAlgorithm != 17 || hdr.BlobEncMode != 6 || hdr.BlobEncPadding != 7 {
		return "", fmt.Errorf("unsupported blob encryption algorithm: %d, mode: %d, padding: %d",
			hdr.BlobEncAlgorithm, hdr.BlobEncMode, hdr.BlobEncPadding)
	}

	tmp, err := os.CreateTemp("", "dmg")
	if err != nil {
		return "", fmt.Errorf("failed to create temp DMG file: %w", err)
	}
	defer tmp.Close()

	if hdr.KdfAlgorithm != 103 || hdr.KdfPrngAlgorithm != 0 || hdr.KdfSaltLen != 20 {
		return "", fmt.Errorf("unsupported key derivation algorithm: %d, prng algorithm: %d, salt length: %d",
			hdr.KdfAlgorithm, hdr.KdfPrngAlgorithm, hdr.KdfSaltLen)
	}

	// derive key using PBKDF2 with SHA1
	dk, err := pbkdf2.Key(sha1.New, password, hdr.KdfSalt[:20], int(hdr.KdfIterationCount), 24)
	if err != nil {
		return "", fmt.Errorf("failed to derive key: %v", err)
	}

	// Decrypt the keyblob using Triple DES (DES_EDE3_CBC).
	tdes, err := des.NewTripleDESCipher(dk)
	if err != nil {
		tmp.Close()
		return "", fmt.Errorf("failed to create 3DES cipher: %v", err)
	}

	iv := hdr.BlobEncIv[:hdr.BlobEncIvSize]
	keyblob := append(hdr.EncryptedKeyblob1[:], hdr.EncryptedKeyblob2[:]...)
	keyblob = keyblob[:hdr.EncryptedKeyblobSize]

	if len(keyblob)%tdes.BlockSize() != 0 {
		tmp.Close()
		return "", fmt.Errorf("invalid keyblob size, not a multiple of block size")
	}

	cbc := cipher.NewCBCDecrypter(tdes, iv)
	cbc.CryptBlocks(keyblob, keyblob)

	// Extract AES and HMAC keys from the decrypted keyblob.
	aesKeySize := int(hdr.DataEncKeyBits) / 8
	hmacKeySize := int(hdr.HmacKeyBits) / 8
	if len(keyblob) < aesKeySize+hmacKeySize {
		return "", fmt.Errorf("invalid keyblob size")
	}
	aesKey := keyblob[:aesKeySize]
	hmacKey := keyblob[aesKeySize : aesKeySize+hmacKeySize]

	// Create block cipher based on AES key size.
	var blk cipher.Block
	switch hdr.DataEncKeyBits {
	case 128, 256:
		blk, err = aes.NewCipher(aesKey)
		if err != nil {
			return "", fmt.Errorf("failed to create AES %d-bit cipher: %w", hdr.DataEncKeyBits, err)
		}
	default:
		return "", fmt.Errorf("unsupported AES key size %d", hdr.DataEncKeyBits)
	}

	if _, err := f.Seek(int64(hdr.Dataoffset), io.SeekStart); err != nil {
		return "", fmt.Errorf("failed to seek to data offset: %w", err)
	}

	buf := make([]byte, hdr.Blocksize)
	ivbuf := make([]byte, 4)
	iv = make([]byte, 16)
	plaintext := make([]byte, int(hdr.Blocksize)+blk.BlockSize())
	bytesRead := uint64(0)

	for i := range (hdr.Datasize + uint64(hdr.Blocksize) - 1) / uint64(hdr.Blocksize) {
		n, err := f.Read(buf)
		if n > 0 {
			binary.BigEndian.PutUint32(ivbuf[:], uint32(i))
			mac := hmac.New(sha1.New, hmacKey)
			if _, err := mac.Write(ivbuf[:]); err != nil {
				return "", fmt.Errorf("failed to write IV HMAC: %w", err)
			}
			copy(iv, mac.Sum(nil))
			cipher.NewCBCDecrypter(blk, iv).CryptBlocks(plaintext, buf)
			plaintext = plaintext[:min(hdr.Datasize-bytesRead, uint64(hdr.Blocksize))]
			if _, err := tmp.Write(plaintext); err != nil {
				return "", fmt.Errorf("failed to write to decrypted DMG: %w", err)
			}
			bytesRead += uint64(n)
		}
		if err == io.EOF {
			break
		} else if err != nil {
			return "", fmt.Errorf("failed to read encrypted data: %w", err)
		}
	}

	return tmp.Name(), nil
}
