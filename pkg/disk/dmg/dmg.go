package dmg

import (
	"bufio"
	"bytes"
	"compress/bzip2"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"unicode/utf16"

	"github.com/apex/log"
	"github.com/blacktop/go-apfs/pkg/adc"
	"github.com/blacktop/go-apfs/pkg/disk/gpt"
	"github.com/blacktop/go-apfs/types"
	"github.com/blacktop/go-plist"
	"github.com/fatih/color"

	lzfse "github.com/blacktop/lzfse-cgo"
	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/ulikunitz/xz"
	"github.com/ulikunitz/xz/lzma"
	"github.com/vbauerster/mpb/v7"
	"github.com/vbauerster/mpb/v7/decor"
)

// Buffer pools to reduce allocations
var (
	// Pool for bytes.Buffer used in decompression output
	bufferPool = sync.Pool{
		New: func() any {
			return new(bytes.Buffer)
		},
	}

	// Pool for input byte slices (compressed data) - keyed by size buckets
	// Common chunk sizes: 64KB, 128KB, 256KB, 512KB, 1MB
	inputBufferPool64K = sync.Pool{
		New: func() any {
			b := make([]byte, 64*1024)
			return &b
		},
	}
	inputBufferPool128K = sync.Pool{
		New: func() any {
			b := make([]byte, 128*1024)
			return &b
		},
	}
	inputBufferPool256K = sync.Pool{
		New: func() any {
			b := make([]byte, 256*1024)
			return &b
		},
	}
	inputBufferPool512K = sync.Pool{
		New: func() any {
			b := make([]byte, 512*1024)
			return &b
		},
	}
	inputBufferPool1M = sync.Pool{
		New: func() any {
			b := make([]byte, 1024*1024)
			return &b
		},
	}

	// Pool for bytes.Reader used as zlib input
	bytesReaderPool = sync.Pool{
		New: func() any {
			return bytes.NewReader(nil)
		},
	}
)

// getInputBuffer returns a buffer from the appropriate pool based on size
func getInputBuffer(size uint64) (*[]byte, *sync.Pool) {
	switch {
	case size <= 64*1024:
		buf := inputBufferPool64K.Get().(*[]byte)
		return buf, &inputBufferPool64K
	case size <= 128*1024:
		buf := inputBufferPool128K.Get().(*[]byte)
		return buf, &inputBufferPool128K
	case size <= 256*1024:
		buf := inputBufferPool256K.Get().(*[]byte)
		return buf, &inputBufferPool256K
	case size <= 512*1024:
		buf := inputBufferPool512K.Get().(*[]byte)
		return buf, &inputBufferPool512K
	case size <= 1024*1024:
		buf := inputBufferPool1M.Get().(*[]byte)
		return buf, &inputBufferPool1M
	default:
		// For larger sizes, just allocate
		buf := make([]byte, size)
		return &buf, nil
	}
}

// xzMagic is the 6-byte header for XZ streams (\xFD7zXZ\x00).
// DMG block maps label both raw LZMA1 and XZ/LZMA2 as type 0x80000008,
// so we sniff the magic to pick the right decompressor.
var xzMagic = []byte{0xFD, 0x37, 0x7A, 0x58, 0x5A, 0x00}

func newLZMAReader(data []byte) (io.Reader, error) {
	if len(data) >= 6 && bytes.Equal(data[:6], xzMagic) {
		return xz.NewReader(bytes.NewReader(data))
	}
	return lzma.NewReader(bytes.NewReader(data))
}

const (
	sectorSize = 0x200
	blockSize  = 0xc8000
)

var ErrEncrypted = errors.New("DMG is encrypted")

var diskReadColor = color.New(color.Faint, color.FgWhite).SprintfFunc()

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

	firstAPFSPartition  int
	apfsPartitionOffset uint64
	apfsPartitionSize   uint64
	maxChunkSize        int

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
		return fmt.Sprintf("UNKNOWN (%#x)", t)
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

