package integration

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"testing"

	"github.com/langgenius/dify-cloud-kit/oss"
)

const gcsEmulatorEndpoint = "http://127.0.0.1:14443"

func gcsIntegrationArgs(t *testing.T, bucket string) oss.OSSArgs {
	t.Helper()
	// The GCS SDK routes every client created while STORAGE_EMULATOR_HOST
	// is set to the emulator and disables authentication.
	t.Setenv("STORAGE_EMULATOR_HOST", gcsEmulatorEndpoint)
	return oss.OSSArgs{GoogleCloudStorage: &oss.GoogleCloudStorage{Bucket: bucket, Project: "test-project"}}
}

func runGCSGzipPayloadChecks(t *testing.T, ctx context.Context, store oss.OSS) {
	t.Helper()
	t.Run("gzip payload round trip", func(t *testing.T) {
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
	})
}
