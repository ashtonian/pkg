package parse

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// An executor settles an update's transaction — its COMMIT or ROLLBACK, or
// in a caller's transaction the release of its savepoint or the rollback to
// it — once every statement has run and every row has scanned. The request
// may be gone by then, and a settle the request's cancellation cut short
// would leave the update half reported, so each settling statement runs
// under the request's values without its cancellation, bounded by a timeout
// of its own: a stalled database holds the call for that long and no longer.

// DefaultSettleTimeout bounds each statement that settles an update's
// transaction when no WithSettleTimeout is given.
const DefaultSettleTimeout = 10 * time.Second

// ErrCommitUnknown is an update whose COMMIT was sent and never answered:
// the connection failed, or the settle timeout ran out, while the database
// was committing. The update may have been applied. Every other error from
// an executor's Update or UpdateBatch means nothing the update wrote is
// kept; after this one, read the rows back before retrying a write that is
// not idempotent. errors.Is matches it; the commit's own error is under it.
var ErrCommitUnknown = errors.New("commit outcome unknown")

// ExecOption sets how an executor's Update and UpdateBatch hold their
// transaction (WithSettleTimeout).
type ExecOption func(*ExecConfig)

// WithSettleTimeout bounds each statement that settles an update's
// transaction: COMMIT, ROLLBACK, RELEASE SAVEPOINT and ROLLBACK TO
// SAVEPOINT. It must be positive; the default is DefaultSettleTimeout.
func WithSettleTimeout(d time.Duration) ExecOption {
	return func(c *ExecConfig) { c.SettleTimeout = d }
}

// ExecConfig is how an executor holds an update's transaction, as
// NewExecConfig reads it from the call's options.
type ExecConfig struct {
	// SettleTimeout bounds each statement that settles the transaction.
	SettleTimeout time.Duration
}

// NewExecConfig applies opts over the defaults and refuses a setting no
// executor can run with, before any statement is sent.
func NewExecConfig(opts ...ExecOption) (ExecConfig, error) {
	cfg := ExecConfig{SettleTimeout: DefaultSettleTimeout}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	if cfg.SettleTimeout <= 0 {
		return ExecConfig{}, fmt.Errorf("parse: settle timeout %s: must be positive", cfg.SettleTimeout)
	}
	return cfg, nil
}

// SettleContext is the context one settling statement runs under: ctx's
// values without its cancellation, ended after the settle timeout.
func (c ExecConfig) SettleContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), c.SettleTimeout)
}
