package huaweiobs

import (
	"context"
	"errors"
	"io"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"time"

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

type getObjectResult struct {
	output *obs.GetObjectOutput
	err    error
}

func (h *HuaweiOBSStorage) Range(ctx context.Context, key string, byteRange *oss.ByteRange) (io.ReadCloser, error) {
	rangeHeader := ""
	if byteRange != nil {
		var err error
		if rangeHeader, err = byteRange.HTTPRange(); err != nil {
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

	// The OBS SDK takes no per-request context, so the GET runs in the
	// background and is abandoned on cancellation; a response that still
	// arrives afterwards is closed. The SDK's RangeStart/RangeEnd cannot
	// express "bytes=N-" or "bytes=N-N", so the Range header is sent
	// verbatim instead (custom headers are applied before signing).
	done := make(chan getObjectResult, 1)
	go func() {
		var res getObjectResult
		if rangeHeader == "" {
			res.output, res.err = h.client.GetObject(input)
		} else {
			res.output, res.err = h.client.GetObject(input, obs.WithCustomHeader("Range", rangeHeader))
		}
		done <- res
	}()

	var res getObjectResult
	select {
	case res = <-done:
	case <-ctx.Done():
		go func() {
			if late := <-done; late.output != nil {
				late.output.Body.Close()
			}
		}()
		return nil, ctx.Err()
	}

	if res.err != nil {
		var apiErr obs.ObsError
		if errors.As(res.err, &apiErr) && apiErr.StatusCode == http.StatusRequestedRangeNotSatisfiable {
			return nil, oss.ErrRangeNotSatisfiable
		}
		return nil, res.err
	}
	output := res.output

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

	return newCtxBody(ctx, output.Body), nil
}

// ctxBody closes the response body when ctx is done so a blocked Read
// returns, and reports the context error instead of the close error.
type ctxBody struct {
	ctx  context.Context
	body io.ReadCloser
	stop func() bool
}

func newCtxBody(ctx context.Context, body io.ReadCloser) *ctxBody {
	return &ctxBody{
		ctx:  ctx,
		body: body,
		stop: context.AfterFunc(ctx, func() { body.Close() }),
	}
}

func (b *ctxBody) Read(p []byte) (int, error) {
	n, err := b.body.Read(p)
	if err != nil && err != io.EOF {
		if ctxErr := b.ctx.Err(); ctxErr != nil {
			return n, ctxErr
		}
	}
	return n, err
}

func (b *ctxBody) Close() error {
	b.stop()
	return b.body.Close()
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
			key, ok := listedObjectPath(prefix, v.Key)
			if !ok {
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

func listedObjectPath(prefix string, objectKey string) (string, bool) {
	key := strings.TrimPrefix(objectKey, prefix)
	key = strings.TrimLeft(key, "/")
	if key == "" || strings.HasSuffix(key, "/") {
		return "", false
	}

	return key, true
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
