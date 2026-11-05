package azureblob

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
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/langgenius/dify-cloud-kit/oss"
)

func newTestStorage(t *testing.T, handler http.Handler) (*AzureBlobStorage, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	client, err := azblob.NewClientWithNoCredential(srv.URL, nil)
	if err != nil {
		t.Fatalf("NewClientWithNoCredential: %v", err)
	}
	return &AzureBlobStorage{client: client, containerName: "cont"}, srv
}

// objectHandler serves a fixed body, honoring x-ms-range the way Azure Blob
// does, and records headers + request count.
type objectHandler struct {
	body       []byte
	rangeHdr   *string
	requests   *int32
	ignore     bool
	statusCode int
	errCode    string
}

func (h *objectHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.requests != nil {
		atomic.AddInt32(h.requests, 1)
	}
	if h.statusCode > 0 {
		w.WriteHeader(h.statusCode)
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>err</Message></Error>`, h.errCode)
		return
	}
	rangeHdr := r.Header.Get("x-ms-range")
	if h.rangeHdr != nil {
		*h.rangeHdr = rangeHdr
	}
	w.Header().Set("ETag", `"etag1"`)
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
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>InvalidRange</Code><Message>bad range</Message></Error>`)
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

func TestAzureRangeFull(t *testing.T) {
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
		t.Fatalf("unexpected x-ms-range header %q", rangeHdr)
	}
}

func TestAzureRangeEmptyObject(t *testing.T) {
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

func TestAzureRangeBounded(t *testing.T) {
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
	if rangeHdr != "bytes=2-5" {
		t.Fatalf("x-ms-range header %q", rangeHdr)
	}
	if string(data) != "2345" {
		t.Fatalf("got %q", data)
	}
}

func TestAzureRangeToEOF(t *testing.T) {
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
	if rangeHdr != "bytes=4-" {
		t.Fatalf("x-ms-range header %q", rangeHdr)
	}
	if string(data) != "456789" {
		t.Fatalf("got %q", data)
	}
}

// TestAzureRangeZeroToEOF verifies ByteRange{0,-1} sends an explicit signed
// range header even though the SDK's HTTPRange{0,0} would omit it, and that
// the same range on an empty object yields 416 -> ErrRangeNotSatisfiable.
func TestAzureRangeZeroToEOF(t *testing.T) {
	var rangeHdr string
	st, _ := newTestStorage(t, &objectHandler{body: []byte("0123456789"), rangeHdr: &rangeHdr})

	rc, err := st.Range(context.Background(), "key", &oss.ByteRange{Offset: 0, Length: -1})
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if rangeHdr != "bytes=0-9223372036854775806" {
		t.Fatalf("x-ms-range header %q, want bytes=0-9223372036854775806", rangeHdr)
	}
	if string(data) != "0123456789" {
		t.Fatalf("got %q", data)
	}

	// empty object + bytes=0- must surface ErrRangeNotSatisfiable
	st2, _ := newTestStorage(t, &objectHandler{body: nil})
	_, err = st2.Range(context.Background(), "key", &oss.ByteRange{Offset: 0, Length: -1})
	if !errors.Is(err, oss.ErrRangeNotSatisfiable) {
		t.Fatalf("expected ErrRangeNotSatisfiable, got %v", err)
	}
}

func TestAzureRangeOversizedEndClamped(t *testing.T) {
	var rangeHdr string
	st, _ := newTestStorage(t, &objectHandler{body: []byte("0123456789"), rangeHdr: &rangeHdr})

	rc, err := st.Range(context.Background(), "key", &oss.ByteRange{Offset: 8, Length: 100})
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if rangeHdr != "bytes=8-107" {
		t.Fatalf("x-ms-range header %q", rangeHdr)
	}
	if string(data) != "89" {
		t.Fatalf("got %q", data)
	}
}

func TestAzureRangeInvalidRangeNoRequest(t *testing.T) {
	var requests int32
	st, _ := newTestStorage(t, &objectHandler{body: []byte("x"), requests: &requests})

	for _, r := range []oss.ByteRange{
		{Offset: -1, Length: 1},
		{Offset: 0, Length: 0},
		{Offset: 0, Length: -2},
		{Offset: 1<<62 + 1, Length: 1 << 62}, // overflowing end
	} {
		if _, err := st.Range(context.Background(), "key", &r); !errors.Is(err, oss.ErrInvalidRange) {
			t.Fatalf("range=%+v: expected ErrInvalidRange, got %v", r, err)
		}
	}
	if n := atomic.LoadInt32(&requests); n != 0 {
		t.Fatalf("expected no requests, got %d", n)
	}
}

func TestAzureRangeMissing(t *testing.T) {
	st, _ := newTestStorage(t, &objectHandler{statusCode: http.StatusNotFound, errCode: "BlobNotFound"})
	if _, err := st.Range(context.Background(), "missing", nil); err == nil {
		t.Fatal("expected error")
	}
}

func TestAzureRangeRangeNotSatisfiable(t *testing.T) {
	st, _ := newTestStorage(t, &objectHandler{statusCode: http.StatusRequestedRangeNotSatisfiable, errCode: "InvalidRange"})
	_, err := st.Range(context.Background(), "key", &oss.ByteRange{Offset: 100, Length: 1})
	if !errors.Is(err, oss.ErrRangeNotSatisfiable) {
		t.Fatalf("expected ErrRangeNotSatisfiable, got %v", err)
	}
}

// TestAzureRangePlain416 verifies HTTP 416 maps to ErrRangeNotSatisfiable even
// without an XML error body.
func TestAzureRangePlain416(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		w.Write([]byte("not xml"))
	}))
	t.Cleanup(srv.Close)
	client, err := azblob.NewClientWithNoCredential(srv.URL, nil)
	if err != nil {
		t.Fatalf("NewClientWithNoCredential: %v", err)
	}
	st := &AzureBlobStorage{client: client, containerName: "cont"}

	_, err = st.Range(context.Background(), "key", &oss.ByteRange{Offset: 100, Length: 1})
	if !errors.Is(err, oss.ErrRangeNotSatisfiable) {
		t.Fatalf("expected ErrRangeNotSatisfiable, got %v", err)
	}
}

