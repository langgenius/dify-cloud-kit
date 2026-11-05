package integration

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/langgenius/dify-cloud-kit/oss"
	"github.com/langgenius/dify-cloud-kit/oss/factory"
)

const localstackEndpoint = "http://127.0.0.1:14566"

func newLocalstackS3Client() *s3.Client {
	return s3.New(s3.Options{
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider("test", "test", ""),
		BaseEndpoint: aws.String(localstackEndpoint),
		UsePathStyle: true,
	})
}

func TestS3IntegrationStreaming(t *testing.T) {
	requireIntegration(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	bucket := fmt.Sprintf("cloudkit-it-%s", uuid.NewString())
	objKey := "fixture.bin"
	emptyKey := "empty.bin"
	bucketCreated := false
	client := newLocalstackS3Client()

	t.Cleanup(func() {
		if !bucketCreated {
			return
		}
		c, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		for _, k := range []string{objKey, emptyKey} {
			if _, err := client.DeleteObject(c, &s3.DeleteObjectInput{
				Bucket: aws.String(bucket), Key: aws.String(k),
			}); err != nil {
				t.Logf("cleanup DeleteObject %s: %v", k, err)
			}
		}
		if _, err := client.DeleteBucket(c, &s3.DeleteBucketInput{
			Bucket: aws.String(bucket),
		}); err != nil {
			t.Logf("cleanup DeleteBucket: %v", err)
		}
	})

	if _, err := client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(bucket),
	}); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	bucketCreated = true
	t.Logf("created bucket %s on %s", bucket, localstackEndpoint)

	fixture := writeFixture(t, objectSize)
	f, err := os.Open(fixture)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()
	if _, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(bucket),
		Key:           aws.String(objKey),
		Body:          f,
		ContentLength: aws.Int64(objectSize),
	}); err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	if _, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(bucket),
		Key:           aws.String(emptyKey),
		Body:          bytes.NewReader(nil),
		ContentLength: aws.Int64(0),
	}); err != nil {
		t.Fatalf("PutObject empty: %v", err)
	}

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

	runStreamingChecks(t, store, objKey, emptyKey)
}
