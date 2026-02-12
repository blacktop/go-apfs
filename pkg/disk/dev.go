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
	sr := io.NewSectionReader(g.ReaderAt, off, length)
	_, err := io.CopyN(w, sr, length)
	return err
}

func (g *Generic) GetSize() uint64 {
	return uint64(g.size)
}