// trackingBody records whether it was closed.
type trackingBody struct {
	*strings.Reader
	closed *int32
}

func (b trackingBody) Close() error {
	atomic.StoreInt32(b.closed, 1)
	return nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func newTrackingStorage(t *testing.T, rt http.RoundTripper) *AzureBlobStorage {
	t.Helper()
	client, err := azblob.NewClientWithNoCredential("http://blob.invalid", &azblob.ClientOptions{
		ClientOptions: azcore.ClientOptions{Transport: &http.Client{Transport: rt}},
	})
	if err != nil {
		t.Fatalf("NewClientWithNoCredential: %v", err)
	}
	return &AzureBlobStorage{client: client, containerName: "cont"}
}

// TestAzureRangeRejectedRangeClosesBody ensures the response body is closed
// when a ranged response is rejected for a missing or mismatched
// Content-Range.
func TestAzureRangeRejectedRangeClosesBody(t *testing.T) {
	for _, tc := range []struct {
		name         string
		contentRange string
	}{
		{"missing", ""},
		{"malformed", "garbage"},
		{"wrong start", "bytes 0-3/10"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var closed int32
			rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				hdr := http.Header{"Content-Length": []string{"10"}}
				if tc.contentRange != "" {
					hdr.Set("Content-Range", tc.contentRange)
				}
				return &http.Response{
					StatusCode:    http.StatusOK,
					Status:        "200 OK",
					Header:        hdr,
					Body:          trackingBody{strings.NewReader("0123456789"), &closed},
					ContentLength: 10,
					Request:       r,
				}, nil
			})
			st := newTrackingStorage(t, rt)

			_, err := st.Range(context.Background(), "key", &oss.ByteRange{Offset: 2, Length: 4})
			if err == nil {
				t.Fatal("expected rejection")
			}
			if atomic.LoadInt32(&closed) != 1 {
				t.Fatal("response body was not closed on rejection")
			}
		})
	}
}

