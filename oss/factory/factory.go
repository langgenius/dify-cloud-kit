package factory

import (
	"github.com/langgenius/dify-cloud-kit/oss"
	"github.com/langgenius/dify-cloud-kit/oss/aliyun"
	"github.com/langgenius/dify-cloud-kit/oss/azureblob"
	"github.com/langgenius/dify-cloud-kit/oss/gcsblob"
	"github.com/langgenius/dify-cloud-kit/oss/local"
	"github.com/langgenius/dify-cloud-kit/oss/s3"
	"github.com/langgenius/dify-cloud-kit/oss/tencentcos"
)

var OSSFactory = map[string]func(oss.OSSArgs) (oss.OSS, error){
	"local":      local.NewLocalStorage,
	"local_file": local.NewLocalStorage,

	"s3":     s3.NewS3Storage,
	"aws_s3": s3.NewS3Storage,

	"azure":      azureblob.NewAzureBlobStorage,
	"azure_blob": azureblob.NewAzureBlobStorage,

	"aliyun":     aliyun.NewAliyunOSSStorage,
	"aliyun-oss": aliyun.NewAliyunOSSStorage,
	"aliyun_oss": aliyun.NewAliyunOSSStorage,

	"tencent":     tencentcos.NewTencentCOSStorage,
	"tencent_cos": tencentcos.NewTencentCOSStorage,
	"tencent-cos": tencentcos.NewTencentCOSStorage,

	"gcs":            gcsblob.NewGoogleCloudStorage,
	"google-storage": gcsblob.NewGoogleCloudStorage,
}

func Load(name string, args oss.OSSArgs) (oss.OSS, error) {
	f, ok := OSSFactory[name]
	if !ok {
		return nil, oss.ErrStorageNotFound
	}
	return f(args)
}
