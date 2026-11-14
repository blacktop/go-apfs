package disk

import (
	"bufio"
	"io"
	"os"
)

// Device is a disk device object
type Device interface {
	io.ReaderAt
	io.Closer
	ReadFile(w *bufio.Writer, off, length int64) error
	GetSize() uint64
}

// RangeWriter is implemented by readers that can copy a whole byte range more efficiently than ReadAt
type RangeWriter interface {
	WriteRange(w io.Writer, off, length int64) error
}

type Generic struct {
	io.ReaderAt
	io.Closer

	f    *os.File
	size int64
}

func Open(in string) (Device, error) {
	f, err := os.Open(in)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	g := NewGeneric(f)
	g.f = f
	g.size = fi.Size()
	return g, nil
}

func NewGeneric(r io.ReaderAt) *Generic {
	return &Generic{
		ReaderAt: r,
	}
}

func (g *Generic) Close() error {
	return g.f.Close()
}

func (g *Generic) ReadFile(w *bufio.Writer, off, length int64) error {
	return g.WriteRange(w, off, length)
}

// WriteRange copies length bytes at off to w, delegating to the underlying reader when it is a RangeWriter
func (g *Generic) WriteRange(w io.Writer, off, length int64) error {
	if rw, ok := g.ReaderAt.(RangeWriter); ok {
		return rw.WriteRange(w, off, length)
	}
	sr := io.NewSectionReader(g.ReaderAt, off, length)
	_, err := io.CopyN(w, sr, length)
	return err
}

func (g *Generic) GetSize() uint64 {
	return uint64(g.size)
}
