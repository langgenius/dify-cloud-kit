package huaweiobs

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

	"github.com/huaweicloud/huaweicloud-sdk-go-obs/obs"
	"github.com/langgenius/dify-cloud-kit/oss"
)

// newTestStorage points an OBS client at a mock server using path-style
// addressing (http://host/bucket/key).
func newTestStorage(t *testing.T, handler http.Handler) (*HuaweiOBSStorage, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	client, err := obs.New("ak", "sk", srv.URL, obs.WithPathStyle(true))
	if err != nil {
		t.Fatalf("obs.New: %v", err)
	}
	return &HuaweiOBSStorage{bucket: "bkt", client: client}, srv
}

// objectHandler serves a fixed body, honoring Range the way OBS does, and
// records the received Range header and request count. HEAD requests answer
// with Content-Length like OBS's GetObjectMetadata.
type objectHandler struct {
	body       []byte
	rangeHdr   *string
	requests   *int32
	ignore     bool
	statusCode int
}

func (h *objectHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.requests != nil {
		atomic.AddInt32(h.requests, 1)
	}
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Length", fmt.Sprint(len(h.body)))
		w.WriteHeader(http.StatusOK)
		return
	}
	if h.statusCode > 0 {
		w.WriteHeader(h.statusCode)
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>err</Message></Error>`, "InvalidRange")
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
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>bad range</Message></Error>`, "InvalidRange")
		return
	}
	if end >= int64(len(h.body)) {
		end = int64(len(h.body)) - 1
	}
	part := h.body[start : end+1]
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(h.body)))
	w.Header().Set("Content-Length", fmt.Sprint(len(part)))
	w.WriteHeader(http.StatusPartialContent)
	w.Write(part)
}

func TestOBSRangeFull(t *testing.T) {
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

func TestOBSRangeBounded(t *testing.T) {
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

func TestOBSRangeOpenEnded(t *testing.T) {
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
	// OBS can only express bounded ranges; open-ended is resolved via a HEAD
	// first, so the GET must carry the computed last byte.
	if rangeHdr != "bytes=4-9" {
		t.Fatalf("got Range header %q", rangeHdr)
	}
}

func TestOBSRangeOpenEndedUnsatisfiable(t *testing.T) {
	var requests int32
	st, _ := newTestStorage(t, &objectHandler{body: []byte("0123456789"), requests: &requests})

	// offset == size: detected locally after the HEAD, no GET is issued.
	_, err := st.Range(context.Background(), "key", &oss.ByteRange{Offset: 10, Length: -1})
	if !errors.Is(err, oss.ErrRangeNotSatisfiable) {
		t.Fatalf("expected ErrRangeNotSatisfiable, got %v", err)
	}
	if requests != 1 {
		t.Fatalf("expected a single HEAD request, got %d", requests)
	}
}

func TestOBSRangeSingleByteUnsatisfiable(t *testing.T) {
	var requests int32
	st, _ := newTestStorage(t, &objectHandler{body: []byte("0123456789"), requests: &requests})

	// A single-byte range cannot be expressed by the SDK (it only sends
	// "bytes=start-end" when end > start); the size check must still map
	// offset >= size to ErrRangeNotSatisfiable.
	_, err := st.Range(context.Background(), "key", &oss.ByteRange{Offset: 10, Length: 1})
	if !errors.Is(err, oss.ErrRangeNotSatisfiable) {
		t.Fatalf("expected ErrRangeNotSatisfiable, got %v", err)
	}
	if requests != 1 {
		t.Fatalf("expected a single HEAD request, got %d", requests)
	}
}

func TestOBSRangeSingleByteSatisfiableFailsLoudly(t *testing.T) {
	st, _ := newTestStorage(t, &objectHandler{body: []byte("0123456789")})

	// Satisfiable single-byte ranges are not expressible via the OBS SDK:
	// the GET is issued without a Range header and the full object comes
	// back, which fails Content-Range verification instead of returning
	// wrong data.
	if _, err := st.Range(context.Background(), "key", &oss.ByteRange{Offset: 2, Length: 1}); err == nil {
		t.Fatal("expected an error for a single-byte range")
	}
}

func TestOBSRangeOversizedEndClamped(t *testing.T) {
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

func TestOBSRangeInvalidRangeNoRequest(t *testing.T) {
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

func TestOBSRangeCanceledBeforeRequest(t *testing.T) {
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

func TestOBSRangeRangeNotSatisfiable(t *testing.T) {
	st, _ := newTestStorage(t, &objectHandler{body: []byte("0123456789")})

	_, err := st.Range(context.Background(), "key", &oss.ByteRange{Offset: 100, Length: 1})
	if !errors.Is(err, oss.ErrRangeNotSatisfiable) {
		t.Fatalf("expected ErrRangeNotSatisfiable, got %v", err)
	}
}

func TestOBSRangeEmptyObject(t *testing.T) {
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

func TestOBSRangeServerIgnoresRange(t *testing.T) {
	st, _ := newTestStorage(t, &objectHandler{body: []byte("0123456789"), ignore: true})

	_, err := st.Range(context.Background(), "key", &oss.ByteRange{Offset: 2, Length: 4})
	if err == nil {
		t.Fatal("expected error when server ignores the range")
	}
}

func TestOBSLoadStillWorks(t *testing.T) {
	st, _ := newTestStorage(t, &objectHandler{body: []byte("hello world")})

	data, err := st.Load("key")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if string(data) != "hello world" {
		t.Fatalf("got %q", data)
	}
}
