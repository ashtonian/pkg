package httpx

import (
	"context"
	"io"
	"net/http"
	"reflect"
)

// CleanupResult observes body cleanup independently of an HTTP or application
// outcome. DrainErr and CloseErr retain their original error identities; they
// are not automatically logged or combined into a delivery error.
type CleanupResult struct {
	BytesDrained int64
	// EOF is true only for a known empty body or a read returning io.EOF.
	// Reaching the byte budget without io.EOF leaves completion unknown.
	EOF      bool
	DrainErr error
	CloseErr error
}

// DrainAndClose reads at most maxDrainBytes from the body's current position,
// then closes the body exactly once. The caller transfers sole cleanup ownership
// for this call: do not retain another deferred Close or call it concurrently.
// It does not close a client/transport or change an accepted protocol outcome.
//
// Zero is close-only. A negative budget or nil context records ErrInvalidConfig
// in DrainErr without reading. Nil and typed-nil bodies are absent (EOF=true),
// and http.NoBody is known empty. Other bodies require an io.EOF read to prove
// completion; no extra byte is read to probe a spent budget. The budget counts
// bytes exposed by the supplied body, including any replayed/buffered prefix.
//
// Cancellation is checked before and between reads and still attempts Close.
// An arbitrary Read or Close cannot be interrupted by this context; retain the
// original request/client deadlines and a cooperative body. No goroutine is
// started, and no caller context is canceled. A body returning (0, nil)
// repeatedly stops with io.ErrNoProgress. Other read and close errors are
// reported separately, including bytes returned with a read error.
func DrainAndClose(ctx context.Context, body io.ReadCloser, maxDrainBytes int64) (result CleanupResult) {
	if maxDrainBytes < 0 {
		result.DrainErr = invalidConfig("DrainAndClose: maxDrainBytes must be >= 0")
	} else if nilCleanupValue(ctx) {
		result.DrainErr = invalidConfig("DrainAndClose: context must not be nil")
	}
	if nilCleanupValue(body) {
		result.EOF = true
		return result
	}
	defer func() { result.CloseErr = body.Close() }()
	if body == http.NoBody {
		result.EOF = true
		return result
	}
	if result.DrainErr != nil || maxDrainBytes == 0 {
		return result
	}
	buffer := make([]byte, min(maxDrainBytes, 32<<10))
	emptyReads := 0
	for result.BytesDrained < maxDrainBytes {
		if err := ctx.Err(); err != nil {
			result.DrainErr = err
			return result
		}
		remaining := maxDrainBytes - result.BytesDrained
		n, err := body.Read(buffer[:min(int64(len(buffer)), remaining)])
		result.BytesDrained += int64(n)
		if err == io.EOF {
			result.EOF = true
			return result
		}
		if err != nil {
			result.DrainErr = err
			return result
		}
		if n != 0 {
			emptyReads = 0
			continue
		}
		emptyReads++
		if emptyReads >= 100 {
			result.DrainErr = io.ErrNoProgress
			return result
		}
	}
	return result
}

func nilCleanupValue(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
