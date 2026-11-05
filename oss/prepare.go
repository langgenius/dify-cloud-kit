package oss

import (
	"context"
	"errors"
	"fmt"
)

// ErrPrepareNotSupported is returned by Prepare when the given OSS
// implementation does not implement Preparer.
var ErrPrepareNotSupported = errors.New("storage preparation is not supported")

// Preparer is an optional capability interface for providers that can
// perform idempotent, provider-specific resource preparation (for example,
// creating the configured container or bucket). It is an explicit,
// potentially resource-creating operation — it is never invoked implicitly
// by constructors or other methods.
type Preparer interface {
	// Prepare performs idempotent resource preparation. Calling it more
	// than once must be safe. It returns an error when preparation fails.
	Prepare(ctx context.Context) error
}

// SupportsPrepare reports whether store implements Preparer. A nil store
// reports false.
func SupportsPrepare(store OSS) bool {
	if isNilOSS(store) {
		return false
	}
	_, ok := store.(Preparer)
	return ok
}

// Prepare delegates to store's Preparer implementation when present. A store
// without preparation support fails with ErrPrepareNotSupported; there is no
// silent no-op fallback.
func Prepare(ctx context.Context, store OSS) error {
	if isNilOSS(store) {
		return fmt.Errorf("%w: <nil>", ErrPrepareNotSupported)
	}
	preparer, ok := store.(Preparer)
	if !ok {
		return fmt.Errorf("%w: %T", ErrPrepareNotSupported, store)
	}
	return preparer.Prepare(ctx)
}
