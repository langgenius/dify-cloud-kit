package oss

import "time"

// OSS supports different types of object storage services
// such as local file system, AWS S3, and Tencent COS.
// The interface defines methods for saving, loading, checking existence,
const (
	OSS_TYPE_LOCAL       = "local"
	OSS_TYPE_S3          = "aws_s3"
	OSS_TYPE_TENCENT_COS = "tencent_cos"
	OSS_TYPE_AZURE_BLOB  = "azure_blob"
	OSS_TYPE_GCS         = "gcs"
	OSS_TYPE_ALIYUN_OSS  = "aliyun_oss"
	OSS_TYPE_HUAWEI_OBS  = "huawei_obs"
)

type OSSState struct {
	Size         int64
	LastModified time.Time
}

type OSSPath struct {
	Path  string
	IsDir bool
}

type OSS interface {
	// Save saves data into path key
	Save(key string, data []byte) error
	// Load loads data from path key
	Load(key string) ([]byte, error)
	// Exists checks if the data exists in the path key
	Exists(key string) (bool, error)
	// State gets the state of the data in the path key
	State(key string) (OSSState, error)
	// List lists all the data with the given prefix, and all the paths are absolute paths
	List(prefix string) ([]OSSPath, error)
	// Delete deletes the data in the path key
	Delete(key string) error
	// Type returns the type of the storage
	// For example: local, aws_s3, tencent_cos
	Type() string

	//Validate() error
}

type OSSArgs struct {
	S3                 *S3
	Local              *Local
	AzureBlob          *AzureBlob
	AliyunOSS          *AliyunOSS
	TencentCOS         *TencentCOS
	GoogleCloudStorage *GoogleCloudStorage
	HuaweiOBS          *HuaweiOBS
}

type S3 struct {
	UseAws       bool
	Endpoint     string
	UsePathStyle bool
	AccessKey    string
	SecretKey    string
	Bucket       string
	Region       string
}

type AzureBlob struct {
	ConnectionString string
	ContainerName    string
}

type Local struct {
	Path string
}

type AliyunOSS struct {
	Region      string
	Endpoint    string
	AccessKey   string
	SecretKey   string
	AuthVersion string
	Path        string
	Bucket      string
}

type TencentCOS struct {
	Region    string
	SecretID  string
	SecretKey string
	Bucket    string
}

type GoogleCloudStorage struct {
	Bucket         string
	CredentialsB64 string
}

type HuaweiOBS struct {
	Bucket    string
	AccessKey string
	SecretKey string
	Server    string
}
