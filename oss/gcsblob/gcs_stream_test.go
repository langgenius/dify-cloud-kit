package gcsblob

import (
	"bytes"
	"compress/gzip"
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
	chunked    bool // omit Content-Length to force chunked transfer encoding
	statusCode int  // if >0, respond with this status + XML error instead
}

// write emits body, leaving Content-Length out when chunked is set so the
// response is delimited by chunked transfer encoding instead.
func (h *objectHandler) write(w http.ResponseWriter, status int, contentRange string, body []byte) {
	if contentRange != "" {
		w.Header().Set("Content-Range", contentRange)
	}
	if !h.chunked {
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
	}
	w.WriteHeader(status)
	if h.chunked {
		// Commit the headers before the body so the server does not
		// buffer the response and add a Content-Length.
		w.(http.Flusher).Flush()
	}
	w.Write(body)
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
		h.write(w, http.StatusOK, "", h.body)
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
	h.write(w, http.StatusPartialContent, fmt.Sprintf("bytes %d-%d/%d", start, end, len(h.body)), h.body[start:end+1])
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
	for _, chunked := range []bool{false, true} {
		t.Run(fmt.Sprintf("chunked=%t", chunked), func(t *testing.T) {
			st, _ := newTestStorage(t, &objectHandler{ignore: true, chunked: chunked})
			rc, err := st.Range(t.Context(), "key", &oss.ByteRange{Offset: 0, Length: -1})
			if rc != nil {
				rc.Close()
			}
			if !errors.Is(err, oss.ErrRangeNotSatisfiable) {
				t.Fatalf("expected ErrRangeNotSatisfiable, got %v", err)
			}
		})
	}
}

func TestGCSRangeServerIgnoresRange(t *testing.T) {
	st, _ := newTestStorage(t, &objectHandler{body: []byte("0123456789"), ignore: true})

	_, err := st.Range(context.Background(), "key", &oss.ByteRange{Offset: 2, Length: 4})
	if err == nil {
		t.Fatal("expected error when server ignores the range")
	}
}

// TestGCSRangeChunkedHonored covers valid responses without Content-Length,
// including range lengths clamped to the object size.
func TestGCSRangeChunkedHonored(t *testing.T) {
	var rangeHdr string
	st, _ := newTestStorage(t, &objectHandler{body: []byte("0123456789"), rangeHdr: &rangeHdr, chunked: true})

	tests := []struct {
		name    string
		r       *oss.ByteRange
		want    string
		wantHdr string
	}{
		{name: "bounded", r: &oss.ByteRange{Offset: 2, Length: 4}, want: "2345", wantHdr: "bytes=2-5"},
		{name: "open ended", r: &oss.ByteRange{Offset: 4, Length: -1}, want: "456789", wantHdr: "bytes=4-"},
		{name: "clamped end", r: &oss.ByteRange{Offset: 8, Length: 100}, want: "89", wantHdr: "bytes=8-107"},
		{name: "zero to EOF", r: &oss.ByteRange{Offset: 0, Length: -1}, want: "0123456789"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rc, err := st.Range(context.Background(), "key", tt.r)
			if err != nil {
				t.Fatalf("Range(%+v): %v", *tt.r, err)
			}
			defer rc.Close()
			data, err := io.ReadAll(rc)
			if err != nil {
				t.Fatalf("ReadAll: %v", err)
			}
			if string(data) != tt.want {
				t.Fatalf("got %q, want %q", data, tt.want)
			}
			if rangeHdr != tt.wantHdr {
				t.Fatalf("got Range header %q, want %q", rangeHdr, tt.wantHdr)
			}
		})
	}
}

func TestGCSRangeChunkedWrongLength(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"too long", "23456789", "2345"},
		{"too short", "23", "23"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, _ := newTestStorage(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Range", fmt.Sprintf("bytes 2-%d/10", 1+len(tc.body)))
				w.WriteHeader(http.StatusPartialContent)
				w.(http.Flusher).Flush()
				w.Write([]byte(tc.body))
			}))
			rc, err := st.Range(context.Background(), "key", &oss.ByteRange{Offset: 2, Length: 4})
			if err != nil {
				t.Fatalf("Range: %v", err)
			}
			defer rc.Close()
			got, err := io.ReadAll(rc)
			if err == nil {
				t.Fatal("expected range length error")
			}
			if tc.name == "too short" && !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("got %v, want unexpected EOF", err)
			}
			if string(got) != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestGCSRangeChunkedIgnored covers a 200 with no Content-Length for a ranged
