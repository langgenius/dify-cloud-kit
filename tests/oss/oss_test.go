package main

import (
	"fmt"
	"log"
	"math/rand"
	"os"
	"testing"
	"time"

	"github.com/langgenius/dify-cloud-kit/oss"
	"github.com/langgenius/dify-cloud-kit/oss/factory"
	"github.com/stretchr/testify/assert"
)

type testArgsCases struct {
	vendor string
	args   oss.OSSArgs
	skip   bool
}

var allCases = []testArgsCases{
	{
		vendor: "local",
		args: oss.OSSArgs{
			Local: &oss.Local{
				Path: "/tmp/dify-oss-tests",
			},
		},
		skip: true,
	},
	{
		vendor: "s3",
		args: oss.OSSArgs{
			S3: &oss.S3{
				UseAws:       true,
				UsePathStyle: true,
				AccessKey:    os.Getenv("AWS_S3_ACCESS_KEY"),
				SecretKey:    os.Getenv("AWS_S3_SECRET_KEY"),
				Bucket:       os.Getenv("AWS_S3_BUCKET"),
				Region:       os.Getenv("AWS_S3_REGION"),
			},
		},
		skip: true,
	},
	{
		vendor: "azure",
		args: oss.OSSArgs{
			AzureBlob: &oss.AzureBlob{
				ConnectionString: os.Getenv("AZURE_CONNECTION"),
				ContainerName:    os.Getenv("AZURE_CONTAINER"),
			},
		},
		skip: true,
	},
	{
		vendor: "aliyun",
		args: oss.OSSArgs{
			AliyunOSS: &oss.AliyunOSS{
				Region:      os.Getenv("ALIYUN_OSS_REGION"),
				Endpoint:    os.Getenv("ALIYUN_OSS_ENDPOINT"),
				AccessKey:   os.Getenv("ALIYUN_OSS_ACCESS_KEY"),
				SecretKey:   os.Getenv("ALIYUN_OSS_SECRET_KEY"),
				AuthVersion: os.Getenv("ALIYUN_OSS_AUTH_VERSION"),
				Path:        os.Getenv("ALIYUN_OSS_PATH"),
				Bucket:      os.Getenv("ALIYUN_OSS_BUCKET"),
			},
		},
	},
}

const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

func randomString(length int) string {
	seededRand := rand.New(rand.NewSource(time.Now().UnixNano()))
	result := make([]byte, length)
	for i := range result {
		result[i] = charset[seededRand.Intn(len(charset))]
	}
	return string(result)
}

func TestAll(t *testing.T) {
	prefix := randomString(5)
	key := fmt.Sprintf("%s/%s", prefix, randomString(10))

	size := 1 * 1024 * 1024
	data := make([]byte, size)

	for _, c := range allCases {
		if c.skip {
			continue
		}
		storage, err := factory.Load(c.vendor, c.args)
		if err != nil {
			log.Fatal(err)
			continue
		}

		ossPaths, err := storage.List(prefix)
		assert.Equal(t, 0, len(ossPaths))
		assert.Nil(t, err)

		exist, err := storage.Exists(key)
		assert.Equal(t, false, exist)
		assert.Nil(t, err)

		err = storage.Save(key, data)
		assert.Nil(t, err)

		rdata, err := storage.Load(key)
		assert.Equal(t, data, rdata)
		assert.Nil(t, err)

		ossState, err := storage.State(key)
		assert.Equal(t, int64(size), ossState.Size)
		assert.Nil(t, err)

		exist, err = storage.Exists(key)
		assert.Equal(t, true, exist)
		assert.Nil(t, err)

		ossPaths, err = storage.List(prefix)
		assert.Equal(t, 1, len(ossPaths))
		assert.Nil(t, err)

		err = storage.Delete(key)
		assert.Nil(t, err)

		exist, err = storage.Exists(key)
		assert.Equal(t, false, exist)
		assert.Nil(t, err)

		ossPaths, err = storage.List(prefix)
		assert.Equal(t, 0, len(ossPaths))
		assert.Nil(t, err)
	}
}
