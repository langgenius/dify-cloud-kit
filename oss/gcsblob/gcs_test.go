package gcsblob

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"cloud.google.com/go/storage"
	"github.com/langgenius/dify-cloud-kit/oss"
	"google.golang.org/api/option"
)

func TestGoogleCloudStorage_List(t *testing.T) {
	t.Parallel()

	objectNames := []string{
		"plugin/langgenius/plugin.difypkg",
		"plugin_packages/langgenius/plugin.difypkg",
	}
	tests := []struct {
		name   string
		prefix string
		want   []oss.OSSPath
	}{
		{
			name:   "prefix without trailing slash",
			prefix: "plugin",
			want: []oss.OSSPath{
				{Path: "langgenius/plugin.difypkg", IsDir: false},
			},
		},
		{
			name:   "prefix with trailing slash",
			prefix: "plugin/",
			want: []oss.OSSPath{
				{Path: "langgenius/plugin.difypkg", IsDir: false},
			},
		},
		{
			name:   "empty prefix",
			prefix: "",
			want: []oss.OSSPath{
				{Path: "plugin/langgenius/plugin.difypkg", IsDir: false},
				{Path: "plugin_packages/langgenius/plugin.difypkg", IsDir: false},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				prefix := r.URL.Query().Get("prefix")
				items := make([]map[string]string, 0, len(objectNames))
				for _, name := range objectNames {
					if strings.HasPrefix(name, prefix) {
						items = append(items, map[string]string{"name": name})
					}
				}

				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(map[string]any{"items": items}); err != nil {
					t.Errorf("encode list response: %v", err)
				}
			}))
			t.Cleanup(server.Close)

			client, err := storage.NewClient(
				context.Background(),
				option.WithEndpoint(server.URL),
				option.WithoutAuthentication(),
			)
			if err != nil {
				t.Fatalf("create storage client: %v", err)
			}
			t.Cleanup(func() {
				if err := client.Close(); err != nil {
					t.Errorf("close storage client: %v", err)
				}
			})

			gcs := &GoogleCloudStorage{
				bucket: "test-bucket",
				client: client,
			}
			got, err := gcs.List(tt.prefix)
			if err != nil {
				t.Fatalf("List(%q): %v", tt.prefix, err)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("List(%q) = %#v, want %#v", tt.prefix, got, tt.want)
			}
		})
	}
}
