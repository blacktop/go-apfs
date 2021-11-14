package types

import (
	"bufio"
	"bytes"
	"io/ioutil"

	"compress/zlib"
	"encoding/binary"
	"fmt"
	"io"

	lzfse "github.com/blacktop/lzfse-cgo"
	"github.com/vbauerster/mpb/v7"
	"github.com/vbauerster/mpb/v7/decor"
)

//go:generate stringer -type=compMethod -output decmpfs_string.go

type compMethod uint32

const (
	MAX_DECMPFS_XATTR_SIZE = 3802
	DECMPFS_MAGIC          = "cmpf" // 0x636d7066
	DECMPFS_XATTR_NAME     = "com.apple.decmpfs"
)

// https://opensource.apple.com/source/copyfile/copyfile-138/copyfile.c.auto.html
const (
	CMP_TYPE1     compMethod = 1 // Uncompressed data in xattr
	CMP_ATTR_ZLIB compMethod = 3
	CMP_RSRC_ZLIB compMethod = 4 // 64k blocks
	/*
	 *  case 5: specifies de-dup within the generation store. Don't copy decmpfs xattr.
	 *  case 6: unused
	 */
	CMP_ATTR_LZVN         compMethod = 7
	CMP_RSRC_LZVN         compMethod = 8  // 64k blocks
	CMP_ATTR_UNCOMPRESSED compMethod = 9  // uncompressed data in xattr (similar to but not identical to CMP_Type1)
	CMP_RSRC_UNCOMPRESSED compMethod = 10 // 64k chunked uncompressed data in resource fork
	CMP_ATTR_LZFSE        compMethod = 11
	CMP_RSRC_LZFSE        compMethod = 12 // 64k blocks

	/* additional types defined in AppleFSCompression project */

	CMP_MAX compMethod = 255 // Highest compression_type supported
)

// DecmpfsDiskHeader this structure represents the xattr on disk; the fields below are little-endian
type DecmpfsDiskHeader struct {
	decmpfsDiskHeader
	AttrBytes []byte
}

type decmpfsDiskHeader struct {
	Magic            magic
	CompressionType  compMethod
	UncompressedSize uint64
	_                byte
}

func (h DecmpfsDiskHeader) String() string {
	return fmt.Sprintf("magic=%s, compression_type=%s, uncompressed_size=%d",
		h.Magic,
		h.CompressionType,
		h.UncompressedSize,
	)
}

// DecmpfsHeader this structure represents the xattr in memory; the fields below are host-endian
type DecmpfsHeader struct {
	AttrSize         uint32
	Magic            magic
	CompressionType  uint32
	UncompressedSize uint64
	AttrBytes        [0]byte
}

// CmpfRsrcHead (fields are big-endian)
type CmpfRsrcHead struct {
	HeaderSize uint32
	TotalSize  uint32
	DataSize   uint32
	Flags      uint32
}

// cmpfRsrcBlock (1 x 64K block)
type cmpfRsrcBlock struct {
	Offset uint32
	Size   uint32
}

type CmpfRsrc struct {
	EntryCount uint32
	Entries    [32]cmpfRsrcBlock
}

type CmpfRsrcBlockHead struct {
	DataSize  uint32
	NumBlocks uint32
	Blocks    []cmpfRsrcBlock
}

type CmpfEnd struct {
	_     [24]byte
	Unk1  uint16
	Unk2  uint16
	Unk3  uint16
	Magic magic
	Flags uint32
	Size  uint64
	Unk4  uint32
}

