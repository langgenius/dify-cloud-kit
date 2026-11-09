package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/langgenius/dify-cloud-kit/oss"
)

var _ oss.StreamingOSS = (*S3Storage)(nil)

type S3Storage struct {
	bucket string
	client *s3.Client
}

func NewS3Storage(args oss.OSSArgs) (oss.OSS, error) {
	var err error
	if args.S3 == nil {
		return nil, oss.ErrArgumentInvalid.WithDetail("can't find s3 argument in OSSArgs")
	}
	err = args.S3.Validate()
	if err != nil {
		return nil, err
	}
	useAws := args.S3.UseAws
	ak := args.S3.AccessKey
	sk := args.S3.SecretKey
	region := args.S3.Region
	endpoint := args.S3.Endpoint
	usePathStyle := args.S3.UsePathStyle
	bucket := args.S3.Bucket
	useIamRole := args.S3.UseIamRole
	signatureVersion := args.S3.SignatureVersion

	var cfg aws.Config
	var client *s3.Client

	if useAws {
		if (ak == "" && sk == "") || useIamRole {
			cfg, err = config.LoadDefaultConfig(
				context.TODO(),
				config.WithRegion(region),
			)
		} else {
			// 处理签名版本和凭证
			var credProvider aws.CredentialsProvider
			if strings.ToLower(signatureVersion) == "unsigned" {
				credProvider = aws.AnonymousCredentials{}
			} else {
				credProvider = credentials.NewStaticCredentialsProvider(
					ak,
					sk,
					"",
				)
			}

			cfg, err = config.LoadDefaultConfig(
				context.TODO(),
				config.WithRegion(region),
				config.WithCredentialsProvider(credProvider),
			)
		}
		if err != nil {
			return nil, oss.ErrProviderInit.WithError(err)
		}

		client = s3.NewFromConfig(cfg, func(options *s3.Options) {
			if endpoint != "" {
				options.BaseEndpoint = aws.String(endpoint)
			}
			options.UsePathStyle = usePathStyle
		})
	} else {
		var credProvider aws.CredentialsProvider
		if strings.ToLower(signatureVersion) == "unsigned" {
			credProvider = aws.AnonymousCredentials{}
		} else {
			credProvider = credentials.NewStaticCredentialsProvider(ak, sk, "")
		}
		client = s3.New(s3.Options{
			Credentials:  credProvider,
			UsePathStyle: usePathStyle,
			Region:       region,
			EndpointResolver: s3.EndpointResolverFunc(
				func(region string, options s3.EndpointResolverOptions) (aws.Endpoint, error) {
					signingMethod := normalizeSignatureVersion(signatureVersion)
					return aws.Endpoint{
						URL:               endpoint,
						HostnameImmutable: false,
						SigningName:       "s3",
						PartitionID:       "aws",
						SigningRegion:     region,
						SigningMethod:     signingMethod,
						Source:            aws.EndpointSourceCustom,
					}, nil
				}),
		})
	}

	// check bucket
	_, err = client.HeadBucket(context.TODO(), &s3.HeadBucketInput{
		Bucket: aws.String(bucket),
	})
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && apiErr.ErrorCode() == "NotFound" {
			_, err = client.CreateBucket(context.TODO(), &s3.CreateBucketInput{
				Bucket: aws.String(bucket),
			})
			if err != nil {
				return nil, oss.ErrProviderInit.WithError(err)
			}
		}
	}
	return &S3Storage{bucket: bucket, client: client}, nil
}

func normalizeSignatureVersion(version string) string {
	switch strings.ToLower(version) {
	case "unsigned":
		return ""
	case "":
		return "v4" // 默认使用 v4
	default:
		return version
	}
}

func (s *S3Storage) Save(key string, data []byte) error {
	_, err := s.client.PutObject(context.TODO(), &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(data),
	})
	return err
}

func (s *S3Storage) Load(key string) ([]byte, error) {
	reader, err := s.Range(context.Background(), key, nil)
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	return io.ReadAll(reader)
}

