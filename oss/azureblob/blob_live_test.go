package azureblob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/langgenius/dify-cloud-kit/oss"
)

// TestAzureLiveStreaming is an OPT-IN real-Azure integration test. It only
// READS an existing blob; it performs no cloud writes or deletes.
//
// Required env vars (test skips unless all are set):
//
//	CLOUDKIT_AZURE_LIVE_ACCOUNT   storage account name
//	CLOUDKIT_AZURE_LIVE_CONTAINER container name
//	CLOUDKIT_AZURE_LIVE_KEY       blob key (16MiB fixture: byte i = i % 251)
func TestAzureLiveStreaming(t *testing.T) {
	account := os.Getenv("CLOUDKIT_AZURE_LIVE_ACCOUNT")
	container := os.Getenv("CLOUDKIT_AZURE_LIVE_CONTAINER")
	key := os.Getenv("CLOUDKIT_AZURE_LIVE_KEY")
	if account == "" || container == "" || key == "" {
		t.Skip("set CLOUDKIT_AZURE_LIVE_ACCOUNT, CLOUDKIT_AZURE_LIVE_CONTAINER, CLOUDKIT_AZURE_LIVE_KEY to run")
	}

	cred, err := azidentity.NewAzureCLICredential(nil)
	if err != nil {
		t.Fatalf("NewAzureCLICredential: %v", err)
	}
	client, err := azblob.NewClient("https://"+account+".blob.core.windows.net", cred, nil)
	if err != nil {
		t.Fatalf("azblob.NewClient: %v", err)
	}
	store := &AzureBlobStorage{client: client, containerName: container}

	const size = 16 * 1024 * 1024
	expected := func(pos int64) byte { return byte(pos % 251) }

	if !oss.SupportsStreaming(store) {
		t.Fatal("AzureBlobStorage does not report streaming support")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Use the SDK directly so the size check is bounded by ctx (store.State
	// has no context parameter).
	props, err := client.ServiceClient().NewContainerClient(container).
		NewBlobClient(key).GetProperties(ctx, nil)
	if err != nil {
		t.Fatalf("GetProperties: %v", err)
	}
	if props.ContentLength == nil || *props.ContentLength != size {
		t.Fatalf("ContentLength %v, want %d", props.ContentLength, size)
	}
	t.Logf("blob %s/%s/%s size=%d", account, container, key, *props.ContentLength)

	// readBounded streams rc through a 32KiB buffer, validating every byte
	// against expected(globalOffset+i) starting at startOff. Returns bytes read.
	readBounded := func(rc io.Reader, startOff int64) (int64, error) {
		buf := make([]byte, 32*1024)
		var total int64
		for {
			n, err := rc.Read(buf)
			for i := 0; i < n; i++ {
				pos := startOff + total + int64(i)
				if buf[i] != expected(pos) {
					return total, fmt.Errorf("content mismatch at offset %d: got %d, want %d", pos, buf[i], expected(pos))
				}
			}
			total += int64(n)
			if err == io.EOF {
				return total, nil
			}
			if err != nil {
				return total, err
			}
		}
	}

	// verify opens, reads through a bounded buffer, and closes the reader
	// before returning; t.Cleanup is only a backup for early failures.
	verify := func(name string, r *oss.ByteRange, startOff, want int64) {
		t.Helper()
		c, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		rc, err := oss.OpenStream(c, store, key, r)
		if err != nil {
			cancel()
			t.Fatalf("%s: OpenStream(%+v): %v", name, r, err)
		}
		t.Cleanup(func() {
			rc.Close()
			cancel()
		})
		got, err := readBounded(rc, startOff)
		rc.Close()
		cancel()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got != want {
			t.Fatalf("%s: read %d bytes, want %d", name, got, want)
		}
		t.Logf("%s: verified %d bytes from offset %d", name, want, startOff)
	}

	verify("full", nil, 0, size)
	verify("bounded early", &oss.ByteRange{Offset: 2, Length: 4096}, 2, 4096)
	verify("bounded mid", &oss.ByteRange{Offset: 1<<20 + 17, Length: 65536}, 1<<20+17, 65536)
	verify("tail to EOF", &oss.ByteRange{Offset: size - 123, Length: -1}, size-123, 123)
	verify("zero to EOF", &oss.ByteRange{Offset: 0, Length: -1}, 0, size)
	verify("clamped end", &oss.ByteRange{Offset: size - 8, Length: 100}, size-8, 8)

	// offset == size is unsatisfiable
	_, err = oss.OpenStream(ctx, store, key, &oss.ByteRange{Offset: size, Length: 1})
	if !errors.Is(err, oss.ErrRangeNotSatisfiable) {
		t.Fatalf("offset==size: expected ErrRangeNotSatisfiable, got %v", err)
	}

	// invalid ranges fail before any I/O
	for _, r := range []oss.ByteRange{
		{Offset: -1, Length: 1},
		{Offset: 0, Length: 0},
		{Offset: 0, Length: -2},
		{Offset: math.MaxInt64, Length: 2}, // overflowing end
	} {
		if _, err := oss.OpenStream(ctx, store, key, &r); !errors.Is(err, oss.ErrInvalidRange) {
			t.Fatalf("range=%+v: expected ErrInvalidRange, got %v", r, err)
		}
	}

	// pre-canceled context
	canceled, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	if _, err := oss.OpenStream(canceled, store, key, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	// mid-read cancel: read 1KiB then cancel; a subsequent read must produce
	// a non-EOF error before the whole object is consumed. The context
	// timeout bounds a blocked Read.
	midCtx, midCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer midCancel()
	rc, err := oss.OpenStream(midCtx, store, key, nil)
	if err != nil {
		t.Fatalf("OpenStream (midread): %v", err)
	}
	defer rc.Close()

	buf := make([]byte, 32*1024)
	if _, err := io.ReadFull(rc, buf[:1024]); err != nil {
		t.Fatalf("initial read: %v", err)
	}
	midCancel()
	var consumed int64 = 1024
	var readErr error
	for consumed < size {
		n, err := rc.Read(buf)
		consumed += int64(n)
		if err != nil {
			readErr = err
			break
		}
	}
	if readErr == nil || errors.Is(readErr, io.EOF) || consumed >= size {
		t.Fatalf("expected non-EOF error before consuming the whole object; consumed %d bytes, err %v", consumed, readErr)
	}
	t.Logf("mid-read cancel: consumed %d bytes then error %v", consumed, readErr)
}
