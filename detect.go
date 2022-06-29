package apfs

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"github.com/blacktop/go-apfs/pkg/disk/dmg"
	"github.com/blacktop/go-apfs/pkg/disk/hfsplus"
	"github.com/blacktop/go-apfs/types"
)

type Type uint8

const (
	UNKNOWN Type = iota
	EXFAT
	FAT32
	FAT16
	FAT12
	NTFS
	HFS
	APFS_RAW
	IFS
	DMG
)

func checkDMG(f *os.File) bool {
	f.Seek(0, io.SeekStart) // rewind file
	if _, err := dmg.NewDMG(f); err != nil {
		return false
	}
	return true
}

func checkHFS(f *os.File) bool {
	f.Seek(0, io.SeekStart) // rewind file
	var header hfsplus.VolumeHeader
	if err := binary.Read(f, binary.LittleEndian, &header); err != nil {
		return false
	}
	if header.Signature != hfsplus.HFSPlusSigWord || header.Signature != hfsplus.HFSXSigWord {
		return false
	}
	return true
}

func checkApfsRaw(f *os.File) bool {
	f.Seek(0, io.SeekStart) // rewind file
	obj, err := types.ReadObj(f, 0)
	if err != nil {
		return false
	}
	if nxsb, ok := obj.Body.(types.NxSuperblock); ok {
		if nxsb.Magic.String() == types.NX_MAGIC {
			return true
		}
	}
	return false
}

func detectFilesystem(f *os.File) (Type, error) {
	if checkDMG(f) {
		return DMG, nil
	} else if checkHFS(f) {
		return HFS, nil
	} else if checkApfsRaw(f) {
		return APFS_RAW, nil
	}
	return UNKNOWN, fmt.Errorf("failed to detect filesystem")
}
