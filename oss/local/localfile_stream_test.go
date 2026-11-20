package local

import (
	"context"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/langgenius/dify-cloud-kit/oss"
)

func newTestStorage(t *testing.T) *LocalStorage {
	t.Helper()
	st, err := NewLocalStorage(oss.OSSArgs{Local: &oss.Local{Path: t.TempDir()}})
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}
	return st.(*LocalStorage)
}

func writeFile(t *testing.T, st *LocalStorage, key string, data []byte) {
	t.Helper()
	if err := st.Save(key, data); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

func rangeReader(t *testing.T, st *LocalStorage, key string, r *oss.ByteRange) io.ReadCloser {
	t.Helper()
	rc, err := st.Range(context.Background(), key, r)
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	t.Cleanup(func() { rc.Close() })
	return rc
}

func TestLocalRangeFull(t *testing.T) {
	st := newTestStorage(t)
	writeFile(t, st, "a/b.bin", []byte("hello world"))

	rc := rangeReader(t, st, "a/b.bin", nil)
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(data) != "hello world" {
		t.Fatalf("got %q", data)
	}
}

func TestLocalRangeFullEmptyFile(t *testing.T) {
	st := newTestStorage(t)
	writeFile(t, st, "empty", nil)

	rc := rangeReader(t, st, "empty", nil)
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(data) != 0 {
		t.Fatalf("expected empty, got %q", data)
	}
}

func TestLocalRangeBounded(t *testing.T) {
	st := newTestStorage(t)
	writeFile(t, st, "f", []byte("0123456789"))

	rc := rangeReader(t, st, "f", &oss.ByteRange{Offset: 2, Length: 4})
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(data) != "2345" {
		t.Fatalf("got %q", data)
	}
}

func TestLocalRangeToEOF(t *testing.T) {
	st := newTestStorage(t)
	writeFile(t, st, "f", []byte("0123456789"))

	rc := rangeReader(t, st, "f", &oss.ByteRange{Offset: 7, Length: -1})
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(data) != "789" {
		t.Fatalf("got %q", data)
	}
}

func TestLocalRangeClampedEnd(t *testing.T) {
	st := newTestStorage(t)
	writeFile(t, st, "f", []byte("0123456789"))

	// Length extends past end of file; only remaining bytes come back.
	rc := rangeReader(t, st, "f", &oss.ByteRange{Offset: 8, Length: 100})
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(data) != "89" {
		t.Fatalf("got %q", data)
	}
}

func TestLocalRangeHugeLengthClamped(t *testing.T) {
	st := newTestStorage(t)
	writeFile(t, st, "f", []byte("0123456789"))

	// Length is a valid inclusive HTTP range (end = MaxInt64); the local
	// clamp must not overflow computing offset+length.
	rc := rangeReader(t, st, "f", &oss.ByteRange{Offset: 1, Length: math.MaxInt64})
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(data) != "123456789" {
		t.Fatalf("got %q", data)
	}
}

func TestLocalRangeMissing(t *testing.T) {
	st := newTestStorage(t)
	if _, err := st.Range(context.Background(), "nope", nil); err == nil {
		t.Fatal("expected error for missing key")
	}
}

func TestLocalRangeUnsatisfiable(t *testing.T) {
	st := newTestStorage(t)
	writeFile(t, st, "f", []byte("0123456789"))
	writeFile(t, st, "empty", nil)

	for _, c := range []struct {
		key string
		r   oss.ByteRange
	}{
		{"f", oss.ByteRange{Offset: 10, Length: 1}},    // offset == size
		{"f", oss.ByteRange{Offset: 10, Length: -1}},   // offset == size to EOF
		{"f", oss.ByteRange{Offset: 11, Length: 1}},    // offset > size
		{"empty", oss.ByteRange{Offset: 0, Length: 1}}, // empty file
		{"empty", oss.ByteRange{Offset: 0, Length: -1}},
	} {
		if _, err := st.Range(context.Background(), c.key, &c.r); !errors.Is(err, oss.ErrRangeNotSatisfiable) {
			t.Fatalf("key=%s range=%+v: expected ErrRangeNotSatisfiable, got %v", c.key, c.r, err)
		}
	}
}

func TestLocalRangeInvalidRange(t *testing.T) {
	st := newTestStorage(t)
	writeFile(t, st, "f", []byte("0123456789"))

	for _, r := range []oss.ByteRange{
		{Offset: -1, Length: 1},
		{Offset: 0, Length: 0},
		{Offset: 0, Length: -2},
	} {
		if _, err := st.Range(context.Background(), "f", &r); !errors.Is(err, oss.ErrInvalidRange) {
			t.Fatalf("range=%+v: expected ErrInvalidRange, got %v", r, err)
		}
	}
}

func TestLocalRangeCanceledBefore(t *testing.T) {
	st := newTestStorage(t)
	writeFile(t, st, "f", []byte("0123456789"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := st.Range(ctx, "f", nil); err == nil {
		t.Fatal("expected context error")
	}
}

func TestLocalRangeCanceledDuringRead(t *testing.T) {
	st := newTestStorage(t)
	writeFile(t, st, "f", []byte("0123456789"))

	ctx, cancel := context.WithCancel(context.Background())
	rc, err := st.Range(ctx, "f", nil)
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	defer rc.Close()

	cancel()
	buf := make([]byte, 4)
	done := make(chan error, 1)
	go func() {
		_, err := rc.Read(buf)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Read did not return after cancel")
	}
}

func TestLocalRangeClosedResource(t *testing.T) {
	st := newTestStorage(t)
	writeFile(t, st, "f", []byte("0123456789"))

	rc, err := st.Range(context.Background(), "f", nil)
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if err := rc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := rc.Read(make([]byte, 1)); err == nil {
		t.Fatal("expected error reading closed resource")
	}
}

func TestLocalRangeLoadStillWorks(t *testing.T) {
	st := newTestStorage(t)
	writeFile(t, st, "f", []byte("payload"))
	data, err := st.Load("f")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if string(data) != "payload" {
		t.Fatalf("got %q", data)
	}
	if _, err := os.Stat(filepath.Join(st.root, "f")); err != nil {
		t.Fatal(err)
	}
}
