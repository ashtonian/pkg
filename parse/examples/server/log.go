package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"
)

// levelTrace is the level below debug: request and response bodies, and
// schema discovery, which clients poll.
const levelTrace = slog.Level(-12)

// parseLevel reads a log_level setting: trace, or a level slog names.
func parseLevel(name string) (slog.Level, error) {
	if name == "trace" {
		return levelTrace, nil
	}
	var level slog.Level
	err := level.UnmarshalText([]byte(name))
	if err != nil {
		return 0, fmt.Errorf("reading log level %q: %w", name, err)
	}
	return level, nil
}

// levelName is how the log writes a level: slog's name, and TRACE for
// levelTrace, which slog would write DEBUG-8.
func levelName(level slog.Level) string {
	if level == levelTrace {
		return "TRACE"
	}
	return level.String()
}

// newLogger writes JSON entries to w at level. Each entry carries the
// request ID its context holds, so a call site inside a request names its
// request without adding the ID itself.
func newLogger(w io.Writer, level slog.Leveler) *slog.Logger {
	opts := &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) > 0 || a.Key != slog.LevelKey {
				return a
			}
			l, ok := a.Value.Any().(slog.Level)
			if ok {
				a.Value = slog.StringValue(levelName(l))
			}
			return a
		},
	}
	h := contextHandler{slog.NewJSONHandler(w, opts)}
	return slog.New(h)
}

type requestIDKey struct{}

// withRequestID returns ctx carrying a request's ID for every entry logged
// under it.
func withRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// contextHandler adds to each entry the request ID of the context it is
// logged under.
type contextHandler struct{ slog.Handler }

func (h contextHandler) Handle(ctx context.Context, r slog.Record) error {
	id, ok := ctx.Value(requestIDKey{}).(string)
	if ok {
		r.AddAttrs(slog.String("request_id", id))
	}
	return h.Handler.Handle(ctx, r)
}

func (h contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := contextHandler{h.Handler.WithAttrs(attrs)}
	return next
}

func (h contextHandler) WithGroup(name string) slog.Handler {
	next := contextHandler{h.Handler.WithGroup(name)}
	return next
}

// logAlways writes an entry at Info straight to the handler, past the level
// gate: the startup entry and a level change, so that at any level the log
// says what runs and at what level.
func logAlways(ctx context.Context, log *slog.Logger, msg string, attrs ...slog.Attr) {
	r := slog.NewRecord(time.Now(), slog.LevelInfo, msg, 0)
	r.AddAttrs(attrs...)
	// An entry that cannot reach stderr has nowhere else to go.
	_ = log.Handler().Handle(ctx, r)
}
