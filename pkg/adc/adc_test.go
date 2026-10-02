package adc

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestDecompressADCInto(t *testing.T) {
	for _, tt := range []struct {
		name string
		src  []byte
		size int
		want string
		err  error
	}{
		{"literal", []byte{0x82, 'a', 'b', 'c'}, 3, "abc", nil},
		{"overlapping short reference", []byte{0x80, 'a', 0x04, 0}, 5, "aaaaa", nil},
		{"long reference", []byte{0x81, 'a', 'b', 0x40, 0, 1}, 6, "ababab", nil},
		{"literal overflow", []byte{0x82, 'a', 'b', 'c'}, 2, "", io.ErrShortBuffer},
		{"reference overflow", []byte{0x80, 'a', 0x04, 0}, 4, "a", io.ErrShortBuffer},
		{"truncated literal", []byte{0x82, 'a'}, 3, "", io.ErrUnexpectedEOF},
		{"truncated short reference", []byte{0}, 3, "", io.ErrUnexpectedEOF},
		{"truncated long reference", []byte{0x40, 0}, 4, "", io.ErrUnexpectedEOF},
	} {
		t.Run(tt.name, func(t *testing.T) {
			storage := bytes.Repeat([]byte{0xcc}, tt.size+1)
			n, err := DecompressADCInto(tt.src, storage[:tt.size])
			if !errors.Is(err, tt.err) || n != len(tt.want) || string(storage[:n]) != tt.want {
				t.Fatalf("got (%q, %d, %v), want (%q, %d, %v)", storage[:n], n, err, tt.want, len(tt.want), tt.err)
			}
			if storage[tt.size] != 0xcc {
				t.Fatal("wrote beyond destination")
			}
		})
	}
}

func TestDecompressADCInvalidReference(t *testing.T) {
	n, err := DecompressADCInto([]byte{0, 0}, make([]byte, 3))
	if n != 0 || err == nil || err.Error() != "ADC back-reference distance 1 exceeds decoded length 0" {
		t.Fatalf("got (%d, %v)", n, err)
	}
}

func TestDecompressADC(t *testing.T) {
	// Exercise the maximum expansion per back-reference and the allocating API.
	src := []byte{0x80, 'a', 0x7f, 0, 0}
	if got := DecompressADC(src); !bytes.Equal(got, bytes.Repeat([]byte{'a'}, 68)) {
		t.Fatalf("got %q, want 68 a bytes", got)
	}
	if got := DecompressADC([]byte{0, 0}); got != nil {
		t.Fatalf("invalid reference produced %q", got)
	}
}

func FuzzDecompressADCInto(f *testing.F) {
	f.Add([]byte{0x82, 'a', 'b', 'c'})
	f.Add([]byte{0x80, 'a', 0x7f, 0, 0})
	f.Fuzz(func(t *testing.T, src []byte) {
		dst := make([]byte, 512)
		n, _ := DecompressADCInto(src, dst)
		if n < 0 || n > len(dst) {
			t.Fatalf("invalid output length %d", n)
		}
	})
}
