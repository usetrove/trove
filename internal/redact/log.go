package redact

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"
)

// Value returns v made safe to log under the given field name.
// Scalars that cannot hold a secret pass through unchanged; everything else is
// formatted and scrubbed.
func Value(name string, v any) any {
	if IsSensitiveName(name) {
		return Placeholder
	}
	switch x := v.(type) {
	case nil, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64,
		float32, float64, time.Duration, time.Time:
		return x
	case string:
		return String(x)
	case error:
		return String(x.Error())
	default:
		return String(fmt.Sprintf("%+v", v))
	}
}

// Error wraps err so that its message is scrubbed. errors.Is/As still work.
// A nil error stays nil.
func Error(err error) error {
	if err == nil {
		return nil
	}
	return &redactedError{msg: String(err.Error()), err: err}
}

type redactedError struct {
	msg string
	err error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.err }

// Errorf is fmt.Errorf with the resulting message scrubbed.
func Errorf(format string, args ...any) error {
	return Error(fmt.Errorf(format, args...))
}

// handler wraps a slog.Handler and scrubs the message and all attributes.
type handler struct{ inner slog.Handler }

// NewHandler returns a slog.Handler that redacts before delegating to inner.
func NewHandler(inner slog.Handler) slog.Handler { return handler{inner} }

// NewLogger returns a text logger writing to w with redaction applied.
// verbose enables debug output.
func NewLogger(w io.Writer, verbose bool) *slog.Logger {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	return slog.New(NewHandler(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level})))
}

func (h handler) Enabled(ctx context.Context, l slog.Level) bool { return h.inner.Enabled(ctx, l) }

func (h handler) Handle(ctx context.Context, r slog.Record) error {
	nr := slog.NewRecord(r.Time, r.Level, String(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		nr.AddAttrs(attr(a))
		return true
	})
	return h.inner.Handle(ctx, nr)
}

func (h handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		out[i] = attr(a)
	}
	return handler{h.inner.WithAttrs(out)}
}

func (h handler) WithGroup(name string) slog.Handler { return handler{h.inner.WithGroup(name)} }

func attr(a slog.Attr) slog.Attr {
	a.Value = a.Value.Resolve()
	if a.Value.Kind() == slog.KindGroup {
		g := a.Value.Group()
		out := make([]slog.Attr, len(g))
		for i, ga := range g {
			out[i] = attr(ga)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(out...)}
	}
	if IsSensitiveName(a.Key) {
		return slog.String(a.Key, Placeholder)
	}
	switch a.Value.Kind() {
	case slog.KindString:
		return slog.String(a.Key, String(a.Value.String()))
	case slog.KindAny:
		return slog.Any(a.Key, Value(a.Key, a.Value.Any()))
	}
	return a
}

// Writer returns an io.Writer that scrubs each Write before passing it on.
// Callers should write whole lines per call; a secret split across two writes
// cannot be recognised.
func Writer(w io.Writer) io.Writer { return writer{w} }

type writer struct{ w io.Writer }

func (rw writer) Write(p []byte) (int, error) {
	out := String(string(p))
	if _, err := rw.w.Write([]byte(out)); err != nil {
		return 0, err
	}
	return len(p), nil
}

// FormatPanic renders a recovered panic value and stack as scrubbed text.
func FormatPanic(r any, stack []byte) string {
	var b bytes.Buffer
	fmt.Fprintf(&b, "trove crashed: %v\n", r)
	b.Write(stack)
	return String(b.String())
}
