package factory_test

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/langgenius/dify-cloud-kit/oss"
	"github.com/langgenius/dify-cloud-kit/oss/factory"
	"github.com/stretchr/testify/require"
)

func TestStreamStoredGzipBytes(t *testing.T) {
	var stored bytes.Buffer
	zw := gzip.NewWriter(&stored)
	_, err := io.WriteString(zw, "cloud-kit gzip fixture")
	require.NoError(t, err)
	require.NoError(t, zw.Close())

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Type", "application/octet-stream")
		http.ServeContent(w, r, "object", time.Time{}, bytes.NewReader(stored.Bytes()))
	}))
	t.Cleanup(server.Close)

	for _, provider := range []struct {
		name string
		args oss.OSSArgs
	}{
		{"aliyun", oss.OSSArgs{AliyunOSS: &oss.AliyunOSS{
			Endpoint: server.URL, Bucket: "bucket", AccessKey: "test", SecretKey: "test", AuthVersion: "v1",
		}}},
		{"tencent", oss.OSSArgs{TencentCOS: &oss.TencentCOS{
			Endpoint: server.URL, Bucket: "bucket", SecretID: "test", SecretKey: "test", Region: "test",
		}}},
	} {
		t.Run(provider.name, func(t *testing.T) {
			store, err := factory.Load(provider.name, provider.args)
			require.NoError(t, err)
			for _, tc := range []struct {
				name string
				r    *oss.ByteRange
				want []byte
			}{
				{"full", nil, stored.Bytes()},
				{"zero to EOF", &oss.ByteRange{Offset: 0, Length: -1}, stored.Bytes()},
				{"bounded", &oss.ByteRange{Offset: 2, Length: 4}, stored.Bytes()[2:6]},
			} {
				t.Run(tc.name, func(t *testing.T) {
					rc, err := oss.OpenStream(t.Context(), store, "object", tc.r)
					require.NoError(t, err)
					defer rc.Close()
					got, err := io.ReadAll(rc)
					require.NoError(t, err)
					require.Equal(t, tc.want, got)
				})
			}
		})
	}
}
