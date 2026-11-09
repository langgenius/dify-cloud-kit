package huaweiobs

import "testing"

func TestListedObjectPath(t *testing.T) {
	tests := []struct {
		name      string
		prefix    string
		objectKey string
		want      string
		wantOK    bool
	}{
		{
			name:      "file under prefix",
			prefix:    "plugins/",
			objectKey: "plugins/langgenius/model-provider.difypkg",
			want:      "langgenius/model-provider.difypkg",
			wantOK:    true,
		},
		{
			name:      "file under prefix without trailing slash",
			prefix:    "plugins",
			objectKey: "plugins/langgenius/model-provider.difypkg",
			want:      "langgenius/model-provider.difypkg",
			wantOK:    true,
		},
		{
			name:      "directory placeholder under prefix",
			prefix:    "plugins/",
			objectKey: "plugins/langgenius/",
			wantOK:    false,
		},
		{
			name:      "prefix placeholder",
			prefix:    "plugins/",
			objectKey: "plugins/",
			wantOK:    false,
		},
		{
			name:      "leading slash after prefix",
			prefix:    "plugins",
			objectKey: "plugins//langgenius/model-provider.difypkg",
			want:      "langgenius/model-provider.difypkg",
			wantOK:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := listedObjectPath(tt.prefix, tt.objectKey)
			if ok != tt.wantOK {
				t.Fatalf("listedObjectPath() ok = %v, want %v", ok, tt.wantOK)
			}
			if got != tt.want {
				t.Fatalf("listedObjectPath() = %q, want %q", got, tt.want)
			}
		})
	}
}
