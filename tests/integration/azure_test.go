package integration

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/google/uuid"
	"github.com/langgenius/dify-cloud-kit/oss"
	"github.com/langgenius/dify-cloud-kit/oss/factory"
)

// Azurite's well-known development account and key (public, documented by
// Microsoft; not a real credential).
const (
	azuriteEndpoint = "http://127.0.0.1:11000/devstoreaccount1"
	azuriteConnStr  = "DefaultEndpointsProtocol=http;" +
		"AccountName=devstoreaccount1;" +
		"AccountKey=Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw==;" +
		"BlobEndpoint=" + azuriteEndpoint + ";"
)

func TestAzureIntegrationStreaming(t *testing.T) {
	requireIntegration(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	container := fmt.Sprintf("cloudkit-it-%s", uuid.NewString())
	objKey := "fixture.bin"
	emptyKey := "empty.bin"

	// Build the store before the container exists: the constructor must not
	// require or create it.
	store, err := factory.Load("azure_blob", oss.OSSArgs{
		AzureBlob: &oss.AzureBlob{
			ConnectionString: azuriteConnStr,
			ContainerName:    container,
		},
	})
	if err != nil {
		t.Fatalf("factory.Load azure_blob: %v", err)
	}
	if !oss.SupportsPrepare(store) {
		t.Fatal("azure_blob does not report preparation support")
	}

	t.Cleanup(func() {
		for _, k := range []string{objKey, emptyKey} {
			if err := store.Delete(k); err != nil {
				t.Logf("cleanup Delete %s: %v", k, err)
			}
		}
	})

	// Prepare is the production entry point for resource creation and must
	// be idempotent.
	if err := oss.Prepare(ctx, store); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	t.Logf("prepared container %s on %s", container, azuriteEndpoint)

	// A second call and concurrent calls must all succeed on the already
	// existing container.
	if err := oss.Prepare(ctx, store); err != nil {
		t.Fatalf("second Prepare: %v", err)
	}
	prepareErrs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		go func() {
			c, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			prepareErrs <- oss.Prepare(c, store)
		}()
	}
	for i := 0; i < 4; i++ {
		if err := <-prepareErrs; err != nil {
			t.Fatalf("concurrent Prepare: %v", err)
		}
	}

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
}

// TestAzureIntegrationGzipContentEncoding uploads a blob whose stored metadata
// carries Content-Encoding: gzip and reads it back through the library.
//
// Azure (and Azurite) return the stored Content-Encoding on GET, so Go's HTTP
// transport transparently gunzips the body whenever it injected
// Accept-Encoding: gzip itself. A whole-object read then yields the
// decompressed payload instead of the stored bytes, and a ranged read yields a
// partial gzip member that fails mid-stream — even though Content-Range looked
// valid. Streaming reads must return raw stored bytes, while legacy Load
// retains the transport's transparent decompression.
func TestAzureIntegrationGzipContentEncoding(t *testing.T) {
	requireIntegration(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	container := fmt.Sprintf("cloudkit-gzip-%s", uuid.NewString())
	key := "gzip.bin"

	store, err := factory.Load("azure_blob", oss.OSSArgs{
		AzureBlob: &oss.AzureBlob{
			ConnectionString: azuriteConnStr,
			ContainerName:    container,
		},
	})
	if err != nil {
		t.Fatalf("factory.Load azure_blob: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Delete(key); err != nil {
			t.Logf("cleanup Delete %s: %v", key, err)
		}
	})
	if err := oss.Prepare(ctx, store); err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	payload := bytes.Repeat([]byte("dify-cloud-kit gzip fixture "), 4096)
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(payload); err != nil {
		t.Fatalf("gzip.Write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip.Close: %v", err)
	}
	stored := buf.Bytes()
	if len(stored) < 64 || len(stored) >= len(payload) {
		t.Fatalf("unexpected compressed size %d (payload %d)", len(stored), len(payload))
	}

	// Upload with the SDK directly: the library's Save has no way to set
	// content headers, and this test is about reading blobs that other
	// writers stored gzip-encoded.
	uploader, err := azblob.NewClientFromConnectionString(azuriteConnStr, nil)
	if err != nil {
		t.Fatalf("NewClientFromConnectionString: %v", err)
	}
	if _, err := uploader.UploadBuffer(ctx, container, key, stored, &azblob.UploadBufferOptions{
		HTTPHeaders: &blob.HTTPHeaders{BlobContentEncoding: to.Ptr("gzip")},
	}); err != nil {
		t.Fatalf("UploadBuffer: %v", err)
	}

	state, err := store.State(key)
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if state.Size != int64(len(stored)) {
		t.Fatalf("State.Size = %d, want stored size %d", state.Size, len(stored))
	}

	readAll := func(t *testing.T, r *oss.ByteRange) []byte {
		t.Helper()
		rc, err := oss.OpenStream(ctx, store, key, r)
		if err != nil {
			t.Fatalf("OpenStream(%+v): %v", r, err)
		}
		defer rc.Close()
		got, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("ReadAll(%+v): %v", r, err)
		}
		return got
	}

	t.Run("Load", func(t *testing.T) {
		got, err := store.Load(key)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("Load returned %d bytes, want %d decompressed bytes", len(got), len(payload))
		}
	})

	for _, tc := range []struct {
		name  string
		r     *oss.ByteRange
		start int
	}{
		{"full stream", nil, 0},
		{"bounded head", &oss.ByteRange{Offset: 0, Length: 16}, 0},
		{"bounded mid", &oss.ByteRange{Offset: 10, Length: 20}, 10},
		{"tail to EOF", &oss.ByteRange{Offset: int64(len(stored) - 8), Length: -1}, len(stored) - 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := stored[tc.start:]
			if tc.r != nil && tc.r.Length > 0 {
				want = stored[tc.start : tc.start+int(tc.r.Length)]
			}
			got := readAll(t, tc.r)
			if !bytes.Equal(got, want) {
				t.Fatalf("read %d bytes, want raw stored slice %d bytes", len(got), len(want))
			}
		})
	}
}
