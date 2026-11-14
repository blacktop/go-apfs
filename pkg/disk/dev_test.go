package disk

import (
	"bufio"
	"bytes"
	"io"
	"testing"
)

type rangeRecorder struct {
	*bytes.Reader
	off, length int64
}

func (r *rangeRecorder) WriteRange(w io.Writer, off, length int64) error {
	r.off, r.length = off, length
	_, err := io.CopyN(w, io.NewSectionReader(r, off, length), length)
	return err
}

func TestGenericReadFileUsesRangeWriter(t *testing.T) {
	rec := &rangeRecorder{Reader: bytes.NewReader([]byte("0123456789"))}
	var out bytes.Buffer
	w := bufio.NewWriter(&out)
	if err := NewGeneric(rec).ReadFile(w, 3, 5); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if rec.off != 3 || rec.length != 5 || out.String() != "34567" {
		t.Fatalf("WriteRange(%d, %d) wrote %q", rec.off, rec.length, out.String())
	}
}
