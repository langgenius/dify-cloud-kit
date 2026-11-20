package azureblob

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/langgenius/dify-cloud-kit/oss"
)

// newPrepareStorage builds a client pointed at the httptest server with SDK
// retries disabled so error-status tests return promptly.
func newPrepareStorage(t *testing.T, handler http.Handler) *AzureBlobStorage {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client, err := azblob.NewClientWithNoCredential(srv.URL, &azblob.ClientOptions{
		ClientOptions: azcore.ClientOptions{
			Retry: policy.RetryOptions{MaxRetries: -1},
		},
	})
	if err != nil {
		t.Fatalf("NewClientWithNoCredential: %v", err)
	}
	return &AzureBlobStorage{client: client, containerName: "cont"}
}

// containerHandler emulates PUT /<container>?restype=container.
type containerHandler struct {
	statuses   []int // statuses to serve in order; last one repeats
	codes      []string
	requests   *int32
	gotMethod  *string
	gotQuery   *string
	call       int32 // incremented per request
	failStatus int   // if >0, always serve this status+errCode
	errCode    string
}

func (h *containerHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.requests != nil {
		atomic.AddInt32(h.requests, 1)
	}
	if h.gotMethod != nil {
		*h.gotMethod = r.Method
	}
	if h.gotQuery != nil {
		*h.gotQuery = r.URL.RawQuery
	}
	status := h.failStatus
	code := h.errCode
	if len(h.statuses) > 0 {
		i := int(atomic.AddInt32(&h.call, 1)) - 1
		if i >= len(h.statuses) {
			i = len(h.statuses) - 1
		}
		status = h.statuses[i]
		if i < len(h.codes) {
			code = h.codes[i]
		}
	}
	if status >= 400 {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(status)
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>err</Message></Error>`, code)
		return
	}
	w.WriteHeader(status)
}

func TestAzurePrepareCreatesContainer(t *testing.T) {
	var requests int32
	var method, query string
	st := newPrepareStorage(t, &containerHandler{
		statuses: []int{http.StatusCreated},
		requests: &requests, gotMethod: &method, gotQuery: &query,
	})

	if err := st.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if method != http.MethodPut || !strings.Contains(query, "restype=container") {
		t.Fatalf("unexpected request %s ?%s", method, query)
	}
	if n := atomic.LoadInt32(&requests); n != 1 {
		t.Fatalf("expected 1 request, got %d", n)
	}
}

func TestAzurePrepareAlreadyExistsIdempotent(t *testing.T) {
	st := newPrepareStorage(t, &containerHandler{
		statuses: []int{http.StatusCreated, http.StatusConflict},
		codes:    []string{"", "ContainerAlreadyExists"},
	})
	if err := st.Prepare(context.Background()); err != nil {
		t.Fatalf("first Prepare: %v", err)
	}
	if err := st.Prepare(context.Background()); err != nil {
		t.Fatalf("second Prepare (ContainerAlreadyExists): %v", err)
	}
}

func TestAzurePrepareErrorPropagation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		code   string
	}{
		{"conflict being deleted", http.StatusConflict, "ContainerBeingDeleted"},
		{"forbidden", http.StatusForbidden, "AuthorizationFailure"},
		{"server error", http.StatusInternalServerError, "InternalError"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newPrepareStorage(t, &containerHandler{failStatus: tc.status, errCode: tc.code})
			err := st.Prepare(context.Background())
			if err == nil {
				t.Fatal("expected error")
			}
			var respErr *azcore.ResponseError
			if !errors.As(err, &respErr) {
				t.Fatalf("expected *azcore.ResponseError, got %v", err)
			}
			if respErr.StatusCode != tc.status || respErr.ErrorCode != tc.code {
				t.Fatalf("got %d/%s, want %d/%s", respErr.StatusCode, respErr.ErrorCode, tc.status, tc.code)
			}
		})
	}
}

func TestAzurePrepareCanceledBeforeRequest(t *testing.T) {
	var requests int32
	st := newPrepareStorage(t, &containerHandler{statuses: []int{http.StatusCreated}, requests: &requests})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := st.Prepare(ctx); err == nil {
		t.Fatal("expected error")
	}
	if n := atomic.LoadInt32(&requests); n != 0 {
		t.Fatalf("expected no requests, got %d", n)
	}
}

func TestAzurePrepareCanceledInflight(t *testing.T) {
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
	client, err := azblob.NewClientWithNoCredential(srv.URL, &azblob.ClientOptions{
		ClientOptions: azcore.ClientOptions{Retry: policy.RetryOptions{MaxRetries: -1}},
	})
	if err != nil {
		t.Fatalf("NewClientWithNoCredential: %v", err)
	}
	st := &AzureBlobStorage{client: client, containerName: "cont"}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- st.Prepare(ctx)
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
		t.Fatal("Prepare did not return after cancel")
	}
}

func TestAzurePrepareConcurrentIdempotent(t *testing.T) {
	var requests int32
	st := newPrepareStorage(t, &containerHandler{
		statuses: []int{http.StatusCreated, http.StatusConflict},
		codes:    []string{"", "ContainerAlreadyExists"},
		requests: &requests,
	})

	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- st.Prepare(context.Background())
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Prepare: %v", err)
		}
	}
	if n := atomic.LoadInt32(&requests); n != 4 {
		t.Fatalf("expected 4 requests, got %d", n)
	}
}

func TestFactoryDoesNotPrepare(t *testing.T) {
	var requests int32
	srv := httptest.NewServer(&containerHandler{requests: &requests})
	t.Cleanup(srv.Close)

	connStr := "DefaultEndpointsProtocol=http;AccountName=devstoreaccount1;" +
		"AccountKey=Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw==;" +
		"BlobEndpoint=" + srv.URL + "/devstoreaccount1;"

	store, err := NewAzureBlobStorage(oss.OSSArgs{
		AzureBlob: &oss.AzureBlob{
			ConnectionString: connStr,
			ContainerName:    "cont",
		},
	})
	if err != nil {
		t.Fatalf("NewAzureBlobStorage: %v", err)
	}
	if !oss.SupportsPrepare(store) {
		t.Fatal("expected SupportsPrepare true")
	}
	if n := atomic.LoadInt32(&requests); n != 0 {
		t.Fatalf("constructor made %d requests", n)
	}
}
