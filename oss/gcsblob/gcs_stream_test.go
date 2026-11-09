package gcsblob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"cloud.google.com/go/storage"
	"github.com/langgenius/dify-cloud-kit/oss"
)

// newTestStorage points a GCS client at a mock server via the
// STORAGE_EMULATOR_HOST convention (the SDK routes both XML reads and JSON
// API calls there and disables authentication).
func newTestStorage(t *testing.T, handler http.Handler) (*GoogleCloudStorage, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	t.Setenv("STORAGE_EMULATOR_HOST", srv.URL)
	client, err := storage.NewClient(context.Background())
	if err != nil {
		t.Fatalf("storage.NewClient: %v", err)
	}
	t.Cleanup(func() { client.Close() })
	return &GoogleCloudStorage{bucket: "bkt", client: client}, srv
}

// objectHandler serves a fixed body, honoring Range the way GCS does, and
// records the received Range header and request count.
type objectHandler struct {
	body       []byte
	rangeHdr   *string
	requests   *int32
	ignore     bool // respond 200 with full body regardless of Range
	statusCode int  // if >0, respond with this status + XML error instead
}

func (h *objectHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.requests != nil {
		atomic.AddInt32(h.requests, 1)
	}
	if h.statusCode > 0 {
		w.WriteHeader(h.statusCode)
		fmt.Fprint(w, `<?xml version="1.0"?><Error><Code>InvalidRange</Code><Message>bad range</Message></Error>`)
		return
	}
	rangeHdr := r.Header.Get("Range")
	if h.rangeHdr != nil {
		*h.rangeHdr = rangeHdr
	}
	if rangeHdr == "" || h.ignore {
		w.Header().Set("Content-Length", fmt.Sprint(len(h.body)))
		w.WriteHeader(http.StatusOK)
		w.Write(h.body)
		return
	}
	// parse "bytes=start-end" / "bytes=start-"
	var start, end int64
	spec := strings.TrimPrefix(rangeHdr, "bytes=")
	if dash := strings.Index(spec, "-"); dash >= 0 {
		fmt.Sscan(spec[:dash], &start)
		if spec[dash+1:] == "" {
			end = int64(len(h.body)) - 1
		} else {
			fmt.Sscan(spec[dash+1:], &end)
		}
	}
	if start >= int64(len(h.body)) {
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		fmt.Fprint(w, `<?xml version="1.0"?><Error><Code>InvalidRange</Code><Message>bad range</Message></Error>`)
		return
	}
	// clamp like a real GCS backend does for ranges extending past EOF
	if end >= int64(len(h.body)) {
		end = int64(len(h.body)) - 1
	}
	part := h.body[start : end+1]
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(h.body)))
	w.Header().Set("Content-Length", fmt.Sprint(len(part)))
	w.WriteHeader(http.StatusPartialContent)
	w.Write(part)
}

func TestGCSRangeFull(t *testing.T) {
	var rangeHdr string
	st, _ := newTestStorage(t, &objectHandler{body: []byte("hello world"), rangeHdr: &rangeHdr})

	rc, err := st.Range(context.Background(), "key", nil)
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(data) != "hello world" {
		t.Fatalf("got %q", data)
	}
	if rangeHdr != "" {
		t.Fatalf("unexpected Range header %q", rangeHdr)
	}
}

func TestGCSRangeBounded(t *testing.T) {
	var rangeHdr string
	st, _ := newTestStorage(t, &objectHandler{body: []byte("0123456789"), rangeHdr: &rangeHdr})

	rc, err := st.Range(context.Background(), "key", &oss.ByteRange{Offset: 2, Length: 4})
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(data) != "2345" {
		t.Fatalf("got %q", data)
	}
	if rangeHdr != "bytes=2-5" {
		t.Fatalf("got Range header %q", rangeHdr)
	}
}

func TestGCSRangeOpenEnded(t *testing.T) {
	var rangeHdr string
	st, _ := newTestStorage(t, &objectHandler{body: []byte("0123456789"), rangeHdr: &rangeHdr})

	rc, err := st.Range(context.Background(), "key", &oss.ByteRange{Offset: 4, Length: -1})
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(data) != "456789" {
		t.Fatalf("got %q", data)
	}
	if rangeHdr != "bytes=4-" {
		t.Fatalf("got Range header %q", rangeHdr)
	}
}

func TestGCSRangeOversizedEndClamped(t *testing.T) {
	st, _ := newTestStorage(t, &objectHandler{body: []byte("0123456789")})

	rc, err := st.Range(context.Background(), "key", &oss.ByteRange{Offset: 8, Length: 100})
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(data) != "89" {
		t.Fatalf("got %q", data)
	}
}

func TestGCSRangeInvalidRangeNoRequest(t *testing.T) {
	var requests int32
	st, _ := newTestStorage(t, &objectHandler{body: []byte("0123456789"), requests: &requests})

	for _, r := range []oss.ByteRange{
		{Offset: -1, Length: 1},
		{Offset: 0, Length: 0},
		{Offset: 0, Length: -2},
	} {
		if _, err := st.Range(context.Background(), "key", &r); !errors.Is(err, oss.ErrInvalidRange) {
			t.Fatalf("range=%+v: expected ErrInvalidRange, got %v", r, err)
		}
	}
	if requests != 0 {
		t.Fatalf("expected no request, got %d", requests)
	}
}

func TestGCSRangeCanceledBeforeRequest(t *testing.T) {
	var requests int32
	st, _ := newTestStorage(t, &objectHandler{body: []byte("0123456789"), requests: &requests})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := st.Range(ctx, "key", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if requests != 0 {
		t.Fatalf("expected no request, got %d", requests)
	}
}

func TestGCSRangeRangeNotSatisfiable(t *testing.T) {
	st, _ := newTestStorage(t, &objectHandler{body: []byte("0123456789")})

	_, err := st.Range(context.Background(), "key", &oss.ByteRange{Offset: 100, Length: 1})
	if !errors.Is(err, oss.ErrRangeNotSatisfiable) {
		t.Fatalf("expected ErrRangeNotSatisfiable, got %v", err)
	}
}

func TestGCSRangeEmptyObject(t *testing.T) {
	st, _ := newTestStorage(t, &objectHandler{body: nil})

	rc, err := st.Range(context.Background(), "key", nil)
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(data) != 0 {
		t.Fatalf("expected empty body, got %q", data)
	}
}

func TestGCSRangeEmptyObjectToEOFSatisfiedAs416(t *testing.T) {
	// Some emulators answer an open-ended range on an empty object with
	// 200 + empty body instead of 416; the library must still report
	// ErrRangeNotSatisfiable.
	st, _ := newTestStorage(t, &objectHandler{body: nil, ignore: true})

	_, err := st.Range(context.Background(), "key", &oss.ByteRange{Offset: 0, Length: -1})
	if !errors.Is(err, oss.ErrRangeNotSatisfiable) {
		t.Fatalf("expected ErrRangeNotSatisfiable, got %v", err)
	}
}

func TestGCSRangeServerIgnoresRange(t *testing.T) {
	st, _ := newTestStorage(t, &objectHandler{body: []byte("0123456789"), ignore: true})

	_, err := st.Range(context.Background(), "key", &oss.ByteRange{Offset: 2, Length: 4})
	if err == nil {
		t.Fatal("expected error when server ignores the range")
	}
}

func TestGCSLoadStillWorks(t *testing.T) {
	st, _ := newTestStorage(t, &objectHandler{body: []byte("hello world")})

	data, err := st.Load("key")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if string(data) != "hello world" {
		t.Fatalf("got %q", data)
	}
}
