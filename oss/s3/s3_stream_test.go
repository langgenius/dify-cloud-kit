package s3

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

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/langgenius/dify-cloud-kit/oss"
)

func newTestStorage(t *testing.T, handler http.Handler) (*S3Storage, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	client := s3.New(s3.Options{
		Region:       "us-east-1",
		Credentials:  aws.AnonymousCredentials{},
		BaseEndpoint: aws.String(srv.URL),
		UsePathStyle: true,
	})
	return &S3Storage{bucket: "bkt", client: client}, srv
}

// objectHandler serves a fixed body, honoring Range the way S3 does, and
// records the received Range header and request count.
type objectHandler struct {
	body       []byte
	rangeHdr   *string
	requests   *int32
	ignore     bool // respond 200 with full body regardless of Range
	statusCode int  // if >0, respond with this status + XML error instead
	errCode    string
}

func (h *objectHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.requests != nil {
		atomic.AddInt32(h.requests, 1)
	}
	if h.statusCode > 0 {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(h.statusCode)
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>err</Message></Error>`, h.errCode)
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
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>InvalidRange</Code><Message>bad range</Message></Error>`)
		return
	}
	// clamp like a real S3 backend does for ranges extending past EOF
	if end >= int64(len(h.body)) {
		end = int64(len(h.body)) - 1
	}
	part := h.body[start : end+1]
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(h.body)))
	w.Header().Set("Content-Length", fmt.Sprint(len(part)))
	w.WriteHeader(http.StatusPartialContent)
	w.Write(part)
}

func TestS3RangeFull(t *testing.T) {
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

func TestS3RangeBounded(t *testing.T) {
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
		t.Fatalf("Range header %q", rangeHdr)
	}
	if string(data) != "2345" {
		t.Fatalf("got %q", data)
	}
}

func TestS3RangeOpenEnded(t *testing.T) {
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
		t.Fatalf("Range header %q", rangeHdr)
	}
	if string(data) != "456789" {
		t.Fatalf("got %q", data)
	}
}

func TestS3RangeInvalidRangeNoRequest(t *testing.T) {
	var requests int32
	st, _ := newTestStorage(t, &objectHandler{body: []byte("x"), requests: &requests})

	for _, r := range []oss.ByteRange{
		{Offset: -1, Length: 1},
		{Offset: 0, Length: 0},
		{Offset: 0, Length: -2},
	} {
		if _, err := st.Range(context.Background(), "key", &r); !errors.Is(err, oss.ErrInvalidRange) {
			t.Fatalf("range=%+v: expected ErrInvalidRange, got %v", r, err)
		}
	}
	if n := atomic.LoadInt32(&requests); n != 0 {
		t.Fatalf("expected no requests, got %d", n)
	}
}

func TestS3RangeCanceledBeforeRequest(t *testing.T) {
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

func TestS3RangeCanceledInflight(t *testing.T) {
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
	client := s3.New(s3.Options{
		Region:       "us-east-1",
		Credentials:  aws.AnonymousCredentials{},
		BaseEndpoint: aws.String(srv.URL),
		UsePathStyle: true,
	})
	st := &S3Storage{bucket: "bkt", client: client}

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
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Range did not return after cancel")
	}
}

// TestS3RangeCanceledDuringBodyRead cancels the context after Range succeeded
// while a Read is blocked on an in-flight body.
func TestS3RangeCanceledDuringBodyRead(t *testing.T) {
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
	client := s3.New(s3.Options{
		Region:       "us-east-1",
		Credentials:  aws.AnonymousCredentials{},
		BaseEndpoint: aws.String(srv.URL),
		UsePathStyle: true,
	})
	st := &S3Storage{bucket: "bkt", client: client}

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

func TestS3RangeMissing(t *testing.T) {
	st, _ := newTestStorage(t, &objectHandler{statusCode: http.StatusNotFound, errCode: "NoSuchKey"})
	if _, err := st.Range(context.Background(), "missing", nil); err == nil {
		t.Fatal("expected error")
	}
}

func TestS3RangeRangeNotSatisfiable(t *testing.T) {
	st, _ := newTestStorage(t, &objectHandler{statusCode: http.StatusRequestedRangeNotSatisfiable, errCode: "InvalidRange"})
	_, err := st.Range(context.Background(), "key", &oss.ByteRange{Offset: 100, Length: 1})
	if !errors.Is(err, oss.ErrRangeNotSatisfiable) {
		t.Fatalf("expected ErrRangeNotSatisfiable, got %v", err)
	}
}

// TestS3RangePlain416 verifies HTTP 416 maps to ErrRangeNotSatisfiable even
// when the error body carries no InvalidRange code.
func TestS3RangePlain416(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		w.Write([]byte("not xml"))
	}))
	t.Cleanup(srv.Close)
	client := s3.New(s3.Options{
		Region:       "us-east-1",
		Credentials:  aws.AnonymousCredentials{},
		BaseEndpoint: aws.String(srv.URL),
		UsePathStyle: true,
	})
	st := &S3Storage{bucket: "bkt", client: client}

	_, err := st.Range(context.Background(), "key", &oss.ByteRange{Offset: 100, Length: 1})
	if !errors.Is(err, oss.ErrRangeNotSatisfiable) {
		t.Fatalf("expected ErrRangeNotSatisfiable, got %v", err)
	}
}

