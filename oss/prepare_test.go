package oss

import (
	"context"
	"errors"
	"testing"
)

// fakePreparer satisfies OSS via the embedded interface (any method call
// other than Prepare would panic on the nil embedded value).
type fakePreparer struct {
	OSS
	prepared bool
	gotCtx   context.Context
	err      error
}

func (f *fakePreparer) Prepare(ctx context.Context) error {
	f.prepared = true
	f.gotCtx = ctx
	return f.err
}

func TestSupportsPrepare(t *testing.T) {
	if !SupportsPrepare(&fakePreparer{}) {
		t.Fatal("expected true for Preparer implementation")
	}
	if SupportsPrepare(&fakeOSS{}) {
		t.Fatal("expected false for OSS-only implementation")
	}
	if SupportsPrepare(nil) {
		t.Fatal("expected false for nil")
	}
	if SupportsPrepare((*fakePreparer)(nil)) {
		t.Fatal("expected false for typed nil")
	}
}

func TestPrepareUnsupported(t *testing.T) {
	for _, store := range []OSS{nil, &fakeOSS{}, (*fakePreparer)(nil)} {
		err := Prepare(context.Background(), store)
		if !errors.Is(err, ErrPrepareNotSupported) {
			t.Fatalf("expected ErrPrepareNotSupported, got %v", err)
		}
	}
}

func TestPrepareDelegates(t *testing.T) {
	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "v")

	fake := &fakePreparer{}
	if err := Prepare(ctx, fake); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if !fake.prepared || fake.gotCtx.Value(ctxKey{}) != "v" {
		t.Fatalf("Prepare not called with same ctx: %+v", fake)
	}

	fakeErr := errors.New("boom")
	fake2 := &fakePreparer{err: fakeErr}
	if err := Prepare(ctx, fake2); !errors.Is(err, fakeErr) {
		t.Fatalf("expected provider error, got %v", err)
	}
}
