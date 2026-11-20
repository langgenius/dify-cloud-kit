package local

import (
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/langgenius/dify-cloud-kit/oss"
)

var _ oss.StreamingOSS = (*LocalStorage)(nil)

type LocalStorage struct {
	root string
}

func NewLocalStorage(args oss.OSSArgs) (oss.OSS, error) {
	if args.Local == nil {
		return nil, oss.ErrArgumentInvalid.WithDetail("can't find Local argument in OSSArgs")
	}
	err := args.Local.Validate()
	if err != nil {
		return nil, err
	}
	root := args.Local.Path
	if err := os.MkdirAll(root, 0755); err != nil {
		return nil, oss.ErrProviderInit.WithError(err).WithDetail("failed to create storage path")
	}

	return &LocalStorage{root: root}, nil
}

func (l *LocalStorage) Save(key string, data []byte) error {
	path := filepath.Join(l.root, key)
	filePath := filepath.Dir(path)
	if err := os.MkdirAll(filePath, 0o755); err != nil {
		return err
	}

	return os.WriteFile(path, data, 0o644)
}

func (l *LocalStorage) Load(key string) ([]byte, error) {
	reader, err := l.Range(context.Background(), key, nil)
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	return io.ReadAll(reader)
}

// ctxFileReader reads from a file section through an open *os.File, checking
// the context on every Read so a cancelled read returns promptly.
type ctxFileReader struct {
	ctx     context.Context
	file    *os.File
	section *io.SectionReader
}

func (r *ctxFileReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.section.Read(p)
}

func (r *ctxFileReader) Close() error {
	return r.file.Close()
}

func (l *LocalStorage) Range(ctx context.Context, key string, byteRange *oss.ByteRange) (io.ReadCloser, error) {
	if byteRange != nil {
		if _, err := byteRange.HTTPRange(); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	path := filepath.Join(l.root, key)
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	size := info.Size()

	var offset, length int64
	if byteRange != nil {
		offset = byteRange.Offset
		if offset >= size {
			file.Close()
			return nil, oss.ErrRangeNotSatisfiable
		}
		if byteRange.Length == -1 || byteRange.Length > size-offset {
			length = size - offset
		} else {
			length = byteRange.Length
		}
	} else {
		length = size
	}

	return &ctxFileReader{
		ctx:     ctx,
		file:    file,
		section: io.NewSectionReader(file, offset, length),
	}, nil
}

func (l *LocalStorage) Exists(key string) (bool, error) {
	path := filepath.Join(l.root, key)

	_, err := os.Stat(path)
	return err == nil, nil
}

func (l *LocalStorage) State(key string) (oss.OSSState, error) {
	path := filepath.Join(l.root, key)

	info, err := os.Stat(path)
	if err != nil {
		return oss.OSSState{}, err
	}

	return oss.OSSState{Size: info.Size(), LastModified: info.ModTime()}, nil
}

func (l *LocalStorage) List(prefix string) ([]oss.OSSPath, error) {
	paths := make([]oss.OSSPath, 0)
	// check if the patch exists
	exists, err := l.Exists(prefix)
	if err != nil {
		return nil, err
	}
	if !exists {
		return paths, nil
	}
	prefix = filepath.Join(l.root, prefix)

	err = filepath.WalkDir(prefix, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// remove prefix
		path = strings.TrimPrefix(path, prefix)
		if path == "" {
			return nil
		}
		// remove leading slash
		path = strings.TrimPrefix(path, "/")
		paths = append(paths, oss.OSSPath{
			Path:  path,
			IsDir: d.IsDir(),
		})
		return nil
	})

	if err != nil {
		return nil, err
	}

	return paths, nil
}

func (l *LocalStorage) Delete(key string) error {
	path := filepath.Join(l.root, key)

	return os.RemoveAll(path)
}

func (l *LocalStorage) Type() string {
	return oss.OSS_TYPE_LOCAL
}
