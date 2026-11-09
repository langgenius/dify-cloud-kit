package oss

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"strconv"
	"strings"
)

var (
	// ErrInvalidRange is returned when a ByteRange is malformed
	// (negative offset, zero or less-than-minus-one length, or overflowing end).
	ErrInvalidRange = errors.New("invalid byte range")
	// ErrRangeNotSatisfiable is returned when a range cannot be satisfied,
	// either because it is outside the local object size or because the
	// remote service answered 416 Requested Range Not Satisfiable.
	ErrRangeNotSatisfiable = errors.New("byte range not satisfiable")
	// ErrStreamingNotSupported is returned by OpenStream when the given OSS
	// implementation does not implement StreamingOSS.
	ErrStreamingNotSupported = errors.New("streaming reads are not supported")
)

// ByteRange describes a byte range of an object starting at Offset. A nil
// *ByteRange means the whole object.
//
// Offset must be >= 0. Length > 0 requests up to Length bytes (the result may
// be shorter when clamped to the end of the object); Length == -1 reads from
// Offset to the end of the object. Length == 0 or < -1 is invalid.
type ByteRange struct {
	Offset int64
	Length int64
}

// HTTPRange validates the range and returns the HTTP Range header value,
// "bytes=N-M" for bounded ranges or "bytes=N-" for offset-to-EOF ranges.
func (r ByteRange) HTTPRange() (string, error) {
	if r.Offset < 0 {
		return "", fmt.Errorf("%w: offset %d is negative", ErrInvalidRange, r.Offset)
	}
	if r.Length == 0 || r.Length < -1 {
		return "", fmt.Errorf("%w: length %d", ErrInvalidRange, r.Length)
	}
	if r.Length == -1 {
		return fmt.Sprintf("bytes=%d-", r.Offset), nil
	}
	if r.Length-1 > math.MaxInt64-r.Offset {
		return "", fmt.Errorf("%w: end of range overflows", ErrInvalidRange)
	}
	return fmt.Sprintf("bytes=%d-%d", r.Offset, r.Offset+(r.Length-1)), nil
}

// StreamingOSS is an optional extension of OSS for providers that can stream
// an object (or a byte range of it) without buffering the whole object in
// memory. Callers must type-assert OSS implementations to StreamingOSS; there
// is intentionally no fallback that silently buffers via Load.
//
// The caller must Close the returned reader. The read is governed by ctx:
// cancelling ctx aborts the request and subsequent reads.
type StreamingOSS interface {
	OSS
	// Range returns a reader over the object stored at key. byteRange may be
	// nil to request the whole object. An invalid range fails with
	// ErrInvalidRange before any I/O; a range outside the object fails with
	// ErrRangeNotSatisfiable.
	Range(ctx context.Context, key string, byteRange *ByteRange) (io.ReadCloser, error)
}

// isNilOSS reports whether store is nil, including a nil pointer inside a
// non-nil interface, so callers never invoke methods on it.
func isNilOSS(store OSS) bool {
	if store == nil {
		return true
	}
	v := reflect.ValueOf(store)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return v.IsNil()
	}
	return false
}

// SupportsStreaming reports whether store implements StreamingOSS, i.e.
// supports both full-object and ranged streaming reads via Range. A nil store
// reports false.
func SupportsStreaming(store OSS) bool {
	if isNilOSS(store) {
		return false
	}
	_, ok := store.(StreamingOSS)
	return ok
}

// OpenStream opens a streaming read on store when it implements StreamingOSS.
// byteRange may be nil for the whole object. A store without streaming
// support fails with ErrStreamingNotSupported; there is no fallback to Load,
// which would silently buffer the whole object in memory.
func OpenStream(ctx context.Context, store OSS, key string, byteRange *ByteRange) (io.ReadCloser, error) {
	if isNilOSS(store) {
		return nil, fmt.Errorf("%w: <nil>", ErrStreamingNotSupported)
	}
	streamer, ok := store.(StreamingOSS)
	if !ok {
		return nil, fmt.Errorf("%w: %T", ErrStreamingNotSupported, store)
	}
	return streamer.Range(ctx, key, byteRange)
}

// contentRange is a strictly parsed HTTP Content-Range value
// "bytes <start>-<end>/<total>".
type contentRange struct {
	start int64
	end   int64 // inclusive
	total int64 // -1 when the server reported "*"
}

func parseDigits(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// parseContentRange parses a Content-Range header strictly: digits only, no
// signs or spaces inside fields, start >= 0, end >= start, and a numeric
// total must be > end.
func parseContentRange(h string) (contentRange, error) {
	var cr contentRange
	rest, ok := strings.CutPrefix(h, "bytes ")
	if !ok {
		return cr, fmt.Errorf("malformed Content-Range %q", h)
	}
	span, total, ok := strings.Cut(rest, "/")
	if !ok {
		return cr, fmt.Errorf("malformed Content-Range %q", h)
	}
	startStr, endStr, ok := strings.Cut(span, "-")
	if !ok {
		return cr, fmt.Errorf("malformed Content-Range %q", h)
	}
	start, ok := parseDigits(startStr)
	if !ok {
		return cr, fmt.Errorf("malformed Content-Range %q", h)
	}
	end, ok := parseDigits(endStr)
	if !ok {
		return cr, fmt.Errorf("malformed Content-Range %q", h)
	}
	if end < start {
		return cr, fmt.Errorf("malformed Content-Range %q: end before start", h)
	}
	cr.start, cr.end, cr.total = start, end, -1
	if total == "*" {
		return cr, nil
	}
	t, ok := parseDigits(total)
	if !ok {
		return cr, fmt.Errorf("malformed Content-Range %q", h)
	}
	if t <= end {
		return cr, fmt.Errorf("malformed Content-Range %q: total not greater than end", h)
	}
	cr.total = t
	return cr, nil
}

// VerifyContentRange checks that a ranged GET was honored by the server: the
// Content-Range header must be present, well-formed, start at the requested
// offset, and its end must be consistent with the requested length and the
// reported total size.
func VerifyContentRange(contentRange string, r *ByteRange) error {
	if r == nil {
		return nil
	}
	if _, err := r.HTTPRange(); err != nil {
		return err
	}
	cr, err := parseContentRange(contentRange)
	if err != nil {
		return fmt.Errorf("server did not honor requested range %v: %w", *r, err)
	}
	if cr.start != r.Offset {
		return fmt.Errorf("server did not honor requested range %v: Content-Range starts at %d", *r, cr.start)
	}
	if r.Length == -1 {
		// Open-ended request: with a known total the response must run to
		// the last byte; with "*" any end is accepted (it may be EOF).
		if cr.total >= 0 && cr.end != cr.total-1 {
			return fmt.Errorf("server did not honor requested range %v: Content-Range ends at %d of %d", *r, cr.end, cr.total)
		}
		return nil
	}
	requestedEnd := r.Offset + (r.Length - 1)
	if cr.total >= 0 {
		// The server must return exactly the requested end, clamped to the
		// last available byte when the range extends past EOF.
		expected := requestedEnd
		if expected > cr.total-1 {
			expected = cr.total - 1
		}
		if cr.end != expected {
			return fmt.Errorf("server did not honor requested range %v: Content-Range ends at %d, expected %d", *r, cr.end, expected)
		}
		return nil
	}
	if cr.end > requestedEnd {
		return fmt.Errorf("server did not honor requested range %v: Content-Range ends at %d, requested %d", *r, cr.end, requestedEnd)
	}
	return nil
}
