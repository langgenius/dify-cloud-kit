package azureblob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/langgenius/dify-cloud-kit/oss"
)

var _ oss.StreamingOSS = (*AzureBlobStorage)(nil)

type AzureBlobStorage struct {
	client        *azblob.Client
	containerName string
}

func NewAzureBlobStorage(args oss.OSSArgs) (oss.OSS, error) {
	var client *azblob.Client
	var err error

	if args.AzureBlob == nil {
		return nil, oss.ErrArgumentInvalid.WithDetail("can't find Azure Blob argument in OSSArgs")
	}
	err = args.AzureBlob.Validate()
	if err != nil {
		return nil, err
	}
	containerName := args.AzureBlob.ContainerName
	if args.AzureBlob.UseManagedIdentity {
		cred, err := azidentity.NewDefaultAzureCredential(nil)
		if err != nil {
			return nil, oss.ErrProviderInit.WithError(err)
		}
		client, err = azblob.NewClient(args.AzureBlob.ServiceURL, cred, nil)
		if err != nil {
			return nil, oss.ErrProviderInit.WithError(err)
		}
	} else {
		connectionString := args.AzureBlob.ConnectionString
		client, err = azblob.NewClientFromConnectionString(connectionString, nil)
		if err != nil {
			return nil, oss.ErrProviderInit.WithError(err)
		}

	}
	return &AzureBlobStorage{
		client:        client,
		containerName: containerName,
	}, nil
}

func (a *AzureBlobStorage) Save(key string, data []byte) error {
	_, err := a.client.UploadBuffer(context.TODO(), a.containerName, key, data, nil)
	return err
}

func (a *AzureBlobStorage) Load(key string) ([]byte, error) {
	reader, err := a.Range(context.Background(), key, nil)
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	return io.ReadAll(reader)
}

func (a *AzureBlobStorage) Range(ctx context.Context, key string, byteRange *oss.ByteRange) (io.ReadCloser, error) {
	options := &azblob.DownloadStreamOptions{}
	if byteRange != nil {
		if _, err := byteRange.HTTPRange(); err != nil {
			return nil, err
		}
		options.Range = azblob.HTTPRange{Offset: byteRange.Offset}
		switch {
		case byteRange.Length > 0:
			options.Range.Count = byteRange.Length
		case byteRange.Offset == 0 && byteRange.Length == -1:
			// HTTPRange{Offset:0, Count:0} makes the SDK omit the Range
			// header entirely, which would skip Content-Range verification
			// and turn an unsatisfiable range (e.g. empty object) into a
			// silent full download. Request the largest signed range
			// instead; the service clamps the end to the object size and
			// still returns 416 when offset >= size. The header must go
			// through the SDK's signed Range option — injecting x-ms-range
			// via WithHTTPHeader after signing breaks SharedKey auth.
			options.Range.Count = math.MaxInt64
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	resp, err := a.client.DownloadStream(ctx, a.containerName, key, options)
	if err != nil {
		var respErr *azcore.ResponseError
		if errors.As(err, &respErr) && respErr.StatusCode == http.StatusRequestedRangeNotSatisfiable {
			return nil, oss.ErrRangeNotSatisfiable
		}
		return nil, err
	}

	if byteRange != nil {
		contentRange := ""
		if resp.ContentRange != nil {
			contentRange = *resp.ContentRange
		}
		if err := oss.VerifyContentRange(contentRange, byteRange); err != nil {
			resp.Body.Close()
			// Azurite signals an unsatisfiable range with a degenerate
			// "bytes <start>-<start-1>/<total>" Content-Range (end before
			// start) instead of 416; an empty range at the requested
			// offset is unambiguously unsatisfiable.
			if strings.HasPrefix(contentRange,
				fmt.Sprintf("bytes %d-%d/", byteRange.Offset, byteRange.Offset-1)) {
				return nil, oss.ErrRangeNotSatisfiable
			}
			return nil, err
		}
	}

	return resp.NewRetryReader(ctx, &azblob.RetryReaderOptions{EarlyCloseAsError: true}), nil
}

func (a *AzureBlobStorage) Exists(key string) (bool, error) {
	blobClient := a.client.ServiceClient().NewContainerClient(a.containerName).NewBlobClient(key)
	_, err := blobClient.GetProperties(context.TODO(), nil)

	if err != nil {
		if strings.Contains(err.Error(), "404") {
			return false, nil
		}
		return false, err
	}

	return true, nil
}

func (a *AzureBlobStorage) State(key string) (oss.OSSState, error) {
	blobClient := a.client.ServiceClient().NewContainerClient(a.containerName).NewBlobClient(key)
	props, err := blobClient.GetProperties(context.TODO(), nil)

	if err != nil {
		return oss.OSSState{}, err
	}

	return oss.OSSState{
		Size:         *props.ContentLength,
		LastModified: *props.LastModified,
	}, nil
}

func (a *AzureBlobStorage) List(prefix string) ([]oss.OSSPath, error) {
	// append a slash to the prefix if it doesn't end with one
	if !strings.HasSuffix(prefix, "/") {
		prefix = prefix + "/"
	}

	pager := a.client.NewListBlobsFlatPager(a.containerName, &azblob.ListBlobsFlatOptions{
		Prefix: &prefix,
	})

	paths := make([]oss.OSSPath, 0)
	for pager.More() {
		page, err := pager.NextPage(context.TODO())
		if err != nil {
			return nil, err
		}

		for _, blob := range page.Segment.BlobItems {
			// remove prefix
			key := strings.TrimPrefix(*blob.Name, prefix)
			// remove leading slash
			key = strings.TrimPrefix(key, "/")
			paths = append(paths, oss.OSSPath{
				Path:  key,
				IsDir: false,
			})
		}
	}

	return paths, nil
}

func (a *AzureBlobStorage) Delete(key string) error {
	_, err := a.client.DeleteBlob(context.TODO(), a.containerName, key, nil)
	return err
}

func (a *AzureBlobStorage) Type() string {
	return oss.OSS_TYPE_AZURE_BLOB
}
