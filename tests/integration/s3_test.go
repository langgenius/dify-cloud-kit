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

const localstackEndpoint = "http://127.0.0.1:14566"

func TestS3IntegrationStreaming(t *testing.T) {
	requireIntegration(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	bucket := fmt.Sprintf("cloudkit-it-%s", uuid.NewString())
	objKey := "fixture.bin"
	emptyKey := "empty.bin"

	// The legacy constructor ensures the bucket at load time; Prepare then
	// reports it idempotently through the explicit capability.
	store, err := factory.Load("s3", oss.OSSArgs{
		S3: &oss.S3{
			UseAws:           false,
			Endpoint:         localstackEndpoint,
			UsePathStyle:     true,
			AccessKey:        "test",
			SecretKey:        "test",
			Bucket:           bucket,
			Region:           "us-east-1",
			SignatureVersion: "v4",
		},
	})
	if err != nil {
		t.Fatalf("factory.Load s3: %v", err)
	}
	if !oss.SupportsPrepare(store) {
		t.Fatal("s3 does not report preparation support")
	}
	if err := oss.Prepare(ctx, store); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	t.Logf("prepared bucket %s on %s", bucket, localstackEndpoint)

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
