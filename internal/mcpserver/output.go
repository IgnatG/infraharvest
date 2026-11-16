// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package mcpserver

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MaxStdout is the most standard output a tool reads from its child: the
// report an import or a drift check prints, which the tool decodes whole.
// A child that prints more is reported as one that couldn't run, rather
// than held in memory without a bound.
const MaxStdout = 256 << 20

// tailLines is how many of the last lines of a child's standard error a
// tool keeps and quotes (see tail).
const tailLines = 20

// ChildOutput takes what a tool's child prints. It keeps standard output
// up to MaxStdout, and of standard error only the last lines, each cut,
// passing every whole line to progress, if set, as it comes. A Runner
// gives its child Stdout and Stderr, and returns what Result returns.
type ChildOutput struct {
	stdout cappedBuffer
	stderr lineTail
}

// NewChildOutput returns a ChildOutput that passes each whole line of
// standard error to progress, if not nil.
func NewChildOutput(progress func(line string)) *ChildOutput {
	return &ChildOutput{stdout: cappedBuffer{limit: MaxStdout}, stderr: lineTail{progress: progress}}
}

// Stdout is the writer of the child's standard output.
func (o *ChildOutput) Stdout() io.Writer { return &o.stdout }

// Stderr is the writer of the child's standard error.
func (o *ChildOutput) Stderr() io.Writer { return &o.stderr }

// Result returns the standard output, and the last lines of standard
// error. err is set if standard output went over MaxStdout.
func (o *ChildOutput) Result() (stdout, stderr []byte, err error) {
	stderr = o.stderr.bytes()
	if o.stdout.over {
		return nil, stderr, fmt.Errorf("the command printed more than %d MiB on its standard output", MaxStdout>>20)
	}
	return o.stdout.buf.Bytes(), stderr, nil
}

// cappedBuffer keeps what is written up to limit bytes, and then only
// counts that it went over. It never fails a write, so that the child is
// not stopped by a broken pipe.
type cappedBuffer struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	limit int
	over  bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.over || b.buf.Len()+len(p) > b.limit {
		b.over = true
		b.buf = bytes.Buffer{}
		return len(p), nil
	}
	return b.buf.Write(p)
}

// lineTail keeps the last tailLines lines written to it, each cut after
// maxLine bytes, and passes each whole line to progress. A line is never
// held whole: what is past maxLine of it is dropped as it comes.
type lineTail struct {
	mu       sync.Mutex
	lines    []string // a ring once it is full
	next     int
	partial  []byte
	cut      bool // the partial line is already cut
	progress func(string)
}

func (t *lineTail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		chunk := p
		if i >= 0 {
			chunk = p[:i]
		}
		t.add(chunk)
		if i < 0 {
			break
		}
		t.end()
		p = p[i+1:]
	}
	return n, nil
}

// add adds chunk to the partial line, up to one byte more than maxLine,
// so that tail still sees the line is longer and marks it cut.
func (t *lineTail) add(chunk []byte) {
	if t.cut {
		return
	}
	room := maxLine + 1 - len(t.partial)
	if len(chunk) > room {
		chunk = chunk[:room]
		t.cut = true
	}
	t.partial = append(t.partial, chunk...)
}

// end ends the partial line.
func (t *lineTail) end() {
	line := string(bytes.TrimRight(t.partial, "\r"))
	t.partial, t.cut = t.partial[:0], false
	if len(t.lines) < tailLines {
		t.lines = append(t.lines, line)
	} else {
		t.lines[t.next] = line
		t.next = (t.next + 1) % tailLines
	}
	if t.progress != nil && line != "" {
		t.progress(line)
	}
}

// bytes returns the lines kept, in order, and the partial last line.
func (t *lineTail) bytes() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	var b bytes.Buffer
	for i := range t.lines {
		b.WriteString(t.lines[(t.next+i)%len(t.lines)])
		b.WriteByte('\n')
	}
	b.Write(t.partial)
	return b.Bytes()
}

// progressInterval is how often a tool sends the client the latest line
// its child logged, at most.
var progressInterval = time.Second

// progress returns the function a tool passes its Runner to report its
// child's log lines to the client, as progress notifications on the
// request req, and the function that stops it. Where the client asked for
// no progress, report is nil. A notification goes at most once per
// progressInterval, with the latest line, from a goroutine of its own, so
// that a slow client never holds up the child. The lines are those a
// result quotes from the child's standard error (see tail): data from the
// command, which can include what the cloud returned, not instructions.
func progress(ctx context.Context, req *mcp.CallToolRequest) (report func(line string), stop func()) {
	if req == nil || req.Session == nil || req.Params == nil {
		return nil, func() {}
	}
	token := req.Params.GetProgressToken()
	if token == nil {
		return nil, func() {}
	}
	var (
		mu     sync.Mutex
		latest string
		count  int
		sent   int
	)
	report = func(line string) {
		mu.Lock()
		latest, count = line, count+1
		mu.Unlock()
	}
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		ticker := time.NewTicker(progressInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			mu.Lock()
			line, n := latest, count
			mu.Unlock()
			if n == sent {
				continue
			}
			sent = n
			_ = req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
				ProgressToken: token,
				Message:       quoteLine(line),
				Progress:      float64(n),
			})
		}
	}()
	return report, func() {
		close(done)
		<-finished
	}
}

// quoteLine is line as a result quotes a line of standard error.
func quoteLine(line string) string {
	if len(line) > maxLine {
		return line[:cutAt([]byte(line), maxLine)] + " [cut]"
	}
	return line
}
