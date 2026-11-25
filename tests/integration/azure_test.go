package integration

import (
	"context"
	"testing"

	"github.com/langgenius/dify-cloud-kit/oss"
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

func azureIntegrationArgs(_ *testing.T, container string) oss.OSSArgs {
	return oss.OSSArgs{AzureBlob: &oss.AzureBlob{ConnectionString: azuriteConnStr, ContainerName: container}}
}

func runAzureConcurrentPrepare(t *testing.T, ctx context.Context, store oss.OSS) {
	t.Helper()
	t.Run("concurrent Prepare", func(t *testing.T) {
		prepareErrs := make(chan error, 4)
		for i := 0; i < 4; i++ {
			go func() { prepareErrs <- oss.Prepare(ctx, store) }()
		}
		for i := 0; i < 4; i++ {
			if err := <-prepareErrs; err != nil {
				t.Errorf("concurrent Prepare: %v", err)
			}
		}
	})
}