func (b *Partition) maxChunkSize() int {
	var max int
	for _, chunk := range b.Chunks {
		if max < int(chunk.CompressedLength) {
			max = int(chunk.CompressedLength)
		}
	}
	return max
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
	var n int
	var total int
	var err error

	buff := make([]byte, 0, b.maxChunkSize())

	for idx, chunk := range b.Chunks {
		// TODO: verify chunk (size not greater than block etc)
		switch chunk.Type {
		case ZERO_FILL, IGNORED, COMMENT:
			n, err = w.Write(make([]byte, chunk.DiskLength))
			if err != nil {
				return err
			}
			total += n
			log.Debugf(diskReadColor("%d) Wrote %#x bytes of %s data (output size: %#x)", idx, n, chunk.Type, total))
		case UNCOMPRESSED:
			buff = buff[:chunk.CompressedLength]
			pos, _ := b.sr.Seek(0, io.SeekCurrent)
			pos += int64(chunk.CompressedOffset)
			_, err = b.sr.ReadAt(buff, int64(chunk.CompressedOffset))
			if err != nil {
				return err
			}
			n, err = w.Write(buff)
			if err != nil {
				return err
			}
			total += n
			log.Debugf(diskReadColor("%d) From %#x Wrote %#x bytes of %s data (output size: %#x)", idx, pos, n, chunk.Type, total))
		case COMPRESS_ADC:
			buff = buff[:chunk.CompressedLength]
			_, err = b.sr.ReadAt(buff, int64(chunk.CompressedOffset))
			if err != nil {
				return err
			}
			n, err = w.Write(adc.DecompressADC(buff))
			if err != nil {
				return err
			}
			total += n
			log.Debugf(diskReadColor("%d) Wrote %#x bytes of %s data (output size: %#x)", idx, n, chunk.Type, total))
		case COMPRESS_ZLIB:
			buff = buff[:chunk.CompressedLength]
			pos, _ := b.sr.Seek(0, io.SeekCurrent)
			pos += int64(chunk.CompressedOffset)
			_, err = b.sr.ReadAt(buff, int64(chunk.CompressedOffset))
			if err != nil {
				return err
			}
			r, err := zlib.NewReader(bytes.NewReader(buff))
			if err != nil {
				return err
			}
			n, err := w.ReadFrom(r)
			if err != nil {
				return err
			}
			r.Close()
			total += int(n)
			log.Debugf(diskReadColor("%d) From %#x -> Wrote %#x bytes of %s data (output size: %#x)", idx, pos, n, chunk.Type, total))
		case COMPRESSS_BZ2:
			buff = buff[:chunk.CompressedLength]
			if _, err := b.sr.ReadAt(buff, int64(chunk.CompressedOffset)); err != nil {
				return err
			}
			n, err := w.ReadFrom(bzip2.NewReader(bytes.NewReader(buff)))
			if err != nil {
				return err
			}
			total += int(n)
			log.Debugf(diskReadColor("%d) Wrote %#x bytes of %s data (output size: %#x)", idx, n, chunk.Type, total))
		case COMPRESSS_LZFSE:
			buff = buff[:chunk.CompressedLength]
			if _, err := b.sr.ReadAt(buff, int64(chunk.CompressedOffset)); err != nil {
				return err
			}
			n, err = w.Write(lzfse.DecodeBuffer(buff))
			if err != nil {
				return err
			}
			total += n
			log.Debugf(diskReadColor("%d) Wrote %#x bytes of %s data (output size: %#x)", idx, n, chunk.Type, total))
		case COMPRESSS_LZMA:
			buff = buff[:chunk.CompressedLength]
			if _, err := b.sr.ReadAt(buff, int64(chunk.CompressedOffset)); err != nil {
				return err
			}
			lzmaReader, err := newLZMAReader(buff)
			if err != nil {
				return fmt.Errorf("failed to create LZMA reader: %w", err)
			}
			var decompressed bytes.Buffer
			if _, err := decompressed.ReadFrom(lzmaReader); err != nil {
				return fmt.Errorf("failed to decompress LZMA data: %w", err)
			}
			n, err = w.Write(decompressed.Bytes())
			if err != nil {
				return err
			}
			total += n
			log.Debugf(diskReadColor("%d) Wrote %#x bytes of %s data (output size: %#x)", idx, n, chunk.Type, total))
		case LAST_BLOCK:
			if err := w.Flush(); err != nil {
				return err
			}
			log.Debugf(diskReadColor("%d) Wrote %#x bytes of %s data (output size: %#x)", idx, n, chunk.Type, total))
		default:
			return fmt.Errorf("chunk has unsupported compression type: %#x", chunk.Type)
		}
		if len(bar) > 0 {
			bar[0].Increment()
		}
	}
	// wait for progress bar to complete and flush
	if len(bar) > 0 {
		bar[0].Wait()
	}

	return nil
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
		if off >= int64(chk.DiskOffset) && off < int64(chk.DiskOffset+chk.DiskLength) {
			return mid
		} else if off < int64(chk.DiskOffset) {
			end = mid - 1
		} else {
			beg = mid + 1
		}
	}
	return -1
}