// request, regardless of why the response was re-framed. The whole object is
// streamed, so only a whole-object request may be served.
func TestGCSRangeChunkedIgnored(t *testing.T) {
	st, _ := newTestStorage(t, &objectHandler{body: []byte("0123456789"), ignore: true, chunked: true})

	for _, r := range []oss.ByteRange{
		{Offset: 0, Length: 5},
		{Offset: 0, Length: 1},
		{Offset: 2, Length: 4},
		{Offset: 2, Length: -1},
	} {
		rc, err := st.Range(context.Background(), "key", &r)
		if rc != nil {
			rc.Close()
		}
		if err == nil {
			t.Fatalf("range=%+v: expected error when the server ignores the range", r)
		}
	}

	rc, err := st.Range(context.Background(), "key", &oss.ByteRange{Offset: 0, Length: -1})
	if err != nil {
		t.Fatalf("whole-object Range: %v", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(data) != "0123456789" {
		t.Fatalf("got %q", data)
	}
}

// transcodingHandler serves the response GCS produces for an object stored
// with Content-Encoding: gzip when it is decompressively transcoded: the body
// is decompressed, Content-Encoding and Content-Length are dropped, and the
// stored metadata is reported through the X-Goog-Stored-* headers. Range
// requests are silently ignored, as documented.
type transcodingHandler struct {
	stored []byte // object bytes as stored (gzip-compressed)
	served []byte // object bytes as served (decompressed)
}

func (h *transcodingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("X-Goog-Stored-Content-Encoding", "gzip")
	w.Header().Set("X-Goog-Stored-Content-Length", fmt.Sprint(len(h.stored)))
	w.WriteHeader(http.StatusOK)
	// No Content-Length: commit the headers first so the body is chunked.
	w.(http.Flusher).Flush()
	w.Write(h.served)
}

// gzipFixture compresses served and returns the stored bytes.
func gzipFixture(t *testing.T, served []byte) []byte {
	t.Helper()
	var stored bytes.Buffer
	zw := gzip.NewWriter(&stored)
	if _, err := zw.Write(served); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	if stored.Len() == len(served) {
		t.Fatalf("fixture is not compressed: stored %d, served %d", stored.Len(), len(served))
	}
	return stored.Bytes()
}

func TestGCSRangeRejectsTranscodedResponse(t *testing.T) {
	served := bytes.Repeat([]byte("dify"), 1024)
	stored := gzipFixture(t, served)
	st, _ := newTestStorage(t, &transcodingHandler{stored: stored, served: served})

	for _, r := range []*oss.ByteRange{nil, {Offset: 0, Length: -1}, {Offset: 2, Length: 4}} {
		rc, err := st.Range(context.Background(), "key", r)
		if rc != nil {
			rc.Close()
		}
		if rc != nil || err == nil || !strings.Contains(err.Error(), "despite requesting stored bytes") {
			t.Fatalf("Range(%+v): expected transcoding error and no reader, got %v", r, err)
		}
	}
}

func TestGCSRangeReturnsStoredGzipBytes(t *testing.T) {
	served := bytes.Repeat([]byte("dify"), 1024)
	stored := gzipFixture(t, served)
	object := &objectHandler{body: stored}
	st, _ := newTestStorage(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept-Encoding") != "gzip" {
			http.Error(w, "expected Accept-Encoding: gzip", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		object.ServeHTTP(w, r)
	}))

	for _, tc := range []struct {
		name string
		r    *oss.ByteRange
		want []byte
	}{
		{"full stream", nil, stored},
		{"zero to EOF", &oss.ByteRange{Offset: 0, Length: -1}, stored},
		{"bounded range", &oss.ByteRange{Offset: 2, Length: 4}, stored[2:6]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rc, err := st.Range(context.Background(), "key", tc.r)
			if err != nil {
				t.Fatalf("Range: %v", err)
			}
			defer rc.Close()
			got, err := io.ReadAll(rc)
			if err != nil {
				t.Fatalf("ReadAll: %v", err)
			}
			if !bytes.Equal(got, tc.want) {
				t.Fatalf("got %x, want %x", got, tc.want)
			}
		})
	}

	got, err := st.Load("key")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !bytes.Equal(got, served) {
		t.Fatalf("Load returned %d bytes, want %d decompressed bytes", len(got), len(served))
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
