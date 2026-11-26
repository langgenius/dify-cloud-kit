package integration

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/langgenius/dify-cloud-kit/oss"
	"github.com/langgenius/dify-cloud-kit/oss/factory"
)

func TestOSSIntegration(t *testing.T) {
	requireIntegration(t)
	providers := []struct {
		name    string
		timeout time.Duration
		args    func(*testing.T, string) oss.OSSArgs
		extra   func(*testing.T, context.Context, oss.OSS)
	}{
		{"s3", 60 * time.Second, s3IntegrationArgs, nil},
		{"azure_blob", 60 * time.Second, azureIntegrationArgs, runAzureConcurrentPrepare},
		{"gcs", 120 * time.Second, gcsIntegrationArgs, runGCSGzipPayloadChecks},
	}
	for _, provider := range providers {
		t.Run(provider.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), provider.timeout)
			defer cancel()
			resource := fmt.Sprintf("cloudkit-it-%s", uuid.NewString())
			store, err := factory.Load(provider.name, provider.args(t, resource))
			if err != nil {
				t.Fatalf("factory.Load: %v", err)
			}
			if !oss.SupportsPrepare(store) {
				t.Fatalf("%T does not report preparation support", store)
			}
			if err := oss.Prepare(ctx, store); err != nil {
				t.Fatalf("Prepare: %v", err)
			}
			t.Logf("prepared %s", resource)
			t.Run("repeated Prepare", func(t *testing.T) {
				if err := oss.Prepare(ctx, store); err != nil {
					t.Fatalf("second Prepare: %v", err)
				}
			})

			const objKey, emptyKey = "fixture.bin", "empty.bin"
			for _, fixture := range []struct {
				key  string
				data []byte
			}{
				{objKey, fixtureBytes(objectSize)},
				{emptyKey, nil},
			} {
				if err := store.Save(fixture.key, fixture.data); err != nil {
					t.Fatalf("Save(%q): %v", fixture.key, err)
				}
				t.Cleanup(func() {
					if err := store.Delete(fixture.key); err != nil {
						t.Logf("cleanup Delete %s: %v", fixture.key, err)
					}
				})
			}
			t.Run("object API", func(t *testing.T) { runInterfaceChecks(t, store, objKey, emptyKey) })
			t.Run("streaming", func(t *testing.T) { runStreamingChecks(t, store, objKey, emptyKey) })
			if provider.extra != nil {
				provider.extra(t, ctx, store)
			}
		})
	}
}

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

	for _, tc := range []struct {
		name         string
		key          string
		size         int64
		checkOffsets []int64
	}{
		{"full object", objKey, objectSize, []int64{0, 1, 4096, 1 << 20, objectSize - 1}},
		{"empty object", emptyKey, 0, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if ok, err := store.Exists(tc.key); err != nil || !ok {
				t.Fatalf("Exists(%q) = %v, %v; want true", tc.key, ok, err)
			}
			st, err := store.State(tc.key)
			if err != nil {
				t.Fatalf("State(%q): %v", tc.key, err)
			}
			if st.Size != tc.size {
				t.Fatalf("State(%q).Size = %d, want %d", tc.key, st.Size, tc.size)
			}
			data, err := store.Load(tc.key)
			if err != nil {
				t.Fatalf("Load(%q): %v", tc.key, err)
			}
			if int64(len(data)) != tc.size {
				t.Fatalf("Load(%q) returned %d bytes, want %d", tc.key, len(data), tc.size)
			}
			for _, off := range tc.checkOffsets {
				if data[off] != expectedByte(off) {
					t.Fatalf("Load(%q) mismatch at offset %d: got %d, want %d", tc.key, off, data[off], expectedByte(off))
				}
			}
		})
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
	verify := func(t *testing.T, key string, r *oss.ByteRange, want int64) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		rc, err := oss.OpenStream(ctx, store, key, r)
		if err != nil {
			t.Fatalf("OpenStream(%+v): %v", r, err)
		}
		defer rc.Close()
		var startOff int64
		if r != nil {
			startOff = r.Offset
		}
		got, err := readBounded(rc, startOff)
		if err != nil {
			t.Fatalf("%v", err)
		}
		if got != want {
			t.Fatalf("read %d bytes, want %d", got, want)
		}
		t.Logf("verified %d bytes from offset %d", want, startOff)
	}

	for _, tc := range []struct {
		name string
		key  string
		r    *oss.ByteRange
		want int64
	}{
		{"full", objKey, nil, objectSize},
		{"zero to EOF", objKey, &oss.ByteRange{Offset: 0, Length: -1}, objectSize},
		{"bounded early", objKey, &oss.ByteRange{Offset: 2, Length: 4096}, 4096},
		{"bounded mid", objKey, &oss.ByteRange{Offset: 1<<20 + 17, Length: 65536}, 65536},
		{"tail to EOF", objKey, &oss.ByteRange{Offset: objectSize - 123, Length: -1}, 123},
		{"clamped end", objKey, &oss.ByteRange{Offset: objectSize - 8, Length: 100}, 8},
		{"single byte", objKey, &oss.ByteRange{Offset: 2, Length: 1}, 1},
		{"last byte bounded", objKey, &oss.ByteRange{Offset: objectSize - 1, Length: 1}, 1},
		{"last byte to EOF", objKey, &oss.ByteRange{Offset: objectSize - 1, Length: -1}, 1},
		{"empty object", emptyKey, nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) { verify(t, tc.key, tc.r, tc.want) })
	}

	for _, tc := range []struct {
		name string
		key  string
		r    *oss.ByteRange
		want error // nil means any error is acceptable.
	}{
		{"empty object ranged unsatisfiable", emptyKey, &oss.ByteRange{Offset: 0, Length: 1}, oss.ErrRangeNotSatisfiable},
		{"offset equals size unsatisfiable", objKey, &oss.ByteRange{Offset: objectSize, Length: 1}, oss.ErrRangeNotSatisfiable},
		{"empty object to EOF unsatisfiable", emptyKey, &oss.ByteRange{Offset: 0, Length: -1}, oss.ErrRangeNotSatisfiable},
		{"offset equals size to EOF unsatisfiable", objKey, &oss.ByteRange{Offset: objectSize, Length: -1}, oss.ErrRangeNotSatisfiable},
		{"missing key", "no-such-key", nil, nil},
		{"negative offset", objKey, &oss.ByteRange{Offset: -1, Length: 1}, oss.ErrInvalidRange},
		{"zero length", objKey, &oss.ByteRange{Offset: 0, Length: 0}, oss.ErrInvalidRange},
		{"invalid negative length", objKey, &oss.ByteRange{Offset: 0, Length: -2}, oss.ErrInvalidRange},
		{"overflowing end", objKey, &oss.ByteRange{Offset: math.MaxInt64, Length: 2}, oss.ErrInvalidRange},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			rc, err := oss.OpenStream(ctx, store, tc.key, tc.r)
			if rc != nil {
				rc.Close()
			}
			if err == nil {
				t.Fatal("expected OpenStream to fail")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("expected %v, got %v", tc.want, err)
			}
		})
	}

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
