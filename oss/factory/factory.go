package factory

import (
	"fmt"

	"github.com/langgenius/dify-cloud-kit/oss"
	"github.com/langgenius/dify-cloud-kit/oss/aliyun"
	"github.com/langgenius/dify-cloud-kit/oss/azureblob"
	"github.com/langgenius/dify-cloud-kit/oss/gcsblob"
	"github.com/langgenius/dify-cloud-kit/oss/huanweiobs"
	"github.com/langgenius/dify-cloud-kit/oss/local"
	"github.com/langgenius/dify-cloud-kit/oss/s3"
	"github.com/langgenius/dify-cloud-kit/oss/tencentcos"
	"github.com/langgenius/dify-cloud-kit/oss/volcenginetos"
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

	"huawei":     huanweiobs.NewHuaweiOBSStorage,
	"huawei-obs": huanweiobs.NewHuaweiOBSStorage,
	"huawei_obs": huanweiobs.NewHuaweiOBSStorage,

	"volcengine":     volcenginetos.NewVolcengineTOSStorage,
	"volcengine_tos": volcenginetos.NewVolcengineTOSStorage,
	"volcengine-tos": volcenginetos.NewVolcengineTOSStorage,
}

func Load(name string, args oss.OSSArgs) (oss.OSS, error) {
	f, ok := OSSFactory[name]
	if !ok {
		msg := fmt.Sprintf("[ %s ] is not in the provider list", name)
		return nil, oss.ErrProviderNotFound.WithDetail(msg)
	}
	return f(args)
}
