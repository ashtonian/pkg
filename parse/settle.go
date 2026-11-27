package parse

import (
	"errors"
	"time"
)

// A provider settles an update's transaction — its COMMIT or ROLLBACK, or
// in a transaction the resource is scoped to (In) the release of its
// savepoint or the rollback to it — once every statement has run and every
// row has scanned. The request may be gone by then, and a settle the
// request's cancellation cut short would leave the update half reported, so
// each settling statement runs under the request's values without its
// cancellation, bounded by a timeout of its own: a stalled database holds
// the call for that long and no longer. Each provider package takes the
// timeout as an option (parsesql.SettleTimeout, parsepgx.SettleTimeout).

// DefaultSettleTimeout bounds each statement that settles an update's
// transaction when a provider is opened without a settle timeout.
const DefaultSettleTimeout = 10 * time.Second

// ErrCommitUnknown is an update whose COMMIT was sent and never answered:
// the connection failed, or the settle timeout ran out, while the database
// was committing. The update may have been applied. Every other error from
// Update or UpdateBatch means nothing the update wrote is kept; after this
// one, read the rows back before retrying a write that is not idempotent.
// errors.Is matches it; the commit's own error is under it.
var ErrCommitUnknown = errors.New("commit outcome unknown")