func (s *S3Storage) Range(ctx context.Context, key string, byteRange *oss.ByteRange) (io.ReadCloser, error) {
	input := &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}
	if byteRange != nil {
		rangeHeader, err := byteRange.HTTPRange()
		if err != nil {
			return nil, err
		}
		input.Range = aws.String(rangeHeader)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	resp, err := s.client.GetObject(ctx, input)
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) &&
			(apiErr.ErrorCode() == "InvalidRange" || apiErr.ErrorCode() == "RequestedRangeNotSatisfiable") {
			return nil, oss.ErrRangeNotSatisfiable
		}
		var respErr *awshttp.ResponseError
		if errors.As(err, &respErr) && respErr.HTTPStatusCode() == http.StatusRequestedRangeNotSatisfiable {
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
			return nil, err
		}
	}

	return resp.Body, nil
}

func (s *S3Storage) Exists(key string) (bool, error) {
	_, err := s.client.HeadObject(context.TODO(), &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	return err == nil, nil
}

func (s *S3Storage) Delete(key string) error {
	_, err := s.client.DeleteObject(context.TODO(), &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	return err
}

func (s *S3Storage) List(prefix string) ([]oss.OSSPath, error) {
	// append a slash to the prefix if it doesn't end with one
	if !strings.HasSuffix(prefix, "/") {
		prefix = prefix + "/"
	}

	var keys []oss.OSSPath
	input := &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(prefix),
	}

	paginator := s3.NewListObjectsV2Paginator(s.client, input)
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(context.TODO())
		if err != nil {
			return nil, err
		}
		for _, obj := range page.Contents {
			// remove prefix
			key := strings.TrimPrefix(*obj.Key, prefix)
			// remove leading slash
			key = strings.TrimPrefix(key, "/")
			keys = append(keys, oss.OSSPath{
				Path:  key,
				IsDir: false,
			})
		}
	}

	return keys, nil
}

func (s *S3Storage) State(key string) (oss.OSSState, error) {
	resp, err := s.client.HeadObject(context.TODO(), &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return oss.OSSState{}, err
	}

	if resp.ContentLength == nil {
		resp.ContentLength = ToPtr[int64](0)
	}
	if resp.LastModified == nil {
		resp.LastModified = ToPtr(time.Time{})
	}

	return oss.OSSState{
		Size:         *resp.ContentLength,
		LastModified: *resp.LastModified,
	}, nil
}

func (s *S3Storage) Type() string {
	return oss.OSS_TYPE_S3
}

// Prepare creates the configured bucket if it does not exist. It is
// idempotent: an already-existing bucket is a success, and every other error
// is returned to the caller. It is never called implicitly. (The legacy
// constructor additionally ensures the bucket at load time; Prepare is the
// explicit capability for callers that want to control this.)
func (s *S3Storage) Prepare(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(s.bucket),
	})
	if err == nil {
		return nil
	}
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) ||
		(apiErr.ErrorCode() != "NotFound" && apiErr.ErrorCode() != "NoSuchBucket") {
		return fmt.Errorf("failed to prepare bucket %q: %w", s.bucket, err)
	}
	_, err = s.client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(s.bucket),
	})
	if err == nil {
		return nil
	}
	var createErr smithy.APIError
	if errors.As(err, &createErr) {
		switch createErr.ErrorCode() {
		case "BucketAlreadyOwnedByYou":
			return nil
		case "BucketAlreadyExists":
			if _, headErr := s.client.HeadBucket(ctx, &s3.HeadBucketInput{
				Bucket: aws.String(s.bucket),
			}); headErr == nil {
				return nil
			}
			return fmt.Errorf("failed to prepare bucket %q: name is taken and the bucket is not accessible: %w", s.bucket, err)
		}
	}
	return fmt.Errorf("failed to prepare bucket %q: %w", s.bucket, err)
}

func ToPtr[T any](value T) *T {
	return &value
}
