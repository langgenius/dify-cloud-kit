package s3

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
)

// bucketHandler simulates S3 bucket-level operations: HEAD /bucket returns
// 404 until the bucket is created, then 200; PUT /bucket creates it.
type bucketHandler struct {
	heads    *int32
	creates  *int32
	headCode int // if >0, always respond HEAD with this status
}

func (h *bucketHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodHead:
		if h.heads != nil {
			atomic.AddInt32(h.heads, 1)
		}
		if h.headCode > 0 {
			w.WriteHeader(h.headCode)
			return
		}
		w.WriteHeader(http.StatusOK)
	case http.MethodPut:
		if h.creates != nil {
			atomic.AddInt32(h.creates, 1)
		}
		w.WriteHeader(http.StatusOK)
	default:
		w.WriteHeader(http.StatusBadRequest)
	}
}

func TestS3PrepareCreatesBucket(t *testing.T) {
	var heads, creates int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			if atomic.AddInt32(&heads, 1) == 1 {
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>NotFound</Code><Message>not found</Message></Error>`)
				return
			}
			w.WriteHeader(http.StatusOK)
		case http.MethodPut:
			atomic.AddInt32(&creates, 1)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	st, _ := newTestStorage(t, handler)

	if err := st.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if err := st.Prepare(context.Background()); err != nil {
		t.Fatalf("second Prepare: %v", err)
	}
	if heads != 2 || creates != 1 {
		t.Fatalf("expected 2 HEADs and 1 create, got %d HEADs and %d creates", heads, creates)
	}
}

func TestS3PrepareCreateRaceTolerated(t *testing.T) {
	var heads, creates int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			atomic.AddInt32(&heads, 1)
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>NotFound</Code><Message>not found</Message></Error>`)
		case http.MethodPut:
			atomic.AddInt32(&creates, 1)
			w.WriteHeader(http.StatusConflict)
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>BucketAlreadyOwnedByYou</Code><Message>exists</Message></Error>`)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	st, _ := newTestStorage(t, handler)

	if err := st.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare with BucketAlreadyOwnedByYou: %v", err)
	}
}

func TestS3PrepareBucketOwnedByOtherFails(t *testing.T) {
	var heads, creates int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			atomic.AddInt32(&heads, 1)
			w.WriteHeader(http.StatusNotFound)
		case http.MethodPut:
			atomic.AddInt32(&creates, 1)
			w.WriteHeader(http.StatusConflict)
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>BucketAlreadyExists</Code><Message>taken</Message></Error>`)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	st, _ := newTestStorage(t, handler)

	if err := st.Prepare(context.Background()); err == nil {
		t.Fatal("expected error when the bucket name is owned by another account")
	}
	if heads != 2 || creates != 1 {
		t.Fatalf("expected 2 HEADs and 1 create, got %d HEADs and %d creates", heads, creates)
	}
}

func TestS3PrepareBucketAlreadyExistsAccessibleTolerated(t *testing.T) {
	var heads int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			if atomic.AddInt32(&heads, 1) == 1 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
		case http.MethodPut:
			w.WriteHeader(http.StatusConflict)
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>BucketAlreadyExists</Code><Message>exists</Message></Error>`)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	st, _ := newTestStorage(t, handler)

	if err := st.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare with accessible BucketAlreadyExists: %v", err)
	}
}

func TestS3PrepareHeadFails(t *testing.T) {
	st, _ := newTestStorage(t, &bucketHandler{headCode: http.StatusForbidden})

	if err := st.Prepare(context.Background()); err == nil {
		t.Fatal("expected error when HEAD is forbidden")
	}
}

func TestS3PrepareCanceled(t *testing.T) {
	var requests int32
	st, _ := newTestStorage(t, &bucketHandler{heads: &requests})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := st.Prepare(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if requests != 0 {
		t.Fatalf("expected no request, got %d", requests)
	}
}
