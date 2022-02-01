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
func (h *DecmpfsDiskHeader) DecompressFile(r io.ReaderAt, decomp *bufio.Writer, fexts []FileExtent) (err error) {

	var max int
	var buff []byte
	var totalSize int
	var buf bytes.Buffer
	var blocks []cmpfRsrcBlock

	for _, fext := range fexts {
		rr := io.NewSectionReader(r, int64(fext.Block*BLOCK_SIZE), int64(fext.Length))
		if _, err := buf.ReadFrom(rr); err != nil {
			return fmt.Errorf("failed to read from reader: %v", err)
		}
	}

	rsrc := bytes.NewReader(buf.Bytes())

	switch h.CompressionType {
	case CMP_ATTR_ZLIB:
		if h.AttrBytes[0] == 0x78 { // zlib attr
			zr, err := zlib.NewReader(bytes.NewReader(h.AttrBytes))
			if err != nil {
				return fmt.Errorf("failed to create zlib reader: %v", err)
			}
			if _, err = decomp.ReadFrom(zr); err != nil {
				return fmt.Errorf("failed to read from zlib reader for CMP_ATTR_ZLIB attr: %w", err)
			}
			zr.Close()
		} else if (h.AttrBytes[0] & 0x0F) == 0x0F { // uncompressed attr
			if _, err := decomp.Write(h.AttrBytes[1:]); err != nil {
				return fmt.Errorf("failed to write CMP_ATTR_ZLIB uncompressed attr: %w", err)
			}
		} else {
			return fmt.Errorf("unknown CMP_ATTR_ZLIB type: %#x", h.AttrBytes[0])
		}
	case CMP_RSRC_ZLIB:
		var rsrcHdr CmpfRsrcHead
		if err := binary.Read(rsrc, binary.BigEndian, &rsrcHdr); err != nil {
			return fmt.Errorf("failed to read rsrc header: %v", err)
		}

		rsrc.Seek(int64(rsrcHdr.HeaderSize), io.SeekStart)

		var blkHdr CmpfRsrcBlockHead
		if err = binary.Read(rsrc, binary.BigEndian, &blkHdr.DataSize); err != nil {
			return fmt.Errorf("failed to read rsrc block header data size: %v", err)
		}
		if err = binary.Read(rsrc, binary.LittleEndian, &blkHdr.NumBlocks); err != nil {
			return fmt.Errorf("failed to read rsrc block header num blocks: %v", err)
		}

		blocks := make([]cmpfRsrcBlock, blkHdr.NumBlocks)
		if err = binary.Read(rsrc, binary.LittleEndian, &blocks); err != nil {
			return fmt.Errorf("failed to read rsrc blocks: %v", err)
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

		for idx := 0; idx < len(blocks); idx++ {
			rsrc.Seek(int64(blocks[idx].Offset+rsrcHdr.HeaderSize+4), io.SeekStart)

			buff = buff[:blocks[idx].Size]
			if err := binary.Read(rsrc, binary.BigEndian, &buff); err != nil {
				return fmt.Errorf("failed to read compressed data from device: %v", err)
			}

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
	case CMP_ATTR_LZVN:
		if h.AttrBytes[0] == 0x06 { // uncompressed attr
			if _, err := decomp.Write(h.AttrBytes[1:]); err != nil {
				return fmt.Errorf("failed to write uncompressed CMP_ATTR_LZVN attr: %w", err)
			}
		} else {
			dec := make([]byte, h.UncompressedSize)
			if n := lzfse.DecodeLZVNBuffer(buff, dec); n == 0 {
				return fmt.Errorf("failed to decode CMP_ATTR_LZVN compressed data")
			}
			if _, err := decomp.Write(dec); err != nil {
				return fmt.Errorf("failed to write lzvn decompressed CMP_ATTR_LZVN attr: %w", err)
			}
		}
	case CMP_RSRC_LZVN:
		// read offset array
		var firstOffset uint32
		if err := binary.Read(rsrc, binary.LittleEndian, &firstOffset); err != nil {
			return fmt.Errorf("failed to read CMP_RSRC_LZVN first offset: %v", err)
		}
		rsrc.Seek(0, io.SeekStart)
		offsets := make([]uint32, firstOffset/uint32(binary.Size(firstOffset)))
		if err := binary.Read(rsrc, binary.LittleEndian, &offsets); err != nil {
			return fmt.Errorf("failed to read CMP_RSRC_LZVN offsets: %v", err)
		}
		// convert offset array to blocks
		for idx, off := range offsets {
			if idx >= len(offsets)-1 {
				blocks = append(blocks, cmpfRsrcBlock{
					Offset: off,
					Size:   uint32(buf.Len()) - off,
				})
				continue
			}
			blocks = append(blocks, cmpfRsrcBlock{
				Offset: off,
				Size:   uint32(offsets[idx+1]) - off,
			})
		}
		for _, blk := range blocks {
			totalSize += int(blk.Size)
			if max < int(blk.Size) {
				max = int(blk.Size)
			}
		}
		buff = make([]byte, 0, max)

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

		// 64k blocks
		dec := make([]byte, 0x10000)
		for idx, total := 0, 0; idx < len(blocks) || uint64(total) < h.UncompressedSize; idx++ {
			rsrc.Seek(int64(blocks[idx].Offset), io.SeekStart)

			buff = buff[:blocks[idx].Size]
			if err := binary.Read(rsrc, binary.BigEndian, &buff); err != nil {
				return fmt.Errorf("failed to read CMP_RSRC_LZVN data from device: %v", err)
			}

			if buff[0] == 0x06 { // uncompressed block
				nn, err := decomp.Write(buff[1:])
				if err != nil {
					return fmt.Errorf("failed to write CMP_RSRC_LZVN uncompressed block: %w", err)
				}
				total += nn
			} else {
				lzfse.DecodeLZVNBuffer(buff, dec)
				if total+len(dec) >= int(h.UncompressedSize) {
					dec = dec[:h.UncompressedSize-uint64(total)]
				}
				nn, err := decomp.Write(dec)
				if err != nil {
					return fmt.Errorf("failed to write decompressed CMP_RSRC_LZVN data for block %d: %w", idx, err)
				}
				total += nn
			}
			bar.Increment()
		}
		p.Wait()
	case CMP_ATTR_LZFSE:
		if h.AttrBytes[0] == 0x06 { // uncompressed attr
			if _, err := decomp.Write(h.AttrBytes[1:]); err != nil {
				return fmt.Errorf("failed to write uncompressed CMP_ATTR_LZFSE attr: %w", err)
			}
		} else {
			if _, err := decomp.Write(lzfse.DecodeBuffer(h.AttrBytes)); err != nil {
				return fmt.Errorf("failed to write lzfse decompressed CMP_ATTR_LZFSE attr: %w", err)
			}
		}
	case CMP_RSRC_LZFSE:
		// read offset array
		var firstOffset uint32
		if err := binary.Read(rsrc, binary.LittleEndian, &firstOffset); err != nil {
			return fmt.Errorf("failed to read CMP_RSRC_LZFSE first offset: %v", err)
		}
		rsrc.Seek(0, io.SeekStart)
		offsets := make([]uint32, firstOffset/uint32(binary.Size(firstOffset)))
		if err := binary.Read(rsrc, binary.LittleEndian, &offsets); err != nil {
			return fmt.Errorf("failed to read CMP_RSRC_LZFSE offsets: %v", err)
		}
		// convert offset array to blocks
		for idx, off := range offsets {
			if idx >= len(offsets)-1 {
				blocks = append(blocks, cmpfRsrcBlock{
					Offset: off,
					Size:   uint32(buf.Len()) - off,
				})
				continue
			}
			blocks = append(blocks, cmpfRsrcBlock{
				Offset: off,
				Size:   uint32(offsets[idx+1]) - off,
			})
		}
		for _, blk := range blocks {
			totalSize += int(blk.Size)
			if max < int(blk.Size) {
				max = int(blk.Size)
			}
		}
		buff = make([]byte, 0, max)

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

		// 64k blocks
		for idx, total := 0, 0; idx < len(blocks) || uint64(total) < h.UncompressedSize; idx++ {
			rsrc.Seek(int64(blocks[idx].Offset), io.SeekStart)

			buff = buff[:blocks[idx].Size]
			if err := binary.Read(rsrc, binary.BigEndian, &buff); err != nil {
				return fmt.Errorf("failed to read compressed CMP_RSRC_LZFSE from device: %v", err)
			}

			if buff[0] == 0x06 { // uncompressed block
				nn, err := decomp.Write(buff[1:])
				if err != nil {
					return fmt.Errorf("failed to write uncompressed CMP_RSRC_LZFSE block: %w", err)
				}
				total += nn
			} else {
				dec := lzfse.DecodeBuffer(buff)
				if total+len(dec) >= int(h.UncompressedSize) {
					dec = dec[:h.UncompressedSize-uint64(total)]
				}
				nn, err := decomp.Write(dec)
				if err != nil {
					return fmt.Errorf("failed to write decompressed CMP_RSRC_LZFSE data for block %d: %w", idx, err)
				}
				total += nn
			}
			bar.Increment()
		}
		p.Wait()
	case CMP_TYPE1:
		fallthrough // TODO: confirm this is correct (do I still skip the first byte?)
	case CMP_ATTR_UNCOMPRESSED:
		if _, err := decomp.Write(h.AttrBytes[1:]); err != nil { // TODO: figure out what the first byte is
			return fmt.Errorf("failed to write CMP_ATTR_UNCOMPRESSED attr: %w", err)
		}
	case CMP_RSRC_UNCOMPRESSED:
		buff := make([]byte, h.UncompressedSize)
		if err := binary.Read(rsrc, binary.BigEndian, buff); err != nil {
			return fmt.Errorf("failed to read CMP_RSRC_UNCOMPRESSED data from device: %v", err)
		}
		if _, err := decomp.Write(buff); err != nil {
			return fmt.Errorf("failed to write CMP_RSRC_UNCOMPRESSED data: %w", err)
		}
	default:
		return fmt.Errorf("unknown compression type: %s", h.CompressionType)
	}

	return nil
}
