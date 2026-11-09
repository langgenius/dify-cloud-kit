package gcsblob

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"cloud.google.com/go/storage"
	"github.com/langgenius/dify-cloud-kit/oss"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

type GoogleCloudStorage struct {
	bucket  string
	project string
	client  *storage.Client
}

func NewGoogleCloudStorage(args oss.OSSArgs) (oss.OSS, error) {
	if args.GoogleCloudStorage == nil {
		return nil, oss.ErrArgumentInvalid.WithDetail("can't find Google Cloud Storage argument in OSSArgs")
	}
	err := args.GoogleCloudStorage.Validate()
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	bucket := args.GoogleCloudStorage.Bucket
	credentialsB64 := args.GoogleCloudStorage.CredentialsB64

	var client *storage.Client
	if credentialsB64 == "" {
		client, err = storage.NewClient(ctx)
		if err != nil {
			return nil, oss.ErrProviderInit.WithError(err).WithDetail("failed to initialize GCS client with ADC")
		}
	} else {
		credentials, err := base64.StdEncoding.DecodeString(credentialsB64)
		if err != nil {
			return nil, oss.ErrProviderInit.WithError(err).WithDetail("credentials must be a base64 encoded string")
		}
		client, err = storage.NewClient(ctx, option.WithCredentialsJSON(credentials))
		if err != nil {
			return nil, oss.ErrProviderInit.WithError(err)
		}
	}
	return &GoogleCloudStorage{
		bucket:  bucket,
		project: args.GoogleCloudStorage.Project,
		client:  client,
	}, nil
}

// Prepare creates the configured bucket if it does not exist. It is
// idempotent: an existing bucket that this client can access is a success,
// a 409 Conflict for a bucket owned by someone else is an error, and every
// other error is returned to the caller. It is never called implicitly.
func (g *GoogleCloudStorage) Prepare(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if g.project == "" {
		return fmt.Errorf("gcs: cannot prepare bucket %q without a project ID (set GoogleCloudStorage.Project)", g.bucket)
	}
	err := g.checkBucketAccess(ctx)
	if err == nil {
		return nil
	}
	if !isBucketNotFound(err) {
		return fmt.Errorf("failed to prepare bucket %q: %w", g.bucket, err)
	}
	err = g.client.Bucket(g.bucket).Create(ctx, g.project, nil)
	if err == nil {
		return nil
	}
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) && apiErr.Code == http.StatusConflict {
		if g.checkBucketAccess(ctx) == nil {
			return nil
		}
		return fmt.Errorf("failed to prepare bucket %q: name is taken and the bucket is not accessible: %w", g.bucket, err)
	}
	return fmt.Errorf("failed to prepare bucket %q: %w", g.bucket, err)
}

// checkBucketAccess lists at most one object, which needs only
// storage.objects.list rather than storage.buckets.get.
func (g *GoogleCloudStorage) checkBucketAccess(ctx context.Context) error {
	it := g.client.Bucket(g.bucket).Objects(ctx, nil)
	it.PageInfo().MaxSize = 1
	_, err := it.Next()
	if errors.Is(err, iterator.Done) {
		return nil
	}
	return err
}

func isBucketNotFound(err error) bool {
	if errors.Is(err, storage.ErrBucketNotExist) {
		return true
	}
	var apiErr *googleapi.Error
	return errors.As(err, &apiErr) && apiErr.Code == http.StatusNotFound
}

func (g *GoogleCloudStorage) Save(key string, data []byte) error {
	ctx := context.Background()
	obj := g.client.Bucket(g.bucket).Object(key)

	wc := obj.NewWriter(ctx)
	if _, err := wc.Write(data); err != nil {
		return err
	}
	return wc.Close()
}

func (g *GoogleCloudStorage) Load(key string) ([]byte, error) {
	reader, err := g.Range(context.Background(), key, nil)
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	return io.ReadAll(reader)
}

func (g *GoogleCloudStorage) Range(ctx context.Context, key string, byteRange *oss.ByteRange) (io.ReadCloser, error) {
	if byteRange != nil {
		if _, err := byteRange.HTTPRange(); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	obj := g.client.Bucket(g.bucket).Object(key)

	offset, length := int64(0), int64(-1)
	if byteRange != nil {
		offset, length = byteRange.Offset, byteRange.Length
	}

	// NewRangeReader reads at most length bytes starting at offset; a
	// negative length reads to the end of the object. Offsets beyond the
	// end of the object fail with a 416 error.
	rc, err := obj.NewRangeReader(ctx, offset, length)
	if err != nil {
		var apiErr *googleapi.Error
		if errors.As(err, &apiErr) && apiErr.Code == http.StatusRequestedRangeNotSatisfiable {
			return nil, oss.ErrRangeNotSatisfiable
		}
		return nil, err
	}

	if byteRange != nil {
		// An empty object cannot satisfy any range request. Most servers
		// answer 416, but some (e.g. fake-gcs-server) return 200 with an
		// empty body for open-ended ranges; reject it either way.
		if rc.Attrs.Size == 0 {
			rc.Close()
			return nil, oss.ErrRangeNotSatisfiable
		}
		// The GCS SDK enforces that a range starting at a non-zero offset
		// returns 206; reconstruct the Content-Range from the reader
		// attributes and verify it the same way as other providers so a
		// server that ignores the range (e.g. decompressive transcoding)
		// fails loudly instead of streaming the whole object.
		contentRange := fmt.Sprintf("bytes %d-%d/%d",
			rc.Attrs.StartOffset, rc.Attrs.StartOffset+rc.Remain()-1, rc.Attrs.Size)
		if err := oss.VerifyContentRange(contentRange, byteRange); err != nil {
			rc.Close()
			return nil, err
		}
	}

	return rc, nil
}

func (g *GoogleCloudStorage) Exists(key string) (bool, error) {
	obj := g.client.Bucket(g.bucket).Object(key)

	_, err := obj.Attrs(context.Background())
	if err != nil {
		if errors.Is(err, storage.ErrObjectNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (g *GoogleCloudStorage) State(key string) (oss.OSSState, error) {
	obj := g.client.Bucket(g.bucket).Object(key)

	attrs, err := obj.Attrs(context.Background())
	if err != nil {
		return oss.OSSState{}, err
	}
	return oss.OSSState{
		Size:         attrs.Size,
		LastModified: attrs.Updated,
	}, nil
}

func (g *GoogleCloudStorage) List(prefix string) ([]oss.OSSPath, error) {
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix = prefix + "/"
	}
	ctx := context.Background()
	it := g.client.Bucket(g.bucket).Objects(ctx, &storage.Query{
		Prefix: prefix,
	})
	res := []oss.OSSPath{}
	for {
		attrs, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}
		if attrs.Name == prefix {
			continue
		}

		key := strings.TrimPrefix(attrs.Name, prefix)
		key = strings.TrimPrefix(key, "/")

		res = append(res, oss.OSSPath{
			Path:  key,
			IsDir: false,
		})

	}
	return res, nil
}

func (g *GoogleCloudStorage) Delete(key string) error {
	ctx := context.Background()
	obj := g.client.Bucket(g.bucket).Object(key)
	attrs, err := obj.Attrs(ctx)
	if err != nil {
		return err
	}

	obj = obj.If(storage.Conditions{GenerationMatch: attrs.Generation})

	return obj.Delete(ctx)
}

func (g *GoogleCloudStorage) Type() string {
	return oss.OSS_TYPE_GCS
}
