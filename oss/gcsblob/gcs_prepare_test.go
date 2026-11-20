package gcsblob

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// bucketHandler answers the JSON API bucket-create endpoint: the first POST
// creates the bucket, subsequent ones conflict.
type bucketHandler struct {
	creates *int32
}

func (h *bucketHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/storage/v1/b") {
		if h.creates != nil {
			atomic.AddInt32(h.creates, 1)
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"kind":"storage#bucket","name":"bkt"}`)
		return
	}
	w.WriteHeader(http.StatusNotFound)
}

// prepareHandler serves the object-list access probe and bucket create.
// listOK reports whether the bucket is accessible; createCode is the status
// for bucket creation (200 marks the bucket accessible afterwards).
type prepareHandler struct {
	listOK     atomic.Bool
	createCode int
	lists      int32
	creates    int32
}

func (h *prepareHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/o"):
		atomic.AddInt32(&h.lists, 1)
		if h.listOK.Load() {
			fmt.Fprint(w, `{"kind":"storage#objects"}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":{"code":404,"message":"The specified bucket does not exist."}}`)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/storage/v1/b"):
		atomic.AddInt32(&h.creates, 1)
		if h.createCode == http.StatusOK {
			h.listOK.Store(true)
			fmt.Fprint(w, `{"kind":"storage#bucket","name":"bkt"}`)
			return
		}
		w.WriteHeader(h.createCode)
		fmt.Fprintf(w, `{"error":{"code":%d,"message":"The requested bucket name is not available."}}`, h.createCode)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestGCSPrepareCreatesBucket(t *testing.T) {
	h := &prepareHandler{createCode: http.StatusOK}
	st, _ := newTestStorage(t, h)
	st.project = "test-project"

	if err := st.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if err := st.Prepare(context.Background()); err != nil {
		t.Fatalf("second Prepare: %v", err)
	}
	if h.creates != 1 {
		t.Fatalf("expected 1 create call, got %d", h.creates)
	}
}

func TestGCSPrepareExistingBucketSkipsCreate(t *testing.T) {
	h := &prepareHandler{createCode: http.StatusOK}
	h.listOK.Store(true)
	st, _ := newTestStorage(t, h)
	st.project = "test-project"

	if err := st.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if h.creates != 0 {
		t.Fatalf("expected no create call, got %d", h.creates)
	}
}

func TestGCSPrepareConflictNotAccessibleFails(t *testing.T) {
	h := &prepareHandler{createCode: http.StatusConflict}
	st, _ := newTestStorage(t, h)
	st.project = "test-project"

	if err := st.Prepare(context.Background()); err == nil {
		t.Fatal("expected error when the bucket name is owned by another project")
	}
	if h.lists != 2 || h.creates != 1 {
		t.Fatalf("expected 2 access checks and 1 create, got %d and %d", h.lists, h.creates)
	}
}

func TestGCSPrepareConflictAccessibleTolerated(t *testing.T) {
	h := &prepareHandler{createCode: http.StatusConflict}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			h.listOK.Store(true)
		}
		h.ServeHTTP(w, r)
	})
	st, _ := newTestStorage(t, handler)
	st.project = "test-project"

	if err := st.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare after concurrent create: %v", err)
	}
}

func TestGCSPrepareAccessCheckFails(t *testing.T) {
	var creates int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			atomic.AddInt32(&creates, 1)
		}
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":{"code":403,"message":"forbidden"}}`)
	})
	st, _ := newTestStorage(t, handler)
	st.project = "test-project"

	if err := st.Prepare(context.Background()); err == nil {
		t.Fatal("expected error when the access check is forbidden")
	}
	if creates != 0 {
		t.Fatalf("expected no create call, got %d", creates)
	}
}

func TestGCSPrepareRequiresProject(t *testing.T) {
	var requests int32
	st, _ := newTestStorage(t, &bucketHandler{creates: &requests})

	if err := st.Prepare(context.Background()); err == nil {
		t.Fatal("expected error when project is empty")
	}
	if requests != 0 {
		t.Fatalf("expected no request, got %d", requests)
	}
}

func TestGCSPrepareCanceled(t *testing.T) {
	var requests int32
	st, _ := newTestStorage(t, &bucketHandler{creates: &requests})
	st.project = "test-project"

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := st.Prepare(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if requests != 0 {
		t.Fatalf("expected no request, got %d", requests)
	}
}