func (b *Partition) ReadAt(p []byte, off int64) (n int, err error) {
	// Initialize cache on first use
	if err := b.initCache(); err != nil {
		return 0, fmt.Errorf("failed to initialize cache: %w", err)
	}

	// Adjust offset to absolute file offset by adding partition start
	// Chunks use absolute file offsets, but ReadAt receives partition-relative offsets
	partitionStart := int64(b.StartSector) * 512
	off += partitionStart

	length := int64(len(p))

	// Find starting chunk using binary search
	chunkIdx := b.findChunkIndex(off)
	if chunkIdx < 0 {
		return 0, io.ErrUnexpectedEOF
	}

	for length > 0 && chunkIdx < len(b.Chunks) {
		chk := b.Chunks[chunkIdx]

		// Skip non-data chunks
		if chk.Type == LAST_BLOCK || chk.Type == COMMENT {
			chunkIdx++
			continue
		}

		diff := max(off-int64(chk.DiskOffset), 0)

		// Get decompressed data from cache or decompress
		var data []byte
		if cached, found := b.cache.Get(chunkIdx); found {
			data = cached
		} else {
			// Get output buffer from pool
			buf := bufferPool.Get().(*bytes.Buffer)
			buf.Reset()
			// Pre-size buffer to expected decompressed size to avoid growth
			if int64(buf.Cap()) < int64(chk.DiskLength) {
				buf.Grow(int(chk.DiskLength))
			}

			// Get input buffer from pool
			inBufPtr, inPool := getInputBuffer(chk.CompressedLength)
			inBuf := (*inBufPtr)[:chk.CompressedLength]

			if _, err = chk.DecompressChunk(b.sr, inBuf, buf); err != nil {
				if inPool != nil {
					inPool.Put(inBufPtr)
				}
				bufferPool.Put(buf)
				return n, err
			}

			// Return input buffer to pool
			if inPool != nil {
				inPool.Put(inBufPtr)
			}

			// Store a copy in cache
			cacheCopy := make([]byte, buf.Len())
			copy(cacheCopy, buf.Bytes())
			b.cache.Add(chunkIdx, cacheCopy)
			data = cacheCopy

			// Return output buffer to pool
			bufferPool.Put(buf)
		}

		size := int64(len(data)) - diff
		if size <= 0 {
			chunkIdx++
			continue
		}
		if length < size {
			size = length
		}

		copied := copy(p, data[diff:diff+size])
		n += copied
		p = p[copied:]
		off += int64(copied)
		length -= int64(copied)
		chunkIdx++
	}

	if len(p) > 0 {
		err = io.ErrUnexpectedEOF
	}

	return n, err
}

var _ io.Reader = (*Partition)(nil)

func (b *Partition) Read(p []byte) (n int, err error) {

	return n, err
}

