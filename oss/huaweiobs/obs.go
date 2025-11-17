package huaweiobs

import (
	"io"
	"os"
	"strings"
	"time"

	"math/rand"

	"github.com/huaweicloud/huaweicloud-sdk-go-obs/obs"
	"github.com/langgenius/dify-cloud-kit/oss"
)

type HuaweiOBSStorage struct {
	bucket string
	client *obs.ObsClient
}

func NewHuaweiOBSStorage(args oss.OSSArgs) (oss.OSS, error) {
	if args.HuaweiOBS == nil {
		return nil, oss.ErrArgumentInvalid.WithDetail("can't find Huawei OBS argument in OSSArgs")
	}

	err := args.HuaweiOBS.Validate()
	if err != nil {
		return nil, err
	}
	ak := args.HuaweiOBS.AccessKey
	sk := args.HuaweiOBS.SecretKey
	endpoint := args.HuaweiOBS.Server
	bucket := args.HuaweiOBS.Bucket
	pathStyle := args.HuaweiOBS.PathStyle
	client, err := obs.New(ak, sk, endpoint, obs.WithPathStyle(pathStyle))
	if err != nil {
		return nil, oss.ErrProviderInit.WithError(err)
	}

	return &HuaweiOBSStorage{
		bucket: bucket,
		client: client,
	}, nil
}

func (h *HuaweiOBSStorage) Save(key string, data []byte) error {
	tmpFilename := randomString(5)
	file, err := os.CreateTemp("/tmp", tmpFilename)
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())

	_, err = file.Write(data)
	if err != nil {
		return err
	}

	_, err = h.client.PutFile(&obs.PutFileInput{
		PutObjectBasicInput: obs.PutObjectBasicInput{
			ObjectOperationInput: obs.ObjectOperationInput{
				Bucket: h.bucket,
				Key:    key,
			},
		},
		SourceFile: file.Name(),
	})
	return err
}

func (h *HuaweiOBSStorage) Load(key string) ([]byte, error) {
	output, err := h.client.GetObject(&obs.GetObjectInput{
		GetObjectMetadataInput: obs.GetObjectMetadataInput{
			Bucket: h.bucket,
			Key:    key,
		},
	})
	if err != nil {
		return nil, err
	}
	defer output.Body.Close()

	return io.ReadAll(output.Body)
}

func (h *HuaweiOBSStorage) Exists(key string) (bool, error) {
	_, err := h.client.HeadObject(&obs.HeadObjectInput{
		Bucket: h.bucket,
		Key:    key,
	})

	if err == nil {
		return true, nil
	}

	if obsErr, ok := err.(obs.ObsError); ok && obsErr.StatusCode == 404 {
		return false, nil
	}
	return false, err
}

func (h *HuaweiOBSStorage) State(key string) (oss.OSSState, error) {
	output, err := h.client.GetAttribute(&obs.GetAttributeInput{
		GetObjectMetadataInput: obs.GetObjectMetadataInput{
			Bucket: h.bucket,
			Key:    key,
		},
	})
	if err != nil {
		return oss.OSSState{}, err
	}
	return oss.OSSState{
		Size:         output.ContentLength,
		LastModified: output.LastModified,
	}, nil
}

func (h *HuaweiOBSStorage) List(prefix string) ([]oss.OSSPath, error) {
	if !strings.HasSuffix(prefix, "/") {
		prefix = prefix + "/"
	}

	marker := ""
	paths := []oss.OSSPath{}
	for {
		output, err := h.client.ListObjects(&obs.ListObjectsInput{
			Bucket: h.bucket,
			ListObjsInput: obs.ListObjsInput{
				Prefix: prefix,
			},
			Marker: marker,
		})
		if err != nil {
			return nil, err
		}

		for _, v := range output.Contents {
			key := strings.TrimPrefix(v.Key, prefix)
			key = strings.TrimPrefix(key, "/")

			if key == "" {
				continue
			}
			paths = append(paths, oss.OSSPath{
				Path:  key,
				IsDir: false,
			})
		}
		if output.IsTruncated {
			marker = output.NextMarker
		} else {
			break
		}
	}

	return paths, nil
}

func (h *HuaweiOBSStorage) Delete(key string) error {
	_, err := h.client.DeleteObject(&obs.DeleteObjectInput{
		Bucket: h.bucket,
		Key:    key,
	})
	return err
}

func (h *HuaweiOBSStorage) Type() string {
	return oss.OSS_TYPE_HUAWEI_OBS
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
