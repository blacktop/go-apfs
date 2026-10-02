package dmg

import (
	"bufio"
	"bytes"
	"compress/bzip2"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/apex/log"
	"github.com/blacktop/go-apfs/pkg/adc"
	"github.com/blacktop/go-apfs/pkg/disk/gpt"
	"github.com/blacktop/go-apfs/types"
	"github.com/blacktop/go-plist"
	lzfse "github.com/blacktop/lzfse-cgo"
	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/ulikunitz/xz/lzma"
	"github.com/vbauerster/mpb/v7"
	"github.com/vbauerster/mpb/v7/decor"
	"github.com/xi2/xz"
	"io"
	"math"
	"os"
	"strings"
	"unicode/utf16"
)

// xzMagic is the 6-byte header for XZ streams (\xFD7zXZ\x00).
// DMG block maps label both raw LZMA1 and XZ/LZMA2 as type 0x80000008,
// so we sniff the magic to pick the right decompressor.
var xzMagic = []byte{0xFD, 0x37, 0x7A, 0x58, 0x5A, 0x00}

func newLZMAReader(data []byte) (io.Reader, error) {
	if len(data) >= 6 && bytes.Equal(data[:6], xzMagic) {
		return xz.NewReader(bytes.NewReader(data), maxLZMADictionary)
	}
	return (lzma.ReaderConfig{DictCap: maxLZMADictionary}).NewReader(bytes.NewReader(data))
}

const (
	sectorSize    = 0x200
	zeroBlockSize = 32 << 10
	// ponytail: buffered chunks above 64 MiB are unsupported; stream decoding before raising this ceiling.
	maxDecodedChunkSize    = 64 << 20
	maxCompressedChunkSize = 64 << 20
	maxLZMADictionary      = xz.DefaultDictMax
)

var ErrEncrypted = errors.New("DMG is encrypted")

// Config is the DMG config
type Config struct {
	Password     string
	Key          string
	DisableCache bool
}

// DMG apple disk image object
type DMG struct {
	Footer     UDIFResourceFile
	Plist      resourceFork
	Nsiz       nsiz
	Partitions []Partition

	firstAPFSPartition int

	cache        *lru.Cache[int, []byte]
	evictCounter uint64

	config Config

	decrypted string

	sr     *io.SectionReader
	closer io.Closer
}

type block struct {
	Attributes string
	Data       []byte
	ID         string
	Name       string
	CFName     string `plist:"CFName,omitempty"`
}

type resourceFork struct {
	ResourceFork map[string][]block `plist:"resource-fork,omitempty"`
}

type volAndUUID struct {
	Name string `plist:"name,omitempty"`
	UUID string `plist:"uuid,omitempty"`
}

type nsiz struct {
	Sha1Digest          []byte       `plist:"SHA-1-digest,omitempty"`
	Sha256Digest        []byte       `plist:"SHA-256-digest,omitempty"`
	VolumeNamesAndUUIDs []volAndUUID `plist:"Volume names and UUIDs,omitempty"`
	BlockChecksum2      int          `plist:"block-checksum-2,omitempty"`
	PartNum             int          `plist:"part-num,omitempty"`
	Version             int          `plist:"version,omitempty"`
}

type udifSignature [4]byte

func (s udifSignature) String() string {
	return string(s[:])
}

type udifChecksumType uint32

const (
	NONE_TYPE  udifChecksumType = 0
	CRC32_TYPE udifChecksumType = 2
)

// UDIFChecksum object
type UDIFChecksum struct {
	Type udifChecksumType
	Size uint32
	Data [32]uint32
}

const (
	udifRFSignature = "koly"
	udifRFVersion   = 4
	udifSectorSize  = 512
)

type udifResourceFileFlag uint32

const (
	Flattened       udifResourceFileFlag = 0x00000001
	InternetEnabled udifResourceFileFlag = 0x00000004
)

