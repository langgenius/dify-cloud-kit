package oss

import (
	"fmt"
	"time"
)

// OSS supports different types of object storage services
// such as local file system, AWS S3, and Tencent COS.
// The interface defines methods for saving, loading, checking existence,
const (
	OSS_TYPE_LOCAL          = "local"
	OSS_TYPE_S3             = "aws_s3"
	OSS_TYPE_TENCENT_COS    = "tencent_cos"
	OSS_TYPE_AZURE_BLOB     = "azure_blob"
	OSS_TYPE_GCS            = "gcs"
	OSS_TYPE_ALIYUN_OSS     = "aliyun_oss"
	OSS_TYPE_HUAWEI_OBS     = "huawei_obs"
	OSS_TYPE_VOLCENGINE_TOS = "volcengine_tos"
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
}

type OSSArgs struct {
	S3                 *S3
	Local              *Local
	AzureBlob          *AzureBlob
	AliyunOSS          *AliyunOSS
	TencentCOS         *TencentCOS
	GoogleCloudStorage *GoogleCloudStorage
	HuaweiOBS          *HuaweiOBS
	VolcengineTOS      *VolcengineTOS
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

func (s *S3) Validate() error {
	if s.Bucket == "" || s.Region == "" {
		msg := fmt.Sprintf("bucket and region cannot be empty.")
		return ErrArgumentInvalid.WithDetail(msg)
	}
	return nil
}

type AzureBlob struct {
	ConnectionString string
	ContainerName    string
}

func (a *AzureBlob) Validate() error {
	if a.ConnectionString == "" || a.ContainerName == "" {
		msg := fmt.Sprintf("connectorString and containerName cannot be empty.")
		return ErrArgumentInvalid.WithDetail(msg)
	}
	return nil
}

type Local struct {
	Path string
}

func (l *Local) Validate() error {
	if l.Path == "" {
		return ErrArgumentInvalid.WithDetail("path cannot be empty")
	}
	return nil
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

func (a *AliyunOSS) Validate() error {
	if a.Bucket == "" || a.SecretKey == "" || a.AccessKey == "" || a.Endpoint == "" {
		msg := fmt.Sprintf("bucket, accesskKey, secretKey, endpoint cannot be empty.")
		return ErrArgumentInvalid.WithDetail(msg)
	}
	return nil
}

type TencentCOS struct {
	Region    string
	SecretID  string
	SecretKey string
	Bucket    string
}

func (t *TencentCOS) Validate() error {
	if t.Bucket == "" || t.Region == "" || t.SecretID == "" || t.SecretKey == "" {
		msg := fmt.Sprintf("bucket, region, secretKey, secretID cannot be empty.")
		return ErrArgumentInvalid.WithDetail(msg)
	}
	return nil
}

type GoogleCloudStorage struct {
	Bucket         string
	CredentialsB64 string
}

func (g *GoogleCloudStorage) Validate() error {
	if g.Bucket == "" || g.CredentialsB64 == "" {
		msg := fmt.Sprintf("bucket and credentials cannot be empty.")
		return ErrArgumentInvalid.WithDetail(msg)
	}
	return nil
}

type HuaweiOBS struct {
	Bucket    string
	AccessKey string
	SecretKey string
	Server    string
}

func (h *HuaweiOBS) Validate() error {
	if h.Bucket == "" || h.AccessKey == "" || h.SecretKey == "" || h.Server == "" {
		msg := fmt.Sprintf("bucket, accesskKey, secretKey, server cannot be empty.")
		return ErrArgumentInvalid.WithDetail(msg)
	}
	return nil
}

type VolcengineTOS struct {
	Region    string
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
}

func (t *VolcengineTOS) Validate() error {
	if t.Bucket == "" || t.Endpoint == "" || t.AccessKey == "" || t.SecretKey == "" {
		msg := fmt.Sprintf("bucket, endpoint,accessKey, secretKey cannot be empty.")
		return ErrArgumentInvalid.WithDetail(msg)
	}
	return nil
}
