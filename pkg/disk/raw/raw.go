package raw

import (
	"bufio"
	"io"
	"os"
)

type Raw struct {
	f *os.File
}

func NewRaw(f *os.File) (*Raw, error) {
	return &Raw{f: f}, nil
}

func (r *Raw) ReadAt(p []byte, off int64) (n int, err error) {
	r.f.Seek(off, io.SeekStart)
	return r.f.Read(p)
}

func (r *Raw) Close() error {
	return r.f.Close()
}

func (r *Raw) ReadFile(w *bufio.Writer, off int64, length int64) error {
	r.f.Seek(off, io.SeekStart)
	_, err := io.CopyN(w, r.f, length)
	return err
}

func (r *Raw) GetSize() uint64 {
	fi, err := r.f.Stat()
	if err != nil {
		return 0
	}
	return uint64(fi.Size())
}
