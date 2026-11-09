package integration

import (
	"context"
	"errors"
	"io"
	"math"
	"testing"
	"time"

	"github.com/langgenius/dify-cloud-kit/oss"
)

// fixtureBytes returns the deterministic object as an in-memory slice.
func fixtureBytes(size int64) []byte {
	data := make([]byte, size)
	for i := int64(0); i < size; i++ {
		data[i] = expectedByte(i)
	}
	return data
}

// runInterfaceChecks drives the store's own plain-object API — Save,
// Exists, State, Load, Delete — against the emulator, complementing the
// streaming suite. It uses its own scratch key for the delete round-trip so
// the shared fixture keys stay intact for the test's cleanup.
func runInterfaceChecks(t *testing.T, store oss.OSS, objKey, emptyKey string) {
	t.Helper()

	if ok, err := store.Exists(objKey); err != nil || !ok {
		t.Fatalf("Exists(%q) = %v, %v; want true", objKey, ok, err)
	}
	st, err := store.State(objKey)
	if err != nil {
		t.Fatalf("State(%q): %v", objKey, err)
	}
	if st.Size != objectSize {
		t.Fatalf("State(%q).Size = %d, want %d", objKey, st.Size, objectSize)
	}
	data, err := store.Load(objKey)
	if err != nil {
		t.Fatalf("Load(%q): %v", objKey, err)
	}
	if int64(len(data)) != objectSize {
		t.Fatalf("Load(%q) returned %d bytes, want %d", objKey, len(data), objectSize)
	}
	for _, off := range []int64{0, 1, 4096, 1 << 20, objectSize - 1} {
		if data[off] != expectedByte(off) {
			t.Fatalf("Load(%q) mismatch at offset %d: got %d, want %d", objKey, off, data[off], expectedByte(off))
		}
	}

	if ok, err := store.Exists(emptyKey); err != nil || !ok {
		t.Fatalf("Exists(%q) = %v, %v; want true", emptyKey, ok, err)
	}
	st, err = store.State(emptyKey)
	if err != nil {
		t.Fatalf("State(%q): %v", emptyKey, err)
	}
	if st.Size != 0 {
		t.Fatalf("State(%q).Size = %d, want 0", emptyKey, st.Size)
	}
	empty, err := store.Load(emptyKey)
	if err != nil {
		t.Fatalf("Load(%q): %v", emptyKey, err)
	}
	if len(empty) != 0 {
		t.Fatalf("Load(%q) returned %d bytes, want 0", emptyKey, len(empty))
	}

	// Save/Delete round-trip on a dedicated key.
	scratchKey := "scratch.bin"
	if err := store.Save(scratchKey, []byte("scratch")); err != nil {
		t.Fatalf("Save(%q): %v", scratchKey, err)
	}
	if ok, err := store.Exists(scratchKey); err != nil || !ok {
		t.Fatalf("Exists(%q) = %v, %v; want true", scratchKey, ok, err)
	}
	if err := store.Delete(scratchKey); err != nil {
		t.Fatalf("Delete(%q): %v", scratchKey, err)
	}
	if ok, err := store.Exists(scratchKey); err != nil || ok {
		t.Fatalf("Exists(%q) after Delete = %v, %v; want false", scratchKey, ok, err)
	}
	if _, err := store.Load(scratchKey); err == nil {
		t.Fatalf("Load(%q) after Delete: expected error", scratchKey)
	}
}

