package huaweiobs

import (
	"context"
	"errors"
	"io"
	"net/http"
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
	reader, err := h.Range(context.Background(), key, nil)
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	return io.ReadAll(reader)
}

func (h *HuaweiOBSStorage) Range(ctx context.Context, key string, byteRange *oss.ByteRange) (io.ReadCloser, error) {
	if byteRange != nil {
		if _, err := byteRange.HTTPRange(); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	input := &obs.GetObjectInput{
		GetObjectMetadataInput: obs.GetObjectMetadataInput{
			Bucket: h.bucket,
			Key:    key,
		},
	}
	if byteRange != nil {
		input.RangeStart = byteRange.Offset
		// The OBS SDK only emits "bytes=start-end" when end > start; it can
		// neither express "bytes=start-" nor a single-byte range. Both cases
		// resolve the object size first so an unsatisfiable range maps to
		// ErrRangeNotSatisfiable instead of a silent full download.
		if byteRange.Length == -1 || byteRange.Length == 1 {
			size, err := h.objectSize(key)
			if err != nil {
				return nil, err
			}
			if byteRange.Offset >= size {
				return nil, oss.ErrRangeNotSatisfiable
			}
			if byteRange.Length == -1 {
				input.RangeEnd = size - 1
			}
			// Length == 1 keeps RangeEnd == RangeStart, so the SDK sends no
			// Range header; the GET below then fails Content-Range
			// verification loudly instead of returning wrong data.
		} else {
			input.RangeEnd = byteRange.Offset + byteRange.Length - 1
		}
	}

	output, err := h.client.GetObject(input)
	if err != nil {
		var apiErr obs.ObsError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusRequestedRangeNotSatisfiable {
			return nil, oss.ErrRangeNotSatisfiable
		}
		return nil, err
	}

	if byteRange != nil {
		contentRange := ""
		// The OBS SDK lower-cases response header keys.
		if values := output.ResponseHeaders["content-range"]; len(values) > 0 {
			contentRange = values[0]
		}
		if err := oss.VerifyContentRange(contentRange, byteRange); err != nil {
			output.Body.Close()
			return nil, err
		}
	}

	return output.Body, nil
}

// objectSize resolves the object's content length via a HEAD request.
func (h *HuaweiOBSStorage) objectSize(key string) (int64, error) {
	attrs, err := h.client.GetAttribute(&obs.GetAttributeInput{
		GetObjectMetadataInput: obs.GetObjectMetadataInput{
			Bucket: h.bucket,
			Key:    key,
		},
	})
	if err != nil {
		return 0, err
	}
	return attrs.ContentLength, nil
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
