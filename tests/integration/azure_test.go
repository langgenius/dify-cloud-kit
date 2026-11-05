package integration

import (
	"context"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
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
	containerCreated := false

	client, err := azblob.NewClientFromConnectionString(azuriteConnStr, nil)
	if err != nil {
		t.Fatalf("NewClientFromConnectionString: %v", err)
	}

	t.Cleanup(func() {
		if !containerCreated {
			return
		}
		c, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		for _, k := range []string{objKey, emptyKey} {
			if _, err := client.DeleteBlob(c, container, k, nil); err != nil {
				t.Logf("cleanup DeleteBlob %s: %v", k, err)
			}
		}
		if _, err := client.DeleteContainer(c, container, nil); err != nil {
			t.Logf("cleanup DeleteContainer: %v", err)
		}
	})

	if _, err := client.CreateContainer(ctx, container, nil); err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}
	containerCreated = true
	t.Logf("created container %s on %s", container, azuriteEndpoint)

	fixture := writeFixture(t, objectSize)
	f, err := os.Open(fixture)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()
	if _, err := client.UploadStream(ctx, container, objKey, f, nil); err != nil {
		t.Fatalf("UploadStream: %v", err)
	}
	if _, err := client.UploadStream(ctx, container, emptyKey, emptyReader{}, nil); err != nil {
		t.Fatalf("UploadStream empty: %v", err)
	}

	store, err := factory.Load("azure_blob", oss.OSSArgs{
		AzureBlob: &oss.AzureBlob{
			ConnectionString: azuriteConnStr,
			ContainerName:    container,
		},
	})
	if err != nil {
		t.Fatalf("factory.Load azure_blob: %v", err)
	}

	runStreamingChecks(t, store, objKey, emptyKey)
}

// emptyReader is a zero-length io.Reader.
type emptyReader struct{}

func (emptyReader) Read([]byte) (int, error) { return 0, io.EOF }