// runStreamingChecks runs the shared streaming-range assertion suite against
// a cloudkit store backed by an emulator. objKey must hold the deterministic
// 16MiB fixture; emptyKey must exist as a zero-byte object.
func runStreamingChecks(t *testing.T, store oss.OSS, objKey, emptyKey string) {
	t.Helper()

	if !oss.SupportsStreaming(store) {
		t.Fatalf("%T does not report streaming support", store)
	}

	// verify opens, streams through a bounded buffer, and closes before
	// returning. It takes the subtest's *testing.T explicitly so a failure
	// reports on the subtest, not the parent.
	verify := func(t *testing.T, key string, r *oss.ByteRange, startOff, want int64) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		rc, err := oss.OpenStream(ctx, store, key, r)
		if err != nil {
			t.Fatalf("OpenStream(%+v): %v", r, err)
		}
		defer rc.Close()
		got, err := readBounded(rc, startOff)
		if err != nil {
			t.Fatalf("%v", err)
		}
		if got != want {
			t.Fatalf("read %d bytes, want %d", got, want)
		}
		t.Logf("verified %d bytes from offset %d", want, startOff)
	}

	t.Run("full", func(t *testing.T) { verify(t, objKey, nil, 0, objectSize) })
	t.Run("zero to EOF", func(t *testing.T) {
		verify(t, objKey, &oss.ByteRange{Offset: 0, Length: -1}, 0, objectSize)
	})
	t.Run("bounded early", func(t *testing.T) {
		verify(t, objKey, &oss.ByteRange{Offset: 2, Length: 4096}, 2, 4096)
	})
	t.Run("bounded mid", func(t *testing.T) {
		verify(t, objKey, &oss.ByteRange{Offset: 1<<20 + 17, Length: 65536}, 1<<20+17, 65536)
	})
	t.Run("tail to EOF", func(t *testing.T) {
		verify(t, objKey, &oss.ByteRange{Offset: objectSize - 123, Length: -1}, objectSize-123, 123)
	})
	t.Run("clamped end", func(t *testing.T) {
		verify(t, objKey, &oss.ByteRange{Offset: objectSize - 8, Length: 100}, objectSize-8, 8)
	})
	t.Run("empty object", func(t *testing.T) { verify(t, emptyKey, nil, 0, 0) })

	t.Run("empty object ranged unsatisfiable", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		_, err := oss.OpenStream(ctx, store, emptyKey, &oss.ByteRange{Offset: 0, Length: 1})
		if !errors.Is(err, oss.ErrRangeNotSatisfiable) {
			t.Fatalf("expected ErrRangeNotSatisfiable, got %v", err)
		}
	})

	t.Run("offset equals size unsatisfiable", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		_, err := oss.OpenStream(ctx, store, objKey, &oss.ByteRange{Offset: objectSize, Length: 1})
		if !errors.Is(err, oss.ErrRangeNotSatisfiable) {
			t.Fatalf("expected ErrRangeNotSatisfiable, got %v", err)
		}
	})

	t.Run("missing key", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if _, err := oss.OpenStream(ctx, store, "no-such-key", nil); err == nil {
			t.Fatal("expected error for missing key")
		}
	})

	t.Run("invalid ranges", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		for _, r := range []oss.ByteRange{
			{Offset: -1, Length: 1},
			{Offset: 0, Length: 0},
			{Offset: 0, Length: -2},
			{Offset: math.MaxInt64, Length: 2}, // overflowing end
		} {
			if _, err := oss.OpenStream(ctx, store, objKey, &r); !errors.Is(err, oss.ErrInvalidRange) {
				t.Fatalf("range=%+v: expected ErrInvalidRange, got %v", r, err)
			}
		}
	})

	t.Run("pre-canceled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := oss.OpenStream(ctx, store, objKey, nil); !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	})

	t.Run("cancel during read", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		rc, err := oss.OpenStream(ctx, store, objKey, nil)
		if err != nil {
			t.Fatalf("OpenStream: %v", err)
		}
		defer rc.Close()

		buf := make([]byte, 32*1024)
		if _, err := io.ReadFull(rc, buf[:1024]); err != nil {
			t.Fatalf("initial read: %v", err)
		}
		cancel()
		var consumed int64 = 1024
		var readErr error
		for consumed < objectSize {
			n, err := rc.Read(buf)
			consumed += int64(n)
			if err != nil {
				readErr = err
				break
			}
		}
		if readErr == nil || errors.Is(readErr, io.EOF) || consumed >= objectSize {
			t.Fatalf("expected non-EOF error before consuming the whole object; consumed %d bytes, err %v", consumed, readErr)
		}
		t.Logf("cancel during read: consumed %d bytes then error %v", consumed, readErr)
	})
}
