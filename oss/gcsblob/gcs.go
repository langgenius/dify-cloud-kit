package gcsblob

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"strings"

	"cloud.google.com/go/storage"
	"github.com/langgenius/dify-cloud-kit/oss"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

type GoogleCloudStorage struct {
	bucket string
	client *storage.Client
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
		bucket: bucket,
		client: client,
	}, nil
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
	rc, err := g.client.Bucket(g.bucket).Object(key).NewReader(context.Background())
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, err
	}
	return data, nil
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
			Path:  attrs.Name,
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
