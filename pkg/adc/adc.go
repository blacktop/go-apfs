package adc

import (
	"fmt"
	"io"
)

const DECOMP_RATIO = 10

// An ADC back-reference expands at most 67 bytes from three input bytes.
const maxExpansionRatio = 23

// DecompressADC decompresses Apple Data Compression. Invalid data returns nil.
// Call DecompressADCInto when the expected output size is known.
func DecompressADC(src []byte) []byte {
	if len(src) > int(^uint(0)>>1)/maxExpansionRatio {
		return nil
	}
	dst := make([]byte, len(src)*maxExpansionRatio)
	n, err := DecompressADCInto(src, dst)
	if err != nil {
		return nil
	}
	return dst[:n]
}

// DecompressADCInto decompresses src into dst without allocating output memory.
// It returns io.ErrShortBuffer if dst cannot hold the decoded data.
func DecompressADCInto(src, dst []byte) (n int, err error) {
	for len(src) > 0 {
		ctl := src[0]
		src = src[1:]
		if ctl&0x80 != 0 {
			length := int(ctl&0x7f) + 1
			if length > len(src) {
				return n, io.ErrUnexpectedEOF
			}
			if length > len(dst)-n {
				return n, io.ErrShortBuffer
			}
			n += copy(dst[n:], src[:length])
			src = src[length:]
			continue
		}
		var length, distance int
		if ctl&0x40 != 0 {
			if len(src) < 2 {
				return n, io.ErrUnexpectedEOF
			}
			length = int(ctl) - 0x3c
			distance = int(src[0])<<8 | int(src[1])
			src = src[2:]
		} else {
			if len(src) < 1 {
				return n, io.ErrUnexpectedEOF
			}
			length = int(ctl>>2)&0xf + 3
			distance = int(ctl&3)<<8 | int(src[0])
			src = src[1:]
		}
		distance++
		if distance > n {
			return n, fmt.Errorf("ADC back-reference distance %d exceeds decoded length %d", distance, n)
		}
		if length > len(dst)-n {
			return n, io.ErrShortBuffer
		}
		// Back-references can overlap their output, so copy one byte at a time.
		for range length {
			dst[n] = dst[n-distance]
			n++
		}
	}
	return n, nil
}