// UDIFResourceFile - Universal Disk Image Format (UDIF) DMG Footer
type UDIFResourceFile struct {
	Signature             udifSignature // magic 'koly'
	Version               uint32        // 4 (as of 2013)
	HeaderSize            uint32        // sizeof(this) =  512 (as of 2013)
	Flags                 udifResourceFileFlag
	RunningDataForkOffset uint64
	DataForkOffset        uint64 // usually 0, beginning of file
	DataForkLength        uint64
	RsrcForkOffset        uint64 // resource fork offset and length
	RsrcForkLength        uint64
	SegmentNumber         uint32 // Usually 1, can be 0
	SegmentCount          uint32 // Usually 1, can be 0
	SegmentID             types.UUID

	DataChecksum UDIFChecksum

	PlistOffset uint64 // Offset and length of the blkx plist.
	PlistLength uint64

	Reserved1 [64]byte

	CodeSignatureOffset uint64
	CodeSignatureLength uint64

	Reserved2 [40]byte

	MasterChecksum UDIFChecksum

	ImageVariant uint32 // Unknown, commonly 1
	SectorCount  uint64

	Reserved3 uint32
	Reserved4 uint32
	Reserved5 uint32
}

const (
	udifBDSignature = "mish"
	udifBDVersion   = 1
)

// UDIFBlockData object (a partition)
type udifBlockData struct {
	Signature        udifSignature // magic 'mish'
	Version          uint32
	StartSector      uint64 // Logical block offset and length, in sectors.
	SectorCount      uint64
	DataOffset       uint64
	BuffersNeeded    uint32
	BlockDescriptors uint32
	Reserved         [6]uint32
	Checksum         UDIFChecksum
	ChunkCount       uint32
}

// Partition object
type Partition struct {
	udifBlockData

	Name   string
	Chunks []udifBlockChunk

	sr    *io.SectionReader
	cache *lru.Cache[int, []byte] // LRU cache for decompressed chunks
}

type udifBlockChunkType uint32

const (
	ZERO_FILL       udifBlockChunkType = 0x00000000
	UNCOMPRESSED    udifBlockChunkType = 0x00000001
	IGNORED         udifBlockChunkType = 0x00000002 // Sparse (used for Apple_Free)
	COMPRESS_ADC    udifBlockChunkType = 0x80000004
	COMPRESS_ZLIB   udifBlockChunkType = 0x80000005
	COMPRESSS_BZ2   udifBlockChunkType = 0x80000006
	COMPRESSS_LZFSE udifBlockChunkType = 0x80000007
	COMPRESSS_LZMA  udifBlockChunkType = 0x80000008
	COMMENT         udifBlockChunkType = 0x7ffffffe
	LAST_BLOCK      udifBlockChunkType = 0xffffffff
)

func (t udifBlockChunkType) String() string {
	switch t {
	case ZERO_FILL:
		return "ZERO_FILL"
	case UNCOMPRESSED:
		return "UNCOMPRESSED"
	case IGNORED:
		return "IGNORED"
	case COMPRESS_ADC:
		return "COMPRESS_ADC"
	case COMPRESS_ZLIB:
		return "COMPRESS_ZLIB"
	case COMPRESSS_BZ2:
		return "COMPRESSS_BZ2"
	case COMPRESSS_LZFSE:
		return "COMPRESSS_LZFSE"
	case COMPRESSS_LZMA:
		return "COMPRESSS_LZMA"
	case COMMENT:
		return "COMMENT"
	case LAST_BLOCK:
		return "LAST_BLOCK"
	default:
		return fmt.Sprintf("UNKNOWN (%#x)", uint32(t))
	}
}

type udifBlockChunk struct {
	Type             udifBlockChunkType
	Comment          uint32
	DiskOffset       uint64 // Logical chunk offset and length, in sectors. (sector number)
	DiskLength       uint64 // (sector count)
	CompressedOffset uint64 // Compressed offset and length, in bytes.
	CompressedLength uint64
}