// GetDecmpfsHeader parses the  decmpfs header from an xattr node entry
func GetDecmpfsHeader(ne NodeEntry) (*DecmpfsDiskHeader, error) {
	var hdr DecmpfsDiskHeader
	if ne.Hdr.GetType() == APFS_TYPE_XATTR {
		if ne.Key.(JXattrKeyT).Name == DECMPFS_XATTR_NAME {
			r := bytes.NewReader(ne.Val.(JXattrValT).Data.([]byte))
			err := binary.Read(r, binary.LittleEndian, &hdr.decmpfsDiskHeader)
			if err != nil {
				return nil, err
			}
			hdr.AttrBytes, err = ioutil.ReadAll(r)
			if err != nil {
				return nil, err
			}
			return &hdr, nil
		}
	}
	return nil, fmt.Errorf("type is not APFS_TYPE_XATTR")
}

// DecompressFile decompresses decmpfs data
func (h *DecmpfsDiskHeader) DecompressFile(r io.ReaderAt, decomp *bufio.Writer, blockAddr, length uint64) (err error) {

	var max int
	var slice uint64
	var totalSize int

	sr := io.NewSectionReader(r, int64(blockAddr*BLOCK_SIZE), int64(length))

	switch h.CompressionType {
	case CMP_ATTR_ZLIB:
		panic("CMP_ATTR_ZLIB not supported (need to figure out where to grab compressed data from)")
	case CMP_RSRC_ZLIB:
		var rsrcHdr CmpfRsrcHead
		if err := binary.Read(sr, binary.BigEndian, &rsrcHdr); err != nil {
			return err
		}

		sr.Seek(int64(rsrcHdr.HeaderSize), io.SeekStart)

		var blkHdr CmpfRsrcBlockHead
		if err = binary.Read(sr, binary.BigEndian, &blkHdr.DataSize); err != nil {
			return err
		}
		if err = binary.Read(sr, binary.LittleEndian, &blkHdr.NumBlocks); err != nil {
			return err
		}

		blocks := make([]cmpfRsrcBlock, blkHdr.NumBlocks)
		if err = binary.Read(sr, binary.LittleEndian, &blocks); err != nil {
			return err
		}

		// initialize progress bar
		p := mpb.New(mpb.WithWidth(80))
		// adding a single bar, which will inherit container's width
		bar := p.Add(int64(len(blocks)),
			// progress bar filler with customized style
			mpb.NewBarFiller(mpb.BarStyle().Lbound("[").Filler("=").Tip(">").Padding("-").Rbound("|")),
			mpb.PrependDecorators(
				decor.Name("     ", decor.WC{W: len("     ") + 1, C: decor.DidentRight}),
				// replace ETA decorator with "done" message, OnComplete event
				decor.OnComplete(
					decor.AverageETA(decor.ET_STYLE_GO, decor.WC{W: 4}), "✅ ",
				),
			),
			mpb.AppendDecorators(decor.Percentage()),
		)

		for _, blk := range blocks {
			totalSize += int(blk.Size)
			if max < int(blk.Size) {
				max = int(blk.Size)
			}
		}
		buff := make([]byte, 0, max)

		dec := make([]byte, totalSize)
		if _, err = r.ReadAt(dec, int64(blockAddr*BLOCK_SIZE+uint64(blocks[0].Offset)+uint64(rsrcHdr.HeaderSize)+4)); err != nil {
			return fmt.Errorf("failed to read decompressed data from device: %v", err)
		}

		for idx, blk := range blocks {
			buff = dec[slice : slice+uint64(blk.Size)]
			slice += uint64(blk.Size)

			if buff[0] == 0x78 { // zlib block
				zr, err := zlib.NewReader(bytes.NewReader(buff))
				if err != nil {
					return fmt.Errorf("failed to create zlib reader: %v", err)
				}
				if _, err = decomp.ReadFrom(zr); err != nil {
					return fmt.Errorf("failed to read from zlib reader for block %d: %w", idx, err)
				}
				zr.Close()
			} else if (buff[0] & 0x0F) == 0x0F { // uncompressed block
				if _, err := decomp.Write(buff[1:]); err != nil {
					return fmt.Errorf("failed to write uncompressed block: %w", err)
				}
			} else {
				return fmt.Errorf("found unknown chunk type data in resource fork compressed data for block %d", idx)
			}
			bar.Increment()
		}
		p.Wait()
		// var footer CmpfEnd
		// if err := binary.Read(r, binary.BigEndian, &footer); err != nil {
		// 	return err
		// }
	case CMP_ATTR_LZVN:
		panic("CMP_ATTR_LZVN not supported (need to figure out where to grab compressed data from)")
	case CMP_RSRC_LZVN:
		fallthrough
	case CMP_RSRC_LZFSE:
		var rsrcHdr CmpfRsrcHead
		if err := binary.Read(sr, binary.BigEndian, &rsrcHdr); err != nil {
			return err
		}

		sr.Seek(int64(rsrcHdr.HeaderSize), io.SeekStart)

		var blkHdr CmpfRsrcBlockHead
		if err := binary.Read(sr, binary.BigEndian, &blkHdr.DataSize); err != nil {
			return err
		}
		if err := binary.Read(sr, binary.LittleEndian, &blkHdr.NumBlocks); err != nil {
			return err
		}

		blocks := make([]cmpfRsrcBlock, blkHdr.NumBlocks)
		if err := binary.Read(sr, binary.LittleEndian, &blocks); err != nil {
			return err
		}

		// initialize progress bar
		p := mpb.New(mpb.WithWidth(80))
		// adding a single bar, which will inherit container's width
		bar := p.Add(int64(len(blocks)),
			// progress bar filler with customized style
			mpb.NewBarFiller(mpb.BarStyle().Lbound("[").Filler("=").Tip(">").Padding("-").Rbound("|")),
			mpb.PrependDecorators(
				decor.Name("     ", decor.WC{W: len("     ") + 1, C: decor.DidentRight}),
				// replace ETA decorator with "done" message, OnComplete event
				decor.OnComplete(
					decor.AverageETA(decor.ET_STYLE_GO, decor.WC{W: 4}), "✅ ",
				),
			),
			mpb.AppendDecorators(decor.Percentage()),
		)

		for _, blk := range blocks {
			totalSize += int(blk.Size)
			if max < int(blk.Size) {
				max = int(blk.Size)
			}
		}
		buff := make([]byte, 0, max)

		dec := make([]byte, totalSize)
		if _, err = r.ReadAt(dec, int64(blockAddr*BLOCK_SIZE+uint64(blocks[0].Offset)+uint64(rsrcHdr.HeaderSize)+4)); err != nil {
			return fmt.Errorf("failed to read decompressed data from device: %v", err)
		}

		for idx, blk := range blocks {
			buff = dec[slice : slice+uint64(blk.Size)]
			slice += uint64(blk.Size)
			if buff[0] == 0x78 { // lzvn block
				if _, err = decomp.Write(lzfse.DecodeBuffer(buff)); err != nil {
					return fmt.Errorf("failed to write from lzvn/lzfse decoder for block %d: %w", idx, err)
				}
			} else if buff[0] == 0x06 { // uncompressed block TODO: make sure this is the same for lzvn AND lzfse
				if _, err = decomp.Write(buff[1:]); err != nil {
					return err
				}
			} else {
				return fmt.Errorf("found unknown chunk type data in resource fork compressed data for block %d", idx)
			}
			bar.Increment()
		}
		p.Wait()
		// var footer CmpfEnd
		// if err := binary.Read(r, binary.BigEndian, &footer); err != nil {
		// 	return err
		// }
	case CMP_ATTR_UNCOMPRESSED:
		// data is in APFS_TYPE_XATTR data
	case CMP_RSRC_UNCOMPRESSED:
		buff := make([]byte, h.UncompressedSize)
		if err := binary.Read(sr, binary.BigEndian, buff); err != nil {
			return err
		}
		if _, err := decomp.Write(buff); err != nil {
			return fmt.Errorf("failed to write uncompressed block: %w", err)
		}
	default:
		return fmt.Errorf("unknown compression type: %s", h.CompressionType)
	}

	return nil
}
