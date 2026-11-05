package oss

import (
	"context"
	"errors"
	"io"
	"math"
	"strings"
	"testing"
)

func TestByteRangeHTTPRange(t *testing.T) {
	cases := []struct {
		name    string
		r       ByteRange
		want    string
		wantErr bool
	}{
		{"bounded", ByteRange{Offset: 0, Length: 10}, "bytes=0-9", false},
		{"bounded mid", ByteRange{Offset: 5, Length: 4}, "bytes=5-8", false},
		{"to EOF", ByteRange{Offset: 3, Length: -1}, "bytes=3-", false},
		{"to EOF at zero", ByteRange{Offset: 0, Length: -1}, "bytes=0-", false},
		{"single byte at maxint", ByteRange{Offset: math.MaxInt64, Length: 1}, "bytes=9223372036854775807-9223372036854775807", false},
		{"max length from zero", ByteRange{Offset: 0, Length: math.MaxInt64}, "bytes=0-9223372036854775806", false},
		{"overflow end", ByteRange{Offset: math.MaxInt64, Length: 2}, "", true},
		{"overflow end mid", ByteRange{Offset: math.MaxInt64 - 5, Length: 7}, "", true},
		{"negative offset", ByteRange{Offset: -1, Length: 10}, "", true},
		{"negative offset to EOF", ByteRange{Offset: -1, Length: -1}, "", true},
		{"zero length", ByteRange{Offset: 0, Length: 0}, "", true},
		{"length below -1", ByteRange{Offset: 0, Length: -2}, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := c.r.HTTPRange()
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				if !errors.Is(err, ErrInvalidRange) {
					t.Fatalf("expected ErrInvalidRange, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestVerifyContentRange(t *testing.T) {
	bounded := &ByteRange{Offset: 5, Length: 4}    // requested end 8
	openEnded := &ByteRange{Offset: 4, Length: -1} // requested 4-EOF
	clamped := &ByteRange{Offset: 8, Length: 100}  // requested end 107

	cases := []struct {
		name    string
		header  string
		r       *ByteRange
		wantErr bool
	}{
		{"bounded ok", "bytes 5-8/100", bounded, false},
		{"bounded unknown total ok", "bytes 5-8/*", bounded, false},
		{"bounded clamped to EOF ok", "bytes 8-9/10", clamped, false},
		{"bounded short of requested end", "bytes 8-8/10", clamped, true},
		{"open ended ok", "bytes 4-9/10", openEnded, false},
		{"open ended unknown total ok", "bytes 4-9/*", openEnded, false},
		{"open ended stops before EOF", "bytes 4-8/10", openEnded, true},
		{"missing header", "", bounded, true},
		{"garbage", "garbage", bounded, true},
		{"malformed end", "bytes 5-garbage/100", bounded, true},
		{"malformed total", "bytes 5-8/x", bounded, true},
		{"trailing junk on total", "bytes 5-8/100x", bounded, true},
		{"sign on start", "bytes +5-8/100", bounded, true},
		{"sign on end", "bytes 5-+8/100", bounded, true},
		{"negative start rejected by digits", "bytes -1-8/100", bounded, true},
		{"inverted range", "bytes 8-5/100", bounded, true},
		{"missing dash", "bytes 5/100", bounded, true},
		{"missing total part", "bytes 5-8", bounded, true},
		{"wrong start", "bytes 0-8/100", bounded, true},
		{"wrong end", "bytes 5-9/100", bounded, true},
		{"server returned whole object", "bytes 0-99/100", &ByteRange{Offset: 0, Length: 10}, true},
		{"total not greater than end", "bytes 5-8/8", bounded, true},
		{"total equal to end+1 ok", "bytes 5-8/9", bounded, false},
		{"unknown total end beyond request", "bytes 5-99/*", bounded, true},
		{"nil request range ignores header", "", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := VerifyContentRange(c.header, c.r)
			if c.wantErr && err == nil {
				t.Fatalf("expected error for %q", c.header)
			}
			if !c.wantErr && err != nil {
				t.Fatalf("unexpected error for %q: %v", c.header, err)
			}
		})
	}
}

// fakeOSS satisfies OSS only via the embedded interface; calling any method
// on it panics because the embedded value is nil.
type fakeOSS struct {
	OSS
}

type fakeStreamingOSS struct {
	OSS
	opened   bool
	gotCtx   context.Context
	gotKey   string
	gotRange *ByteRange
	reader   io.ReadCloser
	err      error
}

func (f *fakeStreamingOSS) Open(ctx context.Context, key string, byteRange *ByteRange) (io.ReadCloser, error) {
	f.opened = true
	f.gotCtx = ctx
	f.gotKey = key
	f.gotRange = byteRange
	return f.reader, f.err
}

func TestSupportsStreaming(t *testing.T) {
	if !SupportsStreaming(&fakeStreamingOSS{}) {
		t.Fatal("expected true for StreamingOSS implementation")
	}
	if SupportsStreaming(&fakeOSS{}) {
		t.Fatal("expected false for OSS-only implementation")
	}
	if SupportsStreaming(nil) {
		t.Fatal("expected false for nil")
	}
	if SupportsStreaming((*fakeStreamingOSS)(nil)) {
		t.Fatal("expected false for typed nil")
	}
}

func TestOpenStreamUnsupported(t *testing.T) {
	for _, store := range []OSS{nil, &fakeOSS{}, (*fakeStreamingOSS)(nil)} {
		_, err := OpenStream(context.Background(), store, "key", nil)
		if !errors.Is(err, ErrStreamingNotSupported) {
			t.Fatalf("expected ErrStreamingNotSupported, got %v", err)
		}
	}
}

func TestOpenStreamDelegates(t *testing.T) {
	reader := io.NopCloser(strings.NewReader("data"))
	fake := &fakeStreamingOSS{reader: reader}
	r := &ByteRange{Offset: 2, Length: 4}
	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "v")

	rc, err := OpenStream(ctx, fake, "key", r)
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	if rc != reader {
		t.Fatal("expected the same reader back")
	}
	if !fake.opened || fake.gotKey != "key" || fake.gotRange != r || fake.gotCtx.Value(ctxKey{}) != "v" {
		t.Fatalf("Open not called with same ctx/key/range: %+v", fake)
	}

	fakeErr := errors.New("boom")
	fake2 := &fakeStreamingOSS{err: fakeErr}
	if _, err := OpenStream(context.Background(), fake2, "key", nil); !errors.Is(err, fakeErr) {
		t.Fatalf("expected provider error, got %v", err)
	}
}