func (b *Partition) WriteWithProgress(w *bufio.Writer) error {
	log.Infof("Decompressing DMG block %s", b.Name)

	// initialize progress bar
	p := mpb.New(mpb.WithWidth(80))
	// adding a single bar, which will inherit container's width
	bar := p.Add(int64(len(b.Chunks)),
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

	return b.Write(w, bar)
}

// Write decompresses the chunks for a given block and writes them to supplied bufio.Writer
func (b *Partition) Write(w *bufio.Writer, bar ...*mpb.Bar) error {
	for idx, chunk := range b.Chunks {
		if err := b.validateChunkRange(chunk); err != nil {
			return fmt.Errorf("chunk %d: %w", idx, err)
		}
		if chunk.Type == ZERO_FILL || chunk.Type == IGNORED {
			if err := writeZeros(w, chunk.DiskLength); err != nil {
				return err
			}
		} else {
			data, err := b.decompressChunk(chunk)
			if err != nil {
				return fmt.Errorf("chunk %d: %w", idx, err)
			}
			if _, err := w.Write(data); err != nil {
				return err
			}
		}
		if len(bar) > 0 {
			bar[0].Increment()
		}
	}
	if len(bar) > 0 {
		bar[0].Wait()
	}
	return w.Flush()
}

var _ io.ReaderAt = (*Partition)(nil)

// initCache initializes the LRU cache for decompressed chunks if not already initialized
func (b *Partition) initCache() error {
	if b.cache != nil {
		return nil
	}
	// Use BuffersNeeded from the partition metadata, default to 256 if not set
	cacheSize := int(b.BuffersNeeded)
	if cacheSize == 0 {
		cacheSize = 256
	}
	var err error
	b.cache, err = lru.New[int, []byte](cacheSize)
	return err
}

// findChunkIndex uses binary search to find the chunk containing the given offset
func (b *Partition) findChunkIndex(off int64) int {
	beg := 0
	end := len(b.Chunks) - 1

	for beg <= end {
		mid := (beg + end) / 2
		chk := b.Chunks[mid]
		if off >= int64(chk.DiskOffset) && uint64(off)-chk.DiskOffset < chk.DiskLength {
			return mid
		} else if off < int64(chk.DiskOffset) {
			end = mid - 1
		} else {
			beg = mid + 1
		}
	}
	return -1
}

func (b *Partition) ReadAt(p []byte, off int64) (int, error) {
	if err := b.initCache(); err != nil {
		return 0, fmt.Errorf("failed to initialize cache: %w", err)
	}
	return b.readAt(p, off, b.cache)
}

// readAt shares validation and EOF behavior with DMG.ReadAt. A nil cache disables caching.
func (b *Partition) readAt(p []byte, off int64, cache *lru.Cache[int, []byte]) (n int, err error) {
	if off < 0 {
		return 0, fmt.Errorf("negative read offset: %d", off)
	}
	if len(p) == 0 {
		return 0, nil
	}
	start, size, err := b.logicalRange()
	if err != nil {
		return 0, err
	}
	if uint64(off) >= size {
		return 0, io.EOF
	}
	remaining := min(uint64(len(p)), size-uint64(off))
	absolute := start + uint64(off)
	chunkIdx := b.findChunkIndex(int64(absolute))
	if chunkIdx < 0 {
		return 0, fmt.Errorf("no chunk at offset %d: %w", absolute, io.ErrUnexpectedEOF)
	}
	for remaining > 0 && chunkIdx < len(b.Chunks) {
		chunk := b.Chunks[chunkIdx]
		if chunk.Type == COMMENT || chunk.Type == LAST_BLOCK {
			chunkIdx++
			continue
		}
		if err := b.validateChunkRange(chunk); err != nil {
			return n, err
		}
		if absolute < chunk.DiskOffset || absolute-chunk.DiskOffset >= chunk.DiskLength {
			return n, fmt.Errorf("chunk gap at offset %d: %w", absolute, io.ErrUnexpectedEOF)
		}
		if chunk.Type == ZERO_FILL || chunk.Type == IGNORED {
			count := min(chunk.DiskLength-(absolute-chunk.DiskOffset), remaining)
			clear(p[n : n+int(count)])
			n += int(count)
			absolute += count
			remaining -= count
			chunkIdx++
			continue
		}
		var data []byte
		var found bool
		if cache != nil {
			data, found = cache.Get(chunkIdx)
		}
		if !found {
			data, err = b.decompressChunk(chunk)
			if err != nil {
				return n, fmt.Errorf("chunk %d: %w", chunkIdx, err)
			}
			if cache != nil {
				cache.Add(chunkIdx, data)
			}
		}
		diff := absolute - chunk.DiskOffset
		count := min(uint64(len(data))-diff, remaining)
		n += copy(p[n:], data[diff:diff+count])
		absolute += count
		remaining -= count
		chunkIdx++
	}
	if remaining != 0 {
		return n, io.ErrUnexpectedEOF
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

var _ io.Reader = (*Partition)(nil)

func (b *Partition) Read(p []byte) (n int, err error) {

	return n, err
}

// DecompressChunk writes at most the declared output length plus one detection byte.
func (chunk *udifBlockChunk) DecompressChunk(r *io.SectionReader, in []byte, out *bytes.Buffer) (int, error) {
	if chunk.Type == COMMENT || chunk.Type == LAST_BLOCK {
		return 0, nil
	}
	if err := chunk.validateInput(r); err != nil {
		return 0, err
	}
	if chunk.DiskLength > maxDecodedChunkSize {
		return 0, fmt.Errorf("chunk output length %d out of range", chunk.DiskLength)
	}
	if chunk.Type == ZERO_FILL || chunk.Type == IGNORED {
		return 0, writeZeros(out, chunk.DiskLength)
	}
	if chunk.Type == UNCOMPRESSED && chunk.CompressedLength != chunk.DiskLength {
		return 0, fmt.Errorf("raw chunk length %d does not match %d", chunk.CompressedLength, chunk.DiskLength)
	}
	if uint64(cap(in)) < chunk.CompressedLength {
		return 0, fmt.Errorf("compressed input buffer is too small")
	}
	in = in[:chunk.CompressedLength]
	if _, err := r.ReadAt(in, int64(chunk.CompressedOffset)); err != nil {
		return 0, err
	}
	var reader io.Reader
	switch chunk.Type {
	case UNCOMPRESSED:
		reader = bytes.NewReader(in)
	case COMPRESS_ADC, COMPRESSS_LZFSE:
		decoded := make([]byte, int(chunk.DiskLength)+1)
		var n int
		var err error
		if chunk.Type == COMPRESS_ADC {
			n, err = adc.DecompressADCInto(in, decoded)
		} else {
			n = lzfse.DecodeBufferInto(in, decoded)
		}
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(decoded[:n])
	case COMPRESS_ZLIB:
		zr, err := zlib.NewReader(bytes.NewReader(in))
		if err != nil {
			return 0, err
		}
		defer zr.Close()
		reader = zr
	case COMPRESSS_BZ2:
		reader = bzip2.NewReader(bytes.NewReader(in))
	case COMPRESSS_LZMA:
		var err error
		reader, err = newLZMAReader(in)
		if err != nil {
			return 0, err
		}
	default:
		return 0, fmt.Errorf("chunk has unsupported compression type: %#x", chunk.Type)
	}
	n, err := io.Copy(out, io.LimitReader(reader, int64(chunk.DiskLength)+1))
	if err != nil {
		return 0, err
	}
	if uint64(n) > chunk.DiskLength {
		return 0, fmt.Errorf("chunk output exceeds declared length %d", chunk.DiskLength)
	}
	if uint64(n) < chunk.DiskLength {
		return 0, fmt.Errorf("chunk output length %d, expected %d: %w", n, chunk.DiskLength, io.ErrUnexpectedEOF)
	}
	return int(chunk.CompressedLength), nil
}

func writeZeros(w io.Writer, length uint64) error {
	var zeros [zeroBlockSize]byte
	for length > 0 {
		count := min(length, uint64(len(zeros)))
		n, err := w.Write(zeros[:count])
		if err != nil {
			return err
		}
		if uint64(n) != count {
			return io.ErrShortWrite
		}
		length -= count
	}
	return nil
}

func (chunk udifBlockChunk) validateInput(r *io.SectionReader) error {
	if chunk.Type == COMMENT || chunk.Type == LAST_BLOCK || chunk.Type == ZERO_FILL || chunk.Type == IGNORED {
		return nil
	}
	if r == nil || r.Size() < 0 || chunk.CompressedOffset > uint64(r.Size()) || chunk.CompressedLength > uint64(r.Size())-chunk.CompressedOffset || chunk.CompressedLength > maxCompressedChunkSize {
		return fmt.Errorf("chunk compressed range %d+%d out of bounds", chunk.CompressedOffset, chunk.CompressedLength)
	}
	return nil
}

func (b *Partition) logicalRange() (start, size uint64, err error) {
	if b.StartSector > math.MaxInt64/sectorSize || b.SectorCount > math.MaxInt64/sectorSize-b.StartSector {
		return 0, 0, fmt.Errorf("partition sector range %d+%d out of bounds", b.StartSector, b.SectorCount)
	}
	return b.StartSector * sectorSize, b.SectorCount * sectorSize, nil
}

func (b *Partition) validateChunkRange(chunk udifBlockChunk) error {
	if chunk.Type == COMMENT || chunk.Type == LAST_BLOCK {
		return nil
	}
	start, size, err := b.logicalRange()
	if err != nil {
		return err
	}
	if chunk.DiskOffset < start || chunk.DiskOffset-start > size || chunk.DiskLength > size-(chunk.DiskOffset-start) {
		return fmt.Errorf("chunk logical range %d+%d outside partition", chunk.DiskOffset, chunk.DiskLength)
	}
	if chunk.Type != ZERO_FILL && chunk.Type != IGNORED && chunk.DiskLength > maxDecodedChunkSize {
		return fmt.Errorf("chunk output length %d out of range", chunk.DiskLength)
	}
	return chunk.validateInput(b.sr)
}

func (b *Partition) decompressChunk(chunk udifBlockChunk) ([]byte, error) {
	if err := b.validateChunkRange(chunk); err != nil {
		return nil, err
	}
	if chunk.Type == COMMENT || chunk.Type == LAST_BLOCK {
		return nil, nil
	}
	var in []byte
	if chunk.Type != ZERO_FILL && chunk.Type != IGNORED {
		in = make([]byte, chunk.CompressedLength)
	}
	var out bytes.Buffer
	if _, err := chunk.DecompressChunk(b.sr, in, &out); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// Open opens the named file using os.Open and prepares it for use as a dmg.
func Open(name string, c *Config) (*DMG, error) {
	var decrypted string
	if len(c.Password) > 0 || len(c.Key) > 0 {
		if len(c.Password) > 0 {
			var err error
			decrypted, err = DecryptDMGWithPassword(name, c.Password)
			if err != nil {
				return nil, err
			}
		} else if len(c.Key) > 0 {
			var err error
			decrypted, err = DecryptDMGWithKey(name, c.Key)
			if err != nil {
				return nil, err
			}
		}
	}
	var f *os.File
	if len(decrypted) > 0 {
		var err error
		f, err = os.Open(decrypted)
		if err != nil {
			return nil, err
		}
	} else {
		var err error
		f, err = os.Open(name)
		if err != nil {
			return nil, err
		}
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	ff, err := NewDMG(io.NewSectionReader(f, 0, fi.Size()))
	if err != nil {
		f.Close()
		return nil, err
	}
	if len(decrypted) > 0 {
		ff.decrypted = decrypted
	}
	if c != nil {
		ff.config = *c
	}
	if err := ff.Load(); err != nil {
		f.Close()
		return nil, err
	}
	ff.closer = f
	return ff, nil
}

// Close closes the DMG.
// If the DMG was created using NewFile directly instead of Open,
// Close has no effect.
func (d *DMG) Close() error {
	var err error
	if d.closer != nil {
		err = d.closer.Close()
		d.closer = nil
	}
	if d.decrypted != "" {
		// remove temp decrypted file
		err = os.Remove(d.decrypted)
		d.decrypted = ""
	}
	return err
}

func (d *DMG) DecryptedTemp() string {
	return d.decrypted
}

// NewDMG creates a new DMG for accessing a dmg in an underlying reader.
// The dmg is expected to start at position 0 in the ReaderAt.
func NewDMG(sr *io.SectionReader) (*DMG, error) {

	d := new(DMG)
	d.sr = sr

	var encHeader EncryptionHeader
	if err := binary.Read(d.sr, binary.BigEndian, &encHeader); err != nil {
		return nil, fmt.Errorf("failed to read DMG encrypted header: %v", err)
	}

	if string(encHeader.Magic[:]) == EncryptedMagic {
		return nil, ErrEncrypted
	}

	if _, err := d.sr.Seek(int64(-binary.Size(UDIFResourceFile{})), io.SeekEnd); err != nil {
		return nil, fmt.Errorf("failed to seek to DMG footer: %v", err)
	}

	if err := binary.Read(d.sr, binary.BigEndian, &d.Footer); err != nil {
		return nil, fmt.Errorf("failed to read DMG footer: %v", err)
	}

	if d.Footer.Signature.String() != udifRFSignature {
		return nil, fmt.Errorf("found unexpected UDIFResourceFile signure: %s, expected: %s", d.Footer.Signature.String(), udifRFSignature)
	}

	if d.Footer.SectorCount > math.MaxInt64/sectorSize {
		return nil, fmt.Errorf("image sector count %d out of range", d.Footer.SectorCount)
	}
	if d.Footer.DataForkOffset > uint64(sr.Size()) || d.Footer.DataForkLength > uint64(sr.Size())-d.Footer.DataForkOffset {
		return nil, fmt.Errorf("data fork range out of bounds")
	}

	// TODO: parse Code Signnature

	// parse 'plist' data if it exists
	if d.Footer.PlistOffset > 0 && d.Footer.PlistLength > 0 {
		if d.Footer.PlistOffset > uint64(sr.Size()) || d.Footer.PlistLength > uint64(sr.Size())-d.Footer.PlistOffset {
			return nil, fmt.Errorf("plist range out of bounds")
		}
		pr := io.NewSectionReader(sr, int64(d.Footer.PlistOffset), int64(d.Footer.PlistLength))
		if err := plist.NewDecoder(pr).Decode(&d.Plist); err != nil {
			return nil, fmt.Errorf("failed to parse DMG plist data: %w", err)
		}
	} else if d.Footer.RsrcForkOffset > 0 && d.Footer.RsrcForkLength > 0 {
		log.Fatal("Resource fork parsing is not yet implemented.")
	}

	if nsiz, ok := d.Plist.ResourceFork["nsiz"]; ok {
		if err := plist.NewDecoder(bytes.NewReader(nsiz[0].Data)).Decode(&d.Nsiz); err != nil {
			return nil, fmt.Errorf("failed to parse nsiz plist data: %v\n%s", err, string(nsiz[0].Data[:]))
		}
	}

	d.sr.Seek(0, io.SeekStart)

	if blkx, ok := d.Plist.ResourceFork["blkx"]; ok {
		for _, block := range blkx {
			log.Debugf("'blkx' data for block: '%s'", block.Name)
			r := bytes.NewReader(block.Data)

			bdata := Partition{
				Name: block.Name,
			}

			if err := binary.Read(r, binary.BigEndian, &bdata.udifBlockData); err != nil {
				return nil, fmt.Errorf("failed to read UDIFBlockData in block %s: %v", block.Name, err)
			}

			if bdata.udifBlockData.Signature.String() != udifBDSignature {
				return nil, fmt.Errorf("found unexpected UDIFBlockData signure: %s, expected: %s", bdata.udifBlockData.Signature.String(), udifBDSignature)
			}

			if err := d.validatePartition(&bdata); err != nil {
				return nil, fmt.Errorf("block %s: %w", block.Name, err)
			}
			if uint64(bdata.ChunkCount) > uint64(r.Len()/binary.Size(udifBlockChunk{})) {
				return nil, fmt.Errorf("truncated chunk table in block %s: %w", block.Name, io.ErrUnexpectedEOF)
			}
			if bdata.DataOffset > d.Footer.DataForkLength {
				return nil, fmt.Errorf("block %s data offset out of bounds", block.Name)
			}
			bdata.sr = d.sr
			var previousEnd uint64
			for range bdata.ChunkCount {
				var chunk udifBlockChunk
				if err := binary.Read(r, binary.BigEndian, &chunk); err != nil {
					return nil, fmt.Errorf("failed to read chunk in block %s: %w", block.Name, err)
				}
				if chunk.Type == COMMENT || chunk.Type == LAST_BLOCK {
					// Markers carry no logical data, regardless of their other fields.
					continue
				}
				if chunk.DiskOffset > bdata.SectorCount || chunk.DiskLength > bdata.SectorCount-chunk.DiskOffset {
					return nil, fmt.Errorf("chunk sector range outside block %s", block.Name)
				}
				chunk.DiskOffset = (bdata.StartSector + chunk.DiskOffset) * sectorSize
				chunk.DiskLength *= sectorSize
				if chunk.DiskOffset < previousEnd {
					return nil, fmt.Errorf("unordered chunks in block %s", block.Name)
				}
				previousEnd = chunk.DiskOffset + chunk.DiskLength
				if chunk.Type != ZERO_FILL && chunk.Type != IGNORED {
					forkRemaining := d.Footer.DataForkLength - bdata.DataOffset
					if chunk.CompressedOffset > forkRemaining || chunk.CompressedLength > forkRemaining-chunk.CompressedOffset {
						return nil, fmt.Errorf("chunk compressed range outside data fork in block %s", block.Name)
					}
					// Normalize compressed offsets once so all callers use the same reader.
					chunk.CompressedOffset += d.Footer.DataForkOffset + bdata.DataOffset
				}
				if err := bdata.validateChunkRange(chunk); err != nil {
					return nil, fmt.Errorf("block %s: %w", block.Name, err)
				}
				bdata.Chunks = append(bdata.Chunks, chunk)
			}

			d.Partitions = append(d.Partitions, bdata)
		}
	}

	if plstBlocks, ok := d.Plist.ResourceFork["plst"]; ok {
		// TODO: parse plst data (find sample data)
		for _, plst := range plstBlocks {
			log.Debugf("'plst' data for block: '%s'", plst.Name)
		}
	}

	if checksumBlocks, ok := d.Plist.ResourceFork["cSum"]; ok {
		// TODO: parse checksum data (find sample data)
		for _, checksum := range checksumBlocks {
			log.Debugf("'cSum' data for block: '%s'", checksum.Name)
		}
	}

	if sizeBlocks, ok := d.Plist.ResourceFork["size"]; ok {
		// TODO: parse size data (find sample data)
		for _, size := range sizeBlocks {
			log.Debugf("'size' data for block: '%s'", size.Name)
		}
	}

	return d, nil
}

// GetSize returns the size of the DMG data
func (d *DMG) GetSize() uint64 {
	return d.Footer.SectorCount * sectorSize
}

// Partition returns a partition by name
func (d *DMG) Partition(name string) (*Partition, error) {
	for _, block := range d.Partitions {
		if strings.Contains(block.Name, name) {
			return &block, nil
		}
	}
	return nil, fmt.Errorf("block %s not found", name)
}

// Load parses and verifies the GPT
func (d *DMG) Load() error {

	var out bytes.Buffer

	/* Primary GPT Header */
	if block, err := d.Partition("Primary GPT Header"); err == nil {
		if err := d.validatePartition(block); err != nil {
			return err
		}
		for i, chunk := range block.Chunks {
			data, err := block.decompressChunk(chunk)
			if err != nil {
				return fmt.Errorf("failed to decompress chunk %d in block %s: %w", i, block.Name, err)
			}
			out.Write(data)
		}

		var g gpt.GUIDPartitionTable
		if err := binary.Read(bytes.NewReader(out.Bytes()), binary.LittleEndian, &g.Header); err != nil {
			return fmt.Errorf("failed to read %T: %w", g.Header, err)
		}

		if err := g.Header.Verify(); err != nil {
			return fmt.Errorf("failed to verify GPT header: %w", err)
		}

		out.Reset()

		/* Primary GPT Table */
		if block, err := d.Partition("Primary GPT Table"); err == nil {
			if err := d.validatePartition(block); err != nil {
				return err
			}
			for i, chunk := range block.Chunks {
				data, err := block.decompressChunk(chunk)
				if err != nil {
					return fmt.Errorf("failed to decompress chunk %d in block %s: %w", i, block.Name, err)
				}
				out.Write(data)
			}

			if uint64(g.Header.EntriesCount) > uint64(out.Len()/binary.Size(gpt.Partition{})) {
				return fmt.Errorf("GPT entries exceed table length: %w", io.ErrUnexpectedEOF)
			}
			g.Partitions = make([]gpt.Partition, g.Header.EntriesCount)
			if err := binary.Read(bytes.NewReader(out.Bytes()), binary.LittleEndian, &g.Partitions); err != nil {
				return fmt.Errorf("failed to load and verify GPT: %w", err)
			}

			// find first APFS partition
			found := false
			for _, part := range g.Partitions {
				switch part.Type.String() {
				case gpt.None:
				case gpt.HFSPlus:
					fallthrough
				case gpt.Apple_APFS:
					if part.StartingLBA > part.EndingLBA || part.EndingLBA >= d.Footer.SectorCount {
						return fmt.Errorf("GPT partition range out of bounds")
					}
					for i, block := range d.Partitions {
						if block.udifBlockData.StartSector == part.StartingLBA {
							if err := d.validatePartition(&block); err != nil {
								return err
							}
							if block.SectorCount != part.EndingLBA-part.StartingLBA+1 {
								return fmt.Errorf("GPT partition size does not match block map")
							}
							found = true
							d.firstAPFSPartition = i
							cacheSize := max(int(block.BuffersNeeded), 1)
							// setup sector cache
							d.cache, err = lru.NewWithEvict(cacheSize, func(k int, v []byte) {
								log.Warn("evicted item from DMG read cache (maybe we should increase it)")
								d.evictCounter++
							})
							if err != nil {
								return fmt.Errorf("failed to initialize DMG read cache: %w", err)
							}
						}
					}
				default:
					parts := make([]uint16, len(part.PartitionNameUTF16)/binary.Size(uint16(0)))
					if err := binary.Read(bytes.NewReader(part.PartitionNameUTF16[:]), binary.LittleEndian, &parts); err != nil {
						return fmt.Errorf("failed to read partition name: %w", err)
					}
					log.Debugf("skipping partition: %s", string(utf16.Decode(parts)))
				}
			}
			if !found {
				return fmt.Errorf("failed to find Apple_APFS partition in DMG")
			}
		} else {
			return fmt.Errorf("failed to load and verify GPT: %w", err)
		}
	} else {
		log.Debugf("failed to load and verify GPT: %v", err)
	}

	return nil
}

// ReadAt reads relative to the selected APFS partition.
func (d *DMG) ReadAt(buf []byte, off int64) (int, error) {
	if d.firstAPFSPartition < 0 || d.firstAPFSPartition >= len(d.Partitions) {
		return 0, fmt.Errorf("no APFS partition loaded")
	}
	b := &d.Partitions[d.firstAPFSPartition]
	if err := d.validatePartition(b); err != nil {
		return 0, err
	}
	cache := d.cache
	if d.config.DisableCache {
		cache = nil
	}
	return b.readAt(buf, off, cache)
}

func (d *DMG) validatePartition(b *Partition) error {
	if _, _, err := b.logicalRange(); err != nil {
		return err
	}
	if d.Footer.SectorCount > math.MaxInt64/sectorSize || b.StartSector > d.Footer.SectorCount || b.SectorCount > d.Footer.SectorCount-b.StartSector {
		return fmt.Errorf("partition sector range %d+%d outside image", b.StartSector, b.SectorCount)
	}
	return nil
}

// ReadFile extracts a file from the DMG.
func (d *DMG) ReadFile(w *bufio.Writer, off, length int64) error {
	if off < 0 || length < 0 || off > math.MaxInt64-length {
		return fmt.Errorf("invalid file range %d+%d", off, length)
	}
	if _, err := io.CopyN(w, io.NewSectionReader(d, off, length), length); err != nil {
		return err
	}
	return w.Flush()
}