func TestAzureRangeCanceledBeforeRequest(t *testing.T) {
	var requests int32
	st, _ := newTestStorage(t, &objectHandler{body: []byte("x"), requests: &requests})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := st.Range(ctx, "key", nil); err == nil {
		t.Fatal("expected error")
	}
	if n := atomic.LoadInt32(&requests); n != 0 {
		t.Fatalf("expected no requests, got %d", n)
	}
}

func TestAzureRangeCanceledInflight(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	client, err := azblob.NewClientWithNoCredential(srv.URL, nil)
	if err != nil {
		t.Fatalf("NewClientWithNoCredential: %v", err)
	}
	st := &AzureBlobStorage{client: client, containerName: "cont"}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := st.Range(ctx, "key", nil)
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("handler never received request")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error after cancel")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Range did not return after cancel")
	}
}

// TestAzureRangeCanceledDuringBodyRead cancels the context after Range
// succeeded while a Read is blocked on an in-flight body.
func TestAzureRangeCanceledDuringBodyRead(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "10")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("abc"))
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	client, err := azblob.NewClientWithNoCredential(srv.URL, nil)
	if err != nil {
		t.Fatalf("NewClientWithNoCredential: %v", err)
	}
	st := &AzureBlobStorage{client: client, containerName: "cont"}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rc, err := st.Range(ctx, "key", nil)
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	defer rc.Close()

	readDone := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(rc, make([]byte, 10))
		readDone <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-readDone:
		if err == nil {
			t.Fatal("expected read error after cancel")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Read did not return after cancel")
	}
}

// TestAzureRangeStreamsBeforeFullDownload proves Range returns after headers
// and first bytes are readable while the rest is still in flight, and that
// Close releases the blocked handler.
func TestAzureRangeStreamsBeforeFullDownload(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Range", "bytes 0-9/10")
		w.Header().Set("Content-Length", "10")
		w.WriteHeader(http.StatusPartialContent)
		w.Write([]byte("abc"))
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	client, err := azblob.NewClientWithNoCredential(srv.URL, nil)
	if err != nil {
		t.Fatalf("NewClientWithNoCredential: %v", err)
	}
	st := &AzureBlobStorage{client: client, containerName: "cont"}

	type res struct {
		rc  io.ReadCloser
		err error
	}
	opened := make(chan res, 1)
	go func() {
		rc, err := st.Range(context.Background(), "key", &oss.ByteRange{Offset: 0, Length: -1})
		opened <- res{rc, err}
	}()

	var rc io.ReadCloser
	select {
	case r := <-opened:
		if r.err != nil {
			t.Fatalf("Range: %v", r.err)
		}
		rc = r.rc
	case <-time.After(5 * time.Second):
		t.Fatal("Range blocked until full body downloaded")
	}
	defer rc.Close()

	buf := make([]byte, 3)
	if _, err := io.ReadFull(rc, buf); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(buf) != "abc" {
		t.Fatalf("got %q", buf)
	}

	if err := rc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	serverDone := make(chan struct{})
	go func() {
		srv.Close()
		close(serverDone)
	}()
	select {
	case <-serverDone:
	case <-time.After(5 * time.Second):
		t.Fatal("server handler still blocked after Close")
	}
}

// retryHandler serves a truncated first response (declares Content-Length
// for the whole object but closes early), then asserts the retry carries an
// advanced x-ms-range and If-Match ETag, and completes the remaining bytes.
type retryHandler struct {
	full       string // full object content
	served     int    // bytes served on the first response
	retried    chan struct{}
	gotRange   string
	gotIfMatch string
	t          *testing.T
}

func (h *retryHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rangeHdr := r.Header.Get("x-ms-range")
	if strings.HasPrefix(rangeHdr, "bytes=0-") || rangeHdr == "" {
		w.Header().Set("ETag", `"etag1"`)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", len(h.full)-1, len(h.full)))
		w.Header().Set("Content-Length", fmt.Sprint(len(h.full)))
		w.WriteHeader(http.StatusPartialContent)
		w.Write([]byte(h.full[:h.served])) // truncated: unexpected EOF for the client
		return
	}
	// retry request
	h.gotRange = rangeHdr
	h.gotIfMatch = r.Header.Get("If-Match")
	var start int64
	fmt.Sscan(strings.TrimSuffix(strings.TrimPrefix(rangeHdr, "bytes="), "-"), &start)
	part := h.full[start:]
	w.Header().Set("ETag", `"etag1"`)
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(h.full)-1, len(h.full)))
	w.Header().Set("Content-Length", fmt.Sprint(len(part)))
	w.WriteHeader(http.StatusPartialContent)
	w.Write([]byte(part))
	close(h.retried)
}

