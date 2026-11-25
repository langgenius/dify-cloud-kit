package integration

import (
	"testing"

	"github.com/langgenius/dify-cloud-kit/oss"
)

const localstackEndpoint = "http://127.0.0.1:14566"

// The legacy S3 constructor ensures the bucket before the explicit Prepare call.
func s3IntegrationArgs(_ *testing.T, bucket string) oss.OSSArgs {
	return oss.OSSArgs{
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
	}
}
