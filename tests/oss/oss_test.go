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
		skip: false,
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
		skip: false,
	},
	{
		vendor: "azure",
		args: oss.OSSArgs{
			AzureBlob: &oss.AzureBlob{
				ConnectionString: os.Getenv("AZURE_CONNECTION"),
				ContainerName:    os.Getenv("AZURE_CONTAINER"),
			},
		},
		skip: false,
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
		skip: false,
	},
	{
		vendor: "tencent",
		args: oss.OSSArgs{
			TencentCOS: &oss.TencentCOS{
				Region:    os.Getenv("TENCNET_COS_REGION"),
				SecretID:  os.Getenv("TENCNET_COS_SECRET_ID"),
				SecretKey: os.Getenv("TENCNET_COS_SECRET_KEY"),
				Bucket:    os.Getenv("TENCNET_COS_BUCKET"),
			},
		},
		skip: false,
	},
	{
		vendor: "gcs",
		args: oss.OSSArgs{
			GoogleCloudStorage: &oss.GoogleCloudStorage{
				Bucket:         os.Getenv("GCS_BUCKET"),
				CredentialsB64: os.Getenv("GCS_CREDENTIALS"),
			},
		},
		skip: false,
	},
	{
		vendor: "huawei",
		args: oss.OSSArgs{
			HuaweiOBS: &oss.HuaweiOBS{
				Bucket:    os.Getenv("HUAWEI_OBS_BUCKET"),
				AccessKey: os.Getenv("HUAWEI_OBS_ACCESS_KEY"),
				SecretKey: os.Getenv("HUAWEI_OBS_SECRET_KEY"),
				Server:    os.Getenv("HUAWEI_OBS_SERVER"),
			},
		},
		skip: false,
	},
	{
		vendor: "volcengine",
		args: oss.OSSArgs{
			VolcengineTOS: &oss.VolcengineTOS{
				Region:    os.Getenv("VOLCENGINE_TOS_REGION"),
				Endpoint:  os.Getenv("VOLCENGINE_TOS_ENDPOINT"),
				AccessKey: os.Getenv("VOLCENGINE_TOS_ACCESS_KEY"),
				SecretKey: os.Getenv("VOLCENGINE_TOS_SECRET_KEY"),
				Bucket:    os.Getenv("VOLCENGINE_TOS_BUCKET"),
			},
		},
		skip: false,
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
		info := fmt.Sprintf("vendor: %s", c.vendor)
		ossPaths, err := storage.List(prefix)
		assert.Equal(t, 0, len(ossPaths), info)
		assert.Nil(t, err, info)

		exist, err := storage.Exists(key)
		assert.Equal(t, false, exist, info)
		assert.Nil(t, err, info)

		err = storage.Save(key, data)
		assert.Nil(t, err, info)

		rdata, err := storage.Load(key)
		assert.Equal(t, data, rdata, info)
		assert.Nil(t, err, info)

		ossState, err := storage.State(key)
		assert.Equal(t, int64(size), ossState.Size, info)
		assert.Nil(t, err, info)

		exist, err = storage.Exists(key)
		assert.Equal(t, true, exist, info)
		assert.Nil(t, err, info)

		ossPaths, err = storage.List(prefix)
		assert.Equal(t, 1, len(ossPaths), info)
		assert.Nil(t, err, info)

		err = storage.Delete(key)
		assert.Nil(t, err, info)

		exist, err = storage.Exists(key)
		assert.Equal(t, false, exist, info)
		assert.Nil(t, err, info)

		ossPaths, err = storage.List(prefix)
		assert.Equal(t, 0, len(ossPaths), info)
		assert.Nil(t, err, info)
	}
}
