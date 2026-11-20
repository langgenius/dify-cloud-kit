package volcenginetos

import (
	"bytes"
	"context"
	"errors"
	"github.com/langgenius/dify-cloud-kit/oss"
	"github.com/volcengine/ve-tos-golang-sdk/v2/tos"
	"io"
	"net/http"
	"strings"
)

type VolcengineTOSStorage struct {
	bucket string
	client *tos.ClientV2
}

func NewVolcengineTOSStorage(args oss.OSSArgs) (oss.OSS, error) {
	if args.VolcengineTOS == nil {
		return nil, oss.ErrArgumentInvalid.WithDetail("can't find Volcengine TOS argument in OSSArgs")
	}

	err := args.VolcengineTOS.Validate()
	if err != nil {
		return nil, err
	}

	bucket := args.VolcengineTOS.Bucket
	accessKey := args.VolcengineTOS.AccessKey
	secretKey := args.VolcengineTOS.SecretKey
	endpoint := args.VolcengineTOS.Endpoint
	region := args.VolcengineTOS.Region

	client, err := tos.NewClientV2(endpoint,
		tos.WithRegion(region),
		tos.WithCredentials(tos.NewStaticCredentials(accessKey, secretKey)),
	)
	if err != nil {
		return nil, oss.ErrProviderInit.WithError(err)
	}
	return &VolcengineTOSStorage{
		bucket: bucket,
		client: client,
	}, nil
}

func (s *VolcengineTOSStorage) Save(key string, data []byte) error {
	_, err := s.client.PutObjectV2(context.Background(), &tos.PutObjectV2Input{
		PutObjectBasicInput: tos.PutObjectBasicInput{
			Bucket: s.bucket,
			Key:    key,
		},
		Content: bytes.NewReader(data),
	})
	return err
}

func (s *VolcengineTOSStorage) Load(key string) ([]byte, error) {
	reader, err := s.Range(context.Background(), key, nil)
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	return io.ReadAll(reader)
}

func (s *VolcengineTOSStorage) Range(ctx context.Context, key string, byteRange *oss.ByteRange) (io.ReadCloser, error) {
	input := &tos.GetObjectV2Input{
		Bucket: s.bucket,
		Key:    key,
	}
	if byteRange != nil {
		rangeHeader, err := byteRange.HTTPRange()
		if err != nil {
			return nil, err
		}
		input.Range = rangeHeader
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	resp, err := s.client.GetObjectV2(ctx, input)
	if err != nil {
		var apiErr *tos.TosServerError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusRequestedRangeNotSatisfiable {
			return nil, oss.ErrRangeNotSatisfiable
		}
		return nil, err
	}

	if byteRange != nil {
		if err := oss.VerifyContentRange(resp.ContentRange, byteRange); err != nil {
			resp.Content.Close()
			return nil, err
		}
	}

	return resp.Content, nil
}

func (s *VolcengineTOSStorage) Exists(key string) (bool, error) {
	_, err := s.client.HeadObjectV2(context.Background(), &tos.HeadObjectV2Input{
		Bucket: s.bucket,
		Key:    key,
	})
	if err != nil {
		if tosErr, ok := err.(*tos.TosServerError); ok && tosErr.StatusCode == 404 {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (s *VolcengineTOSStorage) State(key string) (oss.OSSState, error) {
	resp, err := s.client.HeadObjectV2(context.Background(), &tos.HeadObjectV2Input{
		Bucket: s.bucket,
		Key:    key,
	})
	if err != nil {
		return oss.OSSState{}, err
	}
	return oss.OSSState{
		Size:         resp.ContentLength,
		LastModified: resp.LastModified,
	}, nil
}

func (s *VolcengineTOSStorage) List(prefix string) ([]oss.OSSPath, error) {
	if !strings.HasSuffix(prefix, "/") {
		prefix = prefix + "/"
	}
	var result []oss.OSSPath
	truncated := true
	continuationToken := ""
	for truncated {

		resp, err := s.client.ListObjectsType2(context.Background(), &tos.ListObjectsType2Input{
			Bucket:            s.bucket,
			Prefix:            prefix,
			MaxKeys:           1000,
			ContinuationToken: continuationToken,
		})
		if err != nil {
			return nil, err
		}
		for _, obj := range resp.Contents {
			// remove prefix
			key := strings.TrimPrefix(obj.Key, prefix)
			// remove leading slash
			key = strings.TrimPrefix(key, "/")
			if key == "" {
				continue
			}
			result = append(result, oss.OSSPath{
				Path:  key,
				IsDir: false,
			})
		}
		if !resp.IsTruncated {
			break
		}
		truncated = resp.IsTruncated
		continuationToken = resp.NextContinuationToken
	}
	return result, nil
}

func (s *VolcengineTOSStorage) Delete(key string) error {
	_, err := s.client.DeleteObjectV2(context.Background(), &tos.DeleteObjectV2Input{
		Bucket: s.bucket,
		Key:    key,
	})
	return err
}

func (s *VolcengineTOSStorage) Type() string {
	return oss.OSS_TYPE_VOLCENGINE_TOS
}
