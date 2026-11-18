// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package logging

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"log/slog"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// timeLayout is the layout of the time that starts each line: the date
// and time the standard logger writes with log.LstdFlags.
const timeLayout = "2006/01/02 15:04:05"

// level is the level the process logs at: Info, or Debug with --verbose.
var level slog.LevelVar

// SetVerbose makes the process log at Debug, adding what only --verbose
// shows, or at Info again.
func SetVerbose(verbose bool) {
	if verbose {
		level.Set(slog.LevelDebug)
		return
	}
	level.Set(slog.LevelInfo)
}

// current is the logger the functions of this package log with once Setup
// made it, and nil before: they write through the standard logger then, as
// a test that captures it (see log.SetOutput) expects.
var current atomic.Pointer[slog.Logger]

// stdLogger is the logger of the functions of this package before Setup:
// it hands each line to the standard logger, which writes it as its output
// and flags say.
var stdLogger = slog.New(&handler{std: true, min: &level})

// logger returns the logger the functions of this package log with.
func logger() *slog.Logger {
	if l := current.Load(); l != nil {
		return l
	}
	return stdLogger
}

// Setup makes the process log to w, through slog and through the standard
// logger, in the format NewHandler writes. Names and values read from a
// cloud reach w with their control characters escaped (see Sanitize).
// What client libraries write to the standard logger is logged at Info,
// except their debug and trace messages, which some of them fill with
// whole HTTP requests, and which are dropped.
func Setup(w io.Writer) {
	l := slog.New(NewHandler(w, &level))
	current.Store(l)
	slog.SetDefault(l)
}

// Debugf logs a message at Debug, the level of what only --verbose shows.
func Debugf(format string, args ...any) { logf(slog.LevelDebug, format, args...) }

// Infof logs a message at Info, the level of a command's progress.
func Infof(format string, args ...any) { logf(slog.LevelInfo, format, args...) }

// Warnf logs a message at Warn, the level of what was left out or didn't
// work, while the run goes on.
func Warnf(format string, args ...any) { logf(slog.LevelWarn, format, args...) }

// Errorf logs a message at Error, the level of what stopped a command.
func Errorf(format string, args ...any) { logf(slog.LevelError, format, args...) }

// Info logs msg at Info, with attributes as slog.Info takes them.
func Info(msg string, args ...any) { emit(slog.LevelInfo, msg, args) }

// logf logs the formatted message at l.
func logf(l slog.Level, format string, args ...any) {
	logger := logger()
	if !logger.Enabled(context.Background(), l) {
		return
	}
	record(logger, l, fmt.Sprintf(format, args...), nil)
}

// emit logs msg at l, with attrs.
func emit(l slog.Level, msg string, attrs []any) {
	logger := logger()
	if !logger.Enabled(context.Background(), l) {
		return
	}
	record(logger, l, msg, attrs)
}

// record hands a record to logger's handler, with the caller of the
// exported function that called logf or emit as its source.
func record(logger *slog.Logger, l slog.Level, msg string, attrs []any) {
	var pcs [1]uintptr
	runtime.Callers(4, pcs[:])
	r := slog.NewRecord(time.Now(), l, msg, pcs[0])
	r.Add(attrs...)
	_ = logger.Handler().Handle(context.Background(), r)
}

// NewHandler returns a handler that writes each record to w as one line:
// the time as the standard logger writes it, a space, the message and the
// record's attributes, if it has any, as key=value. It writes no level
// and nothing else, so that the text of a message is the text of the line,
// as it was when the standard logger wrote it. Records below min are
// dropped, as are the debug and trace messages of client libraries (those
// that start with [DEBUG] or [TRACE]), which some of them fill with whole
// HTTP requests. Control characters in a line are escaped (see Sanitize).
func NewHandler(w io.Writer, leveler slog.Leveler) slog.Handler {
	return &handler{out: &output{w: Sanitize(w)}, min: leveler}
}

// output is where the handlers made from one another write: the writer,
// which a lock lets one line at a time reach.
type output struct {
	mu sync.Mutex
	w  io.Writer
}

// handler is the handler NewHandler returns.
type handler struct {
	out *output
	min slog.Leveler
	// std says that a line is handed to the standard logger, which puts
	// its own prefix before it, and not written to out.
	std bool
	// attrs is the text of the attributes WithAttrs added, each as
	// " key=value".
	attrs string
	// groups are the names of the groups WithGroup opened, outermost first.
	groups []string
}

// Enabled reports whether a record at l is written.
func (h *handler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.min.Level()
}

// Handle writes the record as a line.
func (h *handler) Handle(_ context.Context, r slog.Record) error {
	if hidden(r.Message) {
		return nil
	}
	var b bytes.Buffer
	if !r.Time.IsZero() && !h.std {
		b.WriteString(r.Time.Format(timeLayout))
		b.WriteByte(' ')
	}
	b.WriteString(r.Message)
	b.WriteString(h.attrs)
	r.Attrs(func(a slog.Attr) bool {
		appendAttr(&b, h.groups, a)
		return true
	})
	if h.std {
		return log.Output(0, b.String())
	}
	if b.Len() == 0 || b.Bytes()[b.Len()-1] != '\n' {
		b.WriteByte('\n')
	}
	h.out.mu.Lock()
	defer h.out.mu.Unlock()
	_, err := h.out.w.Write(b.Bytes())
	return err
}

// WithAttrs returns a handler that writes attrs on each line.
func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	var b bytes.Buffer
	b.WriteString(h.attrs)
	for _, a := range attrs {
		appendAttr(&b, h.groups, a)
	}
	next := *h
	next.attrs = b.String()
	return &next
}

// WithGroup returns a handler that writes the attributes after it with the
// group's name before their keys.
func (h *handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	next := *h
	next.groups = append(append([]string(nil), h.groups...), name)
	return &next
}

// hidden reports whether a message is one a client library logs for
// debugging: it starts with [TRACE] or [DEBUG]. A message that only
// mentions a level elsewhere is kept.
func hidden(message string) bool {
	return strings.HasPrefix(message, "[TRACE]") || strings.HasPrefix(message, "[DEBUG]")
}

// appendAttr writes a as " key=value" to b, its key after the names of
// groups. A group attribute writes its members, and an empty attribute
// nothing. A value that is empty, or holds a space, an equals sign, a quote
// or a control character, is quoted.
func appendAttr(b *bytes.Buffer, groups []string, a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return
	}
	if a.Value.Kind() == slog.KindGroup {
		if a.Key != "" {
			groups = append(append([]string(nil), groups...), a.Key)
		}
		for _, m := range a.Value.Group() {
			appendAttr(b, groups, m)
		}
		return
	}
	b.WriteByte(' ')
	for _, g := range groups {
		b.WriteString(g)
		b.WriteByte('.')
	}
	b.WriteString(a.Key)
	b.WriteByte('=')
	value := a.Value.String()
	if value == "" || strings.ContainsAny(value, " \t\r\n\"=") || strings.ContainsFunc(value, isControl) {
		value = strconv.Quote(value)
	}
	b.WriteString(value)
}
