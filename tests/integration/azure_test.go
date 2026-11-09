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