// DecompressChunk decompresses a given chunk and writes it to supplied bufio.Writer
func (chunk *udifBlockChunk) DecompressChunk(r *io.SectionReader, in []byte, out *bytes.Buffer) (n int, err error) {
	var nn int64
	switch chunk.Type {
	case ZERO_FILL, IGNORED, COMMENT:
		if n, err = out.Write(make([]byte, chunk.DiskLength)); err != nil {
			return -1, fmt.Errorf("failed to write ZERO_FILL data")
		}
		log.Debugf(diskReadColor("Wrote %#x bytes of %s data", n, chunk.Type))
	case UNCOMPRESSED:
		in = in[:chunk.CompressedLength]
		_, err = r.ReadAt(in, int64(chunk.CompressedOffset))
		if err != nil {
			return -1, fmt.Errorf("failed to read %s data", chunk.Type)
		}
		n, err = out.Write(in)
		if err != nil {
			return -1, fmt.Errorf("failed to write %s data", chunk.Type)
		}
		log.Debugf(diskReadColor("Wrote %#x bytes of %s data", n, chunk.Type))
	case COMPRESS_ADC:
		in = in[:chunk.CompressedLength]
		if _, err = r.ReadAt(in, int64(chunk.CompressedOffset)); err != nil {
			return
		}
		if n, err = out.Write(adc.DecompressADC(in)); err != nil {
			return -1, fmt.Errorf("failed to write COMPRESS_ADC data")
		}
		log.Debugf(diskReadColor("Wrote %#x bytes of %s data", n, chunk.Type))
	case COMPRESS_ZLIB:
		in = in[:chunk.CompressedLength]
		if _, err = r.ReadAt(in, int64(chunk.CompressedOffset)); err != nil {
			return
		}
		// Get bytes.Reader from pool and reset it
		br := bytesReaderPool.Get().(*bytes.Reader)
		br.Reset(in)
		zr, err := zlib.NewReader(br)
		if err != nil {
			bytesReaderPool.Put(br)
			return -1, fmt.Errorf("failed to create zlib reader")
		}
		if nn, err = out.ReadFrom(zr); err != nil {
			zr.Close()
			bytesReaderPool.Put(br)
			return -1, fmt.Errorf("failed to write COMPRESS_ZLIB data")
		}
		zr.Close()
		bytesReaderPool.Put(br)
		n = int(nn)
		log.Debugf(diskReadColor("Wrote %#x bytes of %s data", n, chunk.Type))
	case COMPRESSS_BZ2:
		in = in[:chunk.CompressedLength]
		if _, err = r.ReadAt(in, int64(chunk.CompressedOffset)); err != nil {
			return
		}
		if nn, err = out.ReadFrom(bzip2.NewReader(bytes.NewReader(in))); err != nil {
			return -1, fmt.Errorf("failed to write COMPRESSS_BZ2 data")
		}
		n = int(nn)
		log.Debugf(diskReadColor("Wrote %#x bytes of %s data", n, chunk.Type))
	case COMPRESSS_LZFSE:
		in = in[:chunk.CompressedLength]
		if _, err = r.ReadAt(in, int64(chunk.CompressedOffset)); err != nil {
			return
		}
		if n, err = out.Write(lzfse.DecodeBuffer(in)); err != nil {
			return -1, fmt.Errorf("failed to write COMPRESSS_LZFSE data")
		}
		log.Debugf(diskReadColor("Wrote %#x bytes of %s data", n, chunk.Type))
	case COMPRESSS_LZMA:
		in = in[:chunk.CompressedLength]
		if _, err = r.ReadAt(in, int64(chunk.CompressedOffset)); err != nil {
			return
		}
		lzmaReader, err := newLZMAReader(in)
		if err != nil {
			return -1, fmt.Errorf("failed to create LZMA reader: %w", err)
		}
		if nn, err = out.ReadFrom(lzmaReader); err != nil {
			return -1, fmt.Errorf("failed to write COMPRESSS_LZMA data: %w", err)
		}
		n = int(nn)
		log.Debugf(diskReadColor("Wrote %#x bytes of %s data", n, chunk.Type))
	case LAST_BLOCK:
	default:
		return n, fmt.Errorf("chuck has unsupported compression type: %#x", chunk.Type)
	}

	return int(chunk.CompressedLength), nil
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

	// TODO: parse Code Signnature

	// parse 'plist' data if it exists
	if d.Footer.PlistOffset > 0 && d.Footer.PlistLength > 0 {
		d.sr.Seek(int64(d.Footer.PlistOffset), io.SeekStart)
		pdata := make([]byte, d.Footer.PlistLength)
		if err := binary.Read(d.sr, binary.BigEndian, &pdata); err != nil {
			return nil, fmt.Errorf("failed to read DMG plist data: %v", err)
		}
		if err := plist.NewDecoder(bytes.NewReader(pdata)).Decode(&d.Plist); err != nil {
			return nil, fmt.Errorf("failed to parse DMG plist data: %v\n%s", err, string(pdata[:]))
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

			for range int(bdata.udifBlockData.ChunkCount) {
				var chunk udifBlockChunk
				binary.Read(r, binary.BigEndian, &chunk)
				bdata.Chunks = append(bdata.Chunks, udifBlockChunk{
					Type:             chunk.Type,
					Comment:          chunk.Comment,
					DiskOffset:       (chunk.DiskOffset + bdata.StartSector) * sectorSize,
					DiskLength:       chunk.DiskLength * sectorSize,
					CompressedOffset: chunk.CompressedOffset + bdata.DataOffset,
					CompressedLength: chunk.CompressedLength,
				})
			}

			bdata.sr = io.NewSectionReader(d.sr, int64(d.Footer.DataForkOffset+bdata.DataOffset), int64(bdata.SectorCount)*sectorSize)

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
	dat := make([]byte, 0, blockSize)

	/* Primary GPT Header */
	if block, err := d.Partition("Primary GPT Header"); err == nil {
		for i, chunk := range block.Chunks {
			if _, err := chunk.DecompressChunk(d.sr, dat, &out); err != nil {
				return fmt.Errorf("failed to decompress chunk %d in block %s: %w", i, block.Name, err)
			}
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
			for i, chunk := range block.Chunks {
				if _, err := chunk.DecompressChunk(d.sr, dat, &out); err != nil {
					return fmt.Errorf("failed to decompress chunk %d in block %s: %w", i, block.Name, err)
				}
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
					for i, block := range d.Partitions {
						if block.udifBlockData.StartSector == part.StartingLBA {
							found = true
							d.firstAPFSPartition = i
							d.maxChunkSize = block.maxChunkSize()
							// setup sector cache
							d.cache, err = lru.NewWithEvict(int(block.BuffersNeeded), func(k int, v []byte) {
								log.Warn("evicted item from DMG read cache (maybe we should increase it)")
								d.evictCounter++
							})
							if err != nil {
								return fmt.Errorf("failed to initialize DMG read cache: %w", err)
							}
						}
					}
					// Get partition offset and size
					d.apfsPartitionOffset = part.StartingLBA * sectorSize
					d.apfsPartitionSize = (part.EndingLBA - part.StartingLBA + 1) * sectorSize
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

// ReadAt impliments the io.ReadAt interface requirement of the Device interface
func (d *DMG) ReadAt(buf []byte, off int64) (n int, err error) {

	var (
		rdOffs int64
		rdSize int
	)

	var bw bytes.Buffer

	w := bufio.NewWriter(&bw)

	off += int64(d.apfsPartitionOffset) // map offset from start of Apple_APFS partition
	length := int64(len(buf))

	apfsChunks := d.Partitions[d.firstAPFSPartition].Chunks

	entryIdx := len(apfsChunks)

	beg := 0
	mid := 0
	end := entryIdx - 1

	// binary search through chunks
	for beg <= end {
		mid = (beg + end) / 2
		if off >= int64(apfsChunks[mid].DiskOffset) && off < int64(apfsChunks[mid].DiskOffset+apfsChunks[mid].DiskLength) {
			entryIdx = mid
			break
		} else if off < int64(apfsChunks[mid].DiskOffset) {
			end = mid - 1
		} else {
			beg = mid + 1
		}
	}

	var out bytes.Buffer
	dec := make([]byte, 0, d.maxChunkSize)

	for length > 0 {

		if int(entryIdx) >= len(apfsChunks)-1 {
			return n, fmt.Errorf("entryIdx >= []apfsChunks")
		}

		sect := apfsChunks[entryIdx]

		rdOffs = max(off-int64(sect.DiskOffset), 0)

		if !d.config.DisableCache {
			// check the cache
			if val, found := d.cache.Get(entryIdx); found {
				if _, err = out.Write(val); err != nil {
					return n, fmt.Errorf("failed to write cached chunk data to writer")
				}
			} else {
				if _, err = sect.DecompressChunk(d.sr, dec, &out); err != nil {
					return n, fmt.Errorf("failed to decompressed chunk %d", entryIdx)
				}
				// Cache the decompressed data
				cacheCopy := make([]byte, out.Len())
				copy(cacheCopy, out.Bytes())
				d.cache.Add(entryIdx, cacheCopy)
			}
		} else {
			if _, err = sect.DecompressChunk(d.sr, dec, &out); err != nil {
				return n, fmt.Errorf("failed to decompressed chunk %d", entryIdx)
			}
		}

		if length >= int64(out.Len())-rdOffs {
			if rdSize, err = w.Write(out.Bytes()[rdOffs:]); err != nil {
				return n, fmt.Errorf("failed to write decompressed chunk to output buffer")
			}
		} else {
			if rdSize, err = w.Write(out.Bytes()[rdOffs : rdOffs+length]); err != nil {
				return n, fmt.Errorf("failed to write decompressed chunk to output buffer")
			}
		}

		out.Reset()

		n += rdSize
		length -= int64(rdSize)
		entryIdx++
	}

	w.Flush()

	bw.Read(buf)

	return len(buf), nil
}

// ReadFile extracts a file from the DMG
func (d *DMG) ReadFile(w *bufio.Writer, off, length int64) (err error) {

	var (
		rdOffs int64
		rdSize int
	)

	off += int64(d.apfsPartitionOffset) // map offset from start of Apple_APFS partition

	apfsChunks := d.Partitions[d.firstAPFSPartition].Chunks

	entryIdx := len(apfsChunks)

	beg := 0
	mid := 0
	end := entryIdx - 1

	// binary search through chunks
	for beg <= end {
		mid = (beg + end) / 2
		if off >= int64(apfsChunks[mid].DiskOffset) && off < int64(apfsChunks[mid].DiskOffset+apfsChunks[mid].DiskLength) {
			entryIdx = mid
			break
		} else if off < int64(apfsChunks[mid].DiskOffset) {
			end = mid - 1
		} else {
			beg = mid + 1
		}
	}

	var out bytes.Buffer
	dec := make([]byte, 0, d.maxChunkSize)

	for length > 0 {
		if int(entryIdx) >= len(apfsChunks)-1 {
			return fmt.Errorf("entryIdx >= []apfsChunks")
		}

		sect := apfsChunks[entryIdx]

		rdOffs = max(off-int64(sect.DiskOffset), 0)

		if _, err = sect.DecompressChunk(d.sr, dec, &out); err != nil {
			return fmt.Errorf("failed to decompressed chunk %d", entryIdx)
		}

		if length >= int64(out.Len()) {
			if rdSize, err = w.Write(out.Bytes()[rdOffs:]); err != nil {
				return fmt.Errorf("failed to write decompressed chunk to output buffer")
			}
		} else {
			if rdSize, err = w.Write(out.Bytes()[rdOffs : rdOffs+length]); err != nil {
				return fmt.Errorf("failed to write decompressed chunk to output buffer")
			}
		}

		out.Reset()

		length -= int64(rdSize)
		entryIdx++
	}

	w.Flush()

	return nil
}
