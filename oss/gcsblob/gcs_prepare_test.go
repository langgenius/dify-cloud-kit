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

func TestGCSPrepareCreatesBucket(t *testing.T) {
	var creates int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/storage/v1/b") {
			if atomic.AddInt32(&creates, 1) == 1 {
				w.WriteHeader(http.StatusOK)
				fmt.Fprint(w, `{"kind":"storage#bucket","name":"bkt"}`)
				return
			}
			w.WriteHeader(http.StatusConflict)
			fmt.Fprint(w, `{"error":{"code":409,"message":"conflict"}}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	st, _ := newTestStorage(t, handler)
	st.project = "test-project"

	if err := st.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if err := st.Prepare(context.Background()); err != nil {
		t.Fatalf("second Prepare (409): %v", err)
	}
	if creates != 2 {
		t.Fatalf("expected 2 create calls, got %d", creates)
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
