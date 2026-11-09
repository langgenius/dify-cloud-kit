package integration

import (
	"context"
	"fmt"
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
}
