package integration

import (
	"fmt"
	"io"
	"os"
	"testing"
)

// objectSize is the shared fixture size: byte at offset i equals i % 251.
const objectSize = 16 * 1024 * 1024

func expectedByte(pos int64) byte { return byte(pos % 251) }

// requireIntegration skips unless the emulator gate is enabled; ordinary
// `go test` never contacts emulators from this package.
func requireIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("CLOUDKIT_INTEGRATION") != "1" {
		t.Skip("set CLOUDKIT_INTEGRATION=1 (via make test-integration) to run emulator integration tests")
	}
}

// readBounded streams rc through a 32KiB buffer, validating every byte
// against expectedByte(startOff+i). Returns total bytes read.
func readBounded(rc io.Reader, startOff int64) (int64, error) {
	buf := make([]byte, 32*1024)
	var total int64
	for {
		n, err := rc.Read(buf)
		for i := 0; i < n; i++ {
			pos := startOff + total + int64(i)
			if buf[i] != expectedByte(pos) {
				return total, fmt.Errorf("content mismatch at offset %d: got %d, want %d", pos, buf[i], expectedByte(pos))
			}
		}
		total += int64(n)
		if err == io.EOF {
			return total, nil
		}
		if err != nil {
			return total, err
		}
	}
}
