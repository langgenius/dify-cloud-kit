package integration

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/langgenius/dify-cloud-kit/oss"
	"github.com/langgenius/dify-cloud-kit/oss/factory"
)

const gcsEmulatorEndpoint = "http://127.0.0.1:14443"

func TestGCSIntegrationStreaming(t *testing.T) {
	requireIntegration(t)

	// The GCS SDK routes every client created while STORAGE_EMULATOR_HOST
	// is set to the emulator and disables authentication.
	t.Setenv("STORAGE_EMULATOR_HOST", gcsEmulatorEndpoint)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	bucket := fmt.Sprintf("cloudkit-it-%s", uuid.NewString())
	objKey := "fixture.bin"
	emptyKey := "empty.bin"

	store, err := factory.Load("gcs", oss.OSSArgs{
		GoogleCloudStorage: &oss.GoogleCloudStorage{
			Bucket:  bucket,
			Project: "test-project",
		},
	})
	if err != nil {
		t.Fatalf("factory.Load gcs: %v", err)
	}
	if !oss.SupportsPrepare(store) {
		t.Fatal("gcs does not report preparation support")
	}
	if err := oss.Prepare(ctx, store); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	t.Logf("prepared bucket %s on %s", bucket, gcsEmulatorEndpoint)

	t.Run("repeated Prepare", func(t *testing.T) {
		if err := oss.Prepare(ctx, store); err != nil {
			t.Fatalf("second Prepare: %v", err)
		}
	})

	t.Cleanup(func() {
		for _, k := range []string{objKey, emptyKey} {
			if err := store.Delete(k); err != nil {
				t.Logf("cleanup Delete %s: %v", k, err)
			}
		}
	})

	// Upload the fixtures through the library's own Save, then exercise the
	// streaming and plain-object interfaces end to end.
	if err := store.Save(objKey, fixtureBytes(objectSize)); err != nil {
		t.Fatalf("Save fixture: %v", err)
	}
	if err := store.Save(emptyKey, nil); err != nil {
		t.Fatalf("Save empty: %v", err)
	}

	runStreamingChecks(t, store, objKey, emptyKey)
	runInterfaceChecks(t, store, objKey, emptyKey)

	t.Run("gzip payload round trip", func(t *testing.T) {
		runGCSGzipPayloadChecks(t, ctx, store)
	})
}

func runGCSGzipPayloadChecks(t *testing.T, ctx context.Context, store oss.OSS) {
	t.Helper()
	const key = "gzip.bin"
	payload := bytes.Repeat([]byte("dify-cloud-kit gzip fixture"), 128)
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	if _, err := zw.Write(payload); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	stored := compressed.Bytes()

	// Save treats the gzip payload as opaque bytes; it does not set Content-Encoding.
	if err := store.Save(key, stored); err != nil {
		t.Fatalf("Save gzip payload: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Delete(key); err != nil {
			t.Logf("cleanup Delete %s: %v", key, err)
		}
	})
	state, err := store.State(key)
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if state.Size != int64(len(stored)) {
		t.Fatalf("State.Size = %d, want %d stored bytes", state.Size, len(stored))
	}

	for _, tc := range []struct {
		name string
		r    *oss.ByteRange
		want []byte
	}{
		{"full stream", nil, stored},
		{"zero to EOF", &oss.ByteRange{Offset: 0, Length: -1}, stored},
		{"bounded range", &oss.ByteRange{Offset: 2, Length: 4}, stored[2:6]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rc, err := oss.OpenStream(ctx, store, key, tc.r)
			if err != nil {
				t.Fatalf("OpenStream: %v", err)
			}
			defer rc.Close()
			got, err := io.ReadAll(rc)
			if err != nil {
				t.Fatalf("ReadAll: %v", err)
			}
			if !bytes.Equal(got, tc.want) {
				t.Fatalf("got %x, want %x", got, tc.want)
			}
		})
	}
}