// TestS3RangeOversizedEndClamped verifies a bounded range extending past EOF
// is accepted when the server clamps the end to the last byte.
func TestS3RangeOversizedEndClamped(t *testing.T) {
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
		t.Fatalf("Range header %q", rangeHdr)
	}
	if string(data) != "89" {
		t.Fatalf("got %q", data)
	}
}

func TestS3RangeEmptyObject(t *testing.T) {
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

// TestS3RangeRejectedRangeClosesBody ensures the response body is closed when
// a ranged response is rejected for missing Content-Range.
func TestS3RangeRejectedRangeClosesBody(t *testing.T) {
	var closed int32
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Status:        "200 OK",
			Header:        http.Header{"Content-Length": []string{"10"}},
			Body:          trackingBody{strings.NewReader("0123456789"), &closed},
			ContentLength: 10,
			Request:       r,
		}, nil
	})
	client := s3.New(s3.Options{
		Region:      "us-east-1",
		Credentials: aws.AnonymousCredentials{},
		HTTPClient:  &http.Client{Transport: rt},
		EndpointResolver: s3.EndpointResolverFunc(func(region string, o s3.EndpointResolverOptions) (aws.Endpoint, error) {
			return aws.Endpoint{URL: "http://s3.invalid"}, nil
		}),
	})
	st := &S3Storage{bucket: "bkt", client: client}

	_, err := st.Range(context.Background(), "key", &oss.ByteRange{Offset: 2, Length: 4})
	if err == nil {
		t.Fatal("expected rejection of range-ignoring response")
	}
	if atomic.LoadInt32(&closed) != 1 {
		t.Fatal("response body was not closed on rejection")
	}
}

func TestS3RangeServerIgnoresRange(t *testing.T) {
	st, _ := newTestStorage(t, &objectHandler{body: []byte("0123456789"), ignore: true})
	rc, err := st.Range(context.Background(), "key", &oss.ByteRange{Offset: 2, Length: 4})
	if err == nil {
		rc.Close()
		t.Fatal("expected error when server ignores Range")
	}
}

// TestS3RangeStreamsBeforeFullDownload proves Range returns after headers and
// the first bytes are readable while the rest of the body is still in flight.
func TestS3RangeStreamsBeforeFullDownload(t *testing.T) {
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

	client := s3.New(s3.Options{
		Region:       "us-east-1",
		Credentials:  aws.AnonymousCredentials{},
		BaseEndpoint: aws.String(srv.URL),
		UsePathStyle: true,
	})
	st := &S3Storage{bucket: "bkt", client: client}

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

	// Closing the reader must release the in-flight server handler.
	if err := rc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	serverDone := make(chan struct{})
	go func() {
		srv.Close() // waits for handlers to return
		close(serverDone)
	}()
	select {
	case <-serverDone:
	case <-time.After(5 * time.Second):
		t.Fatal("server handler still blocked after Close")
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

// TestS3LoadClosesBody proves the legacy Load no longer leaks the HTTP body.
func TestS3LoadClosesBody(t *testing.T) {
	var closed int32
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Status:        "200 OK",
			Header:        http.Header{"Content-Length": []string{"7"}},
			Body:          trackingBody{strings.NewReader("payload"), &closed},
			ContentLength: 7,
			Request:       r,
		}, nil
	})
	client := s3.New(s3.Options{
		Region:      "us-east-1",
		Credentials: aws.AnonymousCredentials{},
		HTTPClient:  &http.Client{Transport: rt},
		EndpointResolver: s3.EndpointResolverFunc(func(region string, o s3.EndpointResolverOptions) (aws.Endpoint, error) {
			return aws.Endpoint{URL: "http://s3.invalid"}, nil
		}),
	})
	st := &S3Storage{bucket: "bkt", client: client}

	data, err := st.Load("key")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if string(data) != "payload" {
		t.Fatalf("got %q", data)
	}
	if atomic.LoadInt32(&closed) != 1 {
		t.Fatal("response body was not closed")
	}
}
