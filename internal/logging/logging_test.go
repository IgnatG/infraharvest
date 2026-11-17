// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package logging

import (
	"bytes"
	"context"
	"log"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// stamp is the length of the time a line starts with, and the space after.
const stamp = len("2006/01/02 15:04:05 ")

// newTestLogger returns a logger writing to out through the handler.
func newTestLogger(out *bytes.Buffer, leveler slog.Leveler) *slog.Logger {
	return slog.New(NewHandler(out, leveler))
}

// lines returns the lines out holds, without their times.
func lines(out *bytes.Buffer) []string {
	var got []string
	for _, line := range strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n") {
		got = append(got, line[stamp:])
	}
	return got
}

// A message is written as the standard logger wrote it, whatever its
// level: the text and a line feed, with the time first.
func TestHandlerWritesTheStandardLoggersText(t *testing.T) {
	messages := []string{
		"generating configuration for 3 resources",
		"",
		"ends with a line feed\n",
		"two lines\nof text",
		"100% of it",
		"aws: ec2_instance: instance types: AccessDenied; the configuration keeps the instances' CPU options",
		"panicked: boom\ngoroutine 1 [running]:\n\tmain.main()",
		"bucket \x1b[2Jevil",
	}
	for _, level := range []slog.Level{slog.LevelInfo, slog.LevelWarn, slog.LevelError} {
		for _, message := range messages {
			var want, got bytes.Buffer
			log.New(Sanitize(&want), "", 0).Print(message)
			// A record without a time is written without one, like the
			// standard logger without flags.
			r := slog.NewRecord(time.Time{}, level, message, 0)
			if err := newTestLogger(&got, slog.LevelInfo).Handler().Handle(context.Background(), r); err != nil {
				t.Fatal(err)
			}
			if got.String() != want.String() {
				t.Errorf("level %v, message %q: got %q, want %q", level, message, got.String(), want.String())
			}
		}
	}
}

// Each line starts with the time in the standard logger's layout.
func TestHandlerStartsLinesWithTheTime(t *testing.T) {
	var out bytes.Buffer
	at := time.Date(2026, 11, 17, 9, 5, 3, 123456789, time.Local)
	r := slog.NewRecord(at, slog.LevelWarn, "warning: --force-unlock: writing to out", 0)
	if err := newTestLogger(&out, slog.LevelInfo).Handler().Handle(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if want := "2026/11/17 09:05:03 warning: --force-unlock: writing to out\n"; out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}
}

// The default level shows Info and above; Debug is what --verbose adds.
func TestHandlerLevels(t *testing.T) {
	for _, tc := range []struct {
		min  slog.Level
		want string
	}{
		{slog.LevelInfo, "info,warn,error"},
		{slog.LevelDebug, "debug,info,warn,error"},
		{slog.LevelError, "error"},
	} {
		var out bytes.Buffer
		logger := newTestLogger(&out, tc.min)
		logger.Debug("debug")
		logger.Info("info")
		logger.Warn("warn")
		logger.Error("error")
		if got := strings.Join(lines(&out), ","); got != tc.want {
			t.Errorf("min %v: got %q, want %q", tc.min, got, tc.want)
		}
	}
}

// Attributes follow the message as key=value, and a value that needs it
// is quoted.
func TestHandlerWritesAttributes(t *testing.T) {
	var out bytes.Buffer
	logger := newTestLogger(&out, slog.LevelDebug).With("provider", "aws").WithGroup("req").With("n", 3)
	logger.Debug("listed", "service", "ec2 instance", "empty", "", slog.Group("g", "k", "v"))
	want := `listed provider=aws req.n=3 req.service="ec2 instance" req.empty="" req.g.k=v`
	if got := lines(&out); len(got) != 1 || got[0] != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Debug and trace messages from client libraries are hidden; messages that
// only mention a level are not.
func TestHandlerHidesOnlyDebugAndTraceMessages(t *testing.T) {
	var out bytes.Buffer
	logger := newTestLogger(&out, slog.LevelDebug)

	logger.Info("[DEBUG] GET /v1/things HTTP/1.1")
	logger.Info("[TRACE] request body")
	logger.Info(`aws: skipping bucket "[DEBUG] logs": not selected`)
	logger.Info("generating configuration for 3 resources")

	got := out.String()
	for _, hidden := range []string{"GET /v1/things", "request body"} {
		if strings.Contains(got, hidden) {
			t.Errorf("%q not hidden in %q", hidden, got)
		}
	}
	for _, kept := range []string{`bucket "[DEBUG] logs"`, "generating configuration"} {
		if !strings.Contains(got, kept) {
			t.Errorf("%q hidden from %q", kept, got)
		}
	}
}

// A log entry holding a terminal escape sequence, such as a name read from
// a cloud, reaches the writer with it shown, not sent: in the message and
// in an attribute.
func TestHandlerEscapesControlCharacters(t *testing.T) {
	var out bytes.Buffer
	newTestLogger(&out, slog.LevelInfo).Info("bucket \x1b[2Jevil", "name", "a\x1b[2Jb")
	want := `bucket \x1b[2Jevil name="a\x1b[2Jb"`
	if got := lines(&out); len(got) != 1 || got[0] != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// What client libraries write to the standard logger reaches the same
// writer once Setup is called, in the same text, and its debug and trace
// messages are dropped.
func TestSetupCarriesTheStandardLogger(t *testing.T) {
	previous, flags, writer := slog.Default(), log.Flags(), log.Writer()
	t.Cleanup(func() {
		slog.SetDefault(previous)
		current.Store(nil)
		log.SetFlags(flags)
		log.SetOutput(writer)
		SetVerbose(false)
	})
	var out bytes.Buffer
	Setup(&out)
	log.Print("[DEBUG] GET /v1/things HTTP/1.1")
	log.Printf("%s: listed %d", "aws", 3)
	Infof("%s: listed %d", "azurerm", 4)
	Debugf("not shown")
	if got, want := strings.Join(lines(&out), ","), "aws: listed 3,azurerm: listed 4"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	SetVerbose(true)
	out.Reset()
	Debugf("shown with %s", "--verbose")
	if got := lines(&out); len(got) != 1 || got[0] != "shown with --verbose" {
		t.Errorf("got %q", got)
	}
}

// Before Setup, what is logged goes through the standard logger, as its
// output and flags say, so that a test capturing it (log.SetOutput) sees
// the messages as they are.
func TestLoggingBeforeSetupGoesThroughTheStandardLogger(t *testing.T) {
	flags, writer := log.Flags(), log.Writer()
	t.Cleanup(func() {
		log.SetFlags(flags)
		log.SetOutput(writer)
	})
	var out bytes.Buffer
	log.SetOutput(&out)
	log.SetFlags(0)
	Infof("%s: listed %d", "aws", 3)
	Warnf("warning: %s", "left out")
	Debugf("not shown")
	Info("listed", "n", 3)
	if want := "aws: listed 3\nwarning: left out\nlisted n=3\n"; out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}
}