func runRetryTest(t *testing.T, byteRange *oss.ByteRange, body string, served int, wantRetryRange string) string {
	t.Helper()
	h := &retryHandler{full: body, served: served, retried: make(chan struct{}), t: t}
	st, _ := newTestStorage(t, h)

	rc, err := st.Range(context.Background(), "key", byteRange)
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	defer rc.Close()

	readDone := make(chan struct {
		data []byte
		err  error
	}, 1)
	go func() {
		data, err := io.ReadAll(rc)
		readDone <- struct {
			data []byte
			err  error
		}{data, err}
	}()

	select {
	case <-h.retried:
	case <-time.After(10 * time.Second):
		t.Fatal("retry never issued")
	}
	res := <-readDone
	if res.err != nil {
		t.Fatalf("ReadAll: %v", res.err)
	}
	if h.gotIfMatch != `"etag1"` {
		t.Fatalf("retry If-Match %q", h.gotIfMatch)
	}
	if h.gotRange != wantRetryRange {
		t.Fatalf("retry x-ms-range %q, want %q", h.gotRange, wantRetryRange)
	}
	return string(res.data)
}

func TestAzureRetryReaderAdvancesRange(t *testing.T) {
	// bounded range 0-9 of a 10-byte object; first response truncates at 3
	// bytes, retry must advance the offset while keeping the requested end.
	got := runRetryTest(t, &oss.ByteRange{Offset: 0, Length: 10}, "0123456789", 3, "bytes=3-9")
	if got != "0123456789" {
		t.Fatalf("got %q", got)
	}
}

// TestAzureRetryAfterZeroToEOF proves retries after a {0,-1} request advance
// the range correctly instead of re-requesting from 0.
func TestAzureRetryAfterZeroToEOF(t *testing.T) {
	got := runRetryTest(t, &oss.ByteRange{Offset: 0, Length: -1}, "0123456789", 3, "bytes=3-9223372036854775806")
	if got != "0123456789" {
		t.Fatalf("got %q", got)
	}
}

func TestAzureLoadStillWorks(t *testing.T) {
	st, _ := newTestStorage(t, &objectHandler{body: []byte("payload")})
	data, err := st.Load("key")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if string(data) != "payload" {
		t.Fatalf("got %q", data)
	}
}

// TestAzureLoadClosesBodyOnReadError ensures a failed read still closes the
// body through Load's deferred Close.
func TestAzureLoadClosesBodyOnReadError(t *testing.T) {
	var closed int32
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Status:        "200 OK",
			Header:        http.Header{"Content-Length": []string{"10"}},
			Body:          failingBody{Reader: strings.NewReader(""), failAfter: 0, closed: &closed},
			ContentLength: 10,
			Request:       r,
		}, nil
	})
	st := newTrackingStorage(t, rt)

	// Reads fail immediately; the retry reader must close each failed body
	// before retrying, and Load returns the error.
	_, err := st.Load("key")
	if err == nil {
		t.Fatal("expected Load to fail")
	}
	if atomic.LoadInt32(&closed) == 0 {
		t.Fatal("response body was not closed on read failure")
	}
}

// failingBody errors after failAfter successful reads.
type failingBody struct {
	*strings.Reader
	failAfter int
	reads     int
	closed    *int32
}

func (b failingBody) Read(p []byte) (int, error) {
	b.reads++
	if b.reads > b.failAfter {
		return 0, fmt.Errorf("simulated read failure")
	}
	return b.Reader.Read(p)
}

func (b failingBody) Close() error {
	atomic.AddInt32(b.closed, 1)
	return nil
}
