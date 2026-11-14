package types

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	lzfse "github.com/blacktop/lzfse-cgo"
	"github.com/vbauerster/mpb/v7"
	"github.com/vbauerster/mpb/v7/decor"
)

const decmpfsChunkSize = 0x10000

func isStoredLZBitmap(marker byte) bool {
	return marker == 0x06 || marker == 0xFF
}

func (h *DecmpfsDiskHeader) decompressLZBitmapAttr(decomp *bufio.Writer) error {
	size := h.UncompressedSize
	if size > decmpfsChunkSize {
		return fmt.Errorf("embedded CMP_ATTR_LZBITMAP data of %d bytes exceeds one %d-byte chunk", size, decmpfsChunkSize)
	}
	if len(h.AttrBytes) == 0 {
		return fmt.Errorf("CMP_ATTR_LZBITMAP has no embedded data")
	}
	out := h.AttrBytes[1:]
	if isStoredLZBitmap(h.AttrBytes[0]) {
		if uint64(len(out)) != size {
			return fmt.Errorf("CMP_ATTR_LZBITMAP holds %d stored bytes, want %d", len(out), size)
		}
	} else {
		out = make([]byte, size)
		if lzfse.LzBitMapDecompress(h.AttrBytes, out) != 0 {
			return fmt.Errorf("failed to decode CMP_ATTR_LZBITMAP data into %d bytes", size)
		}
	}
	if _, err := decomp.Write(out); err != nil {
		return fmt.Errorf("failed to write CMP_ATTR_LZBITMAP data: %w", err)
	}
	return nil
}

func readLZBitmapBlocks(rsrc *bytes.Reader, size uint64) ([]cmpfRsrcBlock, error) {
	fork := uint64(rsrc.Size())
	var firstOffset uint32
	if err := binary.Read(rsrc, binary.LittleEndian, &firstOffset); err != nil {
		return nil, fmt.Errorf("failed to read CMP_RSRC_LZBITMAP first offset: %v", err)
	}
	if firstOffset == 0 || firstOffset%4 != 0 || uint64(firstOffset) > fork {
		return nil, fmt.Errorf("CMP_RSRC_LZBITMAP offset table of %#x bytes does not fit a %#x-byte fork", firstOffset, fork)
	}
	offsets := make([]uint32, firstOffset/4)
	if _, err := rsrc.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("failed to seek to CMP_RSRC_LZBITMAP offset table: %v", err)
	}
	if err := binary.Read(rsrc, binary.LittleEndian, offsets); err != nil {
		return nil, fmt.Errorf("failed to read CMP_RSRC_LZBITMAP offsets: %v", err)
	}
	blocks := make([]cmpfRsrcBlock, 0, len(offsets)-1)
	for i := range len(offsets) - 1 {
		start, end := offsets[i], offsets[i+1]
		if end <= start || uint64(end) > fork {
			return nil, fmt.Errorf("CMP_RSRC_LZBITMAP block %d spans [%#x, %#x), outside the fork or not increasing", i, start, end)
		}
		blocks = append(blocks, cmpfRsrcBlock{Offset: start, Size: end - start})
	}
	if uint64(len(blocks))*decmpfsChunkSize < size {
		return nil, fmt.Errorf("CMP_RSRC_LZBITMAP has %d blocks, too few for %d bytes", len(blocks), size)
	}
	return blocks, nil
}

func newRsrcProgress(n int, nobar bool) (*mpb.Progress, *mpb.Bar) {
	if nobar {
		return nil, nil
	}
	// initialize progress bar
	p := mpb.New(mpb.WithWidth(80))
	// adding a single bar, which will inherit container's width
	bar := p.Add(int64(n),
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
	return p, bar
}

func finishRsrcProgress(p *mpb.Progress, bar *mpb.Bar) {
	if p == nil {
		return
	}
	bar.Abort(false)
	p.Wait()
}

func decompressLZBitmapRsrc(rsrc *bytes.Reader, decomp *bufio.Writer, size uint64, nobar bool) error {
	blocks, err := readLZBitmapBlocks(rsrc, size)
	if err != nil {
		return err
	}
	var maxBlock uint32
	for _, blk := range blocks {
		maxBlock = max(maxBlock, blk.Size)
	}
	src := make([]byte, maxBlock)
	dst := make([]byte, decmpfsChunkSize)
	p, bar := newRsrcProgress(len(blocks), nobar)
	defer finishRsrcProgress(p, bar)

	var total uint64
	for idx, blk := range blocks {
		if total == size {
			break
		}
		chunk := src[:blk.Size]
		if _, err := rsrc.ReadAt(chunk, int64(blk.Offset)); err != nil {
			return fmt.Errorf("failed to read CMP_RSRC_LZBITMAP block %d: %w", idx, err)
		}
		want := min(size-total, decmpfsChunkSize)
		var out []byte
		if isStoredLZBitmap(chunk[0]) {
			payload := chunk[1:]
			if uint64(len(payload)) < want {
				return fmt.Errorf("CMP_RSRC_LZBITMAP stored block %d holds %d bytes, want %d", idx, len(payload), want)
			}
			out = payload[:want]
		} else {
			if lzfse.LzBitMapDecompress(chunk, dst[:want]) != 0 {
				return fmt.Errorf("failed to decode CMP_RSRC_LZBITMAP block %d into %d bytes", idx, want)
			}
			out = dst[:want]
		}
		if _, err := decomp.Write(out); err != nil {
			return fmt.Errorf("failed to write CMP_RSRC_LZBITMAP block %d: %w", idx, err)
		}
		total += uint64(len(out))
		if !nobar {
			bar.Increment()
		}
	}
	if total != size {
		return fmt.Errorf("CMP_RSRC_LZBITMAP decompressed %d bytes, want %d", total, size)
	}
	return nil
}
