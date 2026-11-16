// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package mcpserver

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A child's standard error is kept as its last lines only, however it is
// written, each line cut, and every whole line goes to progress.
func TestChildOutputKeepsTheLastLines(t *testing.T) {
	var got []string
	o := NewChildOutput(func(line string) { got = append(got, line) })
	var all strings.Builder
	for i := range 100 {
		fmt.Fprintf(&all, "line %d\r\n", i)
	}
	all.WriteString(strings.Repeat("x", 3*maxLine) + "\n")
	all.WriteString("partial")
	// In chunks that split lines.
	content := all.String()
	for len(content) > 0 {
		n := min(7, len(content))
		if _, err := o.Stderr().Write([]byte(content[:n])); err != nil {
			t.Fatal(err)
		}
		content = content[n:]
	}

	_, stderr, err := o.Result()
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(stderr), "\n")
	if len(lines) != tailLines+1 || lines[0] != "line 81" || lines[tailLines-2] != "line 99" || lines[tailLines] != "partial" {
		t.Errorf("kept %d lines: first %q, last %q", len(lines), lines[0], lines[len(lines)-1])
	}
	if long := lines[tailLines-1]; len(long) != maxLine+1 {
		t.Errorf("a long line is kept at %d bytes, want %d", len(long), maxLine+1)
	}
	if quoted := tail(stderr); !strings.Contains(quoted, " [cut]") || strings.Contains(quoted, "line 80\n") {
		t.Errorf("tail: %q", quoted[:min(200, len(quoted))])
	}
	if len(got) != 101 || got[0] != "line 0" || got[99] != "line 99" {
		t.Errorf("progress got %d lines, first %q", len(got), got[0])
	}
}

// Standard output is kept whole, up to MaxStdout; more is an error, and
// isn't kept.
func TestChildOutputCapsStdout(t *testing.T) {
	o := NewChildOutput(nil)
	if _, err := o.Stdout().Write([]byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	if stdout, _, err := o.Result(); err != nil || string(stdout) != `{"a":1}` {
		t.Fatalf("got %q, %v", stdout, err)
	}
	big := make([]byte, 1<<20)
	for range MaxStdout >> 20 {
		if _, err := o.Stdout().Write(big); err != nil {
			t.Fatal(err)
		}
	}
	stdout, _, err := o.Result()
	if err == nil || stdout != nil {
		t.Errorf("over the bound: %d bytes, %v", len(stdout), err)
	}
	if o.stdout.buf.Cap() > 2<<20 {
		t.Errorf("still holds %d bytes", o.stdout.buf.Cap())
	}
}

// A client that asks for progress gets the lines the child logs while a
// tool runs, as progress notifications on its request; one that doesn't
// gets none, and the runner is given no progress function.
func TestToolReportsProgress(t *testing.T) {
	needProviders(t, "aws")
	old := progressInterval
	progressInterval = 10 * time.Millisecond
	t.Cleanup(func() { progressInterval = old })

	runner := &fakeRunner{}
	var gotProgress []bool
	run := func(ctx context.Context, args []string, progress func(string)) ([]byte, []byte, int, error) {
		gotProgress = append(gotProgress, progress != nil)
		if progress != nil {
			for i := range 3 {
				progress(fmt.Sprintf("listing service %d", i))
				time.Sleep(30 * time.Millisecond)
			}
		}
		return runner.run(ctx, args, progress)
	}
	var mu sync.Mutex
	var notes []*mcp.ProgressNotificationParams
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	dir := t.TempDir()
	if _, err := New("test", dir, run, runner.check, compiled).Connect(t.Context(), serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test"}, &mcp.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, req *mcp.ProgressNotificationClientRequest) {
			mu.Lock()
			defer mu.Unlock()
			notes = append(notes, req.Params)
		},
	})
	session, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })

	args := map[string]any{"provider": "aws", "services": []string{"vpc"}, "selection": filepath.Join(dir, "selection.yaml")}
	params := &mcp.CallToolParams{Name: "discover", Arguments: args}
	params.SetProgressToken("discover-1")
	res, err := session.CallTool(t.Context(), params)
	if err != nil || res.IsError {
		t.Fatalf("discover: %v %+v", err, res)
	}
	if _, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "discover", Arguments: args}); err != nil {
		t.Fatal(err)
	}

	if len(gotProgress) != 2 || !gotProgress[0] || gotProgress[1] {
		t.Errorf("progress given to the runner: %v, want [true false]", gotProgress)
	}
	// Notifications arrive asynchronously.
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(notes)
		mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(notes) == 0 {
		t.Fatal("no progress notification")
	}
	last := 0.0
	for _, n := range notes {
		if n.ProgressToken != "discover-1" || !strings.HasPrefix(n.Message, "listing service ") || n.Progress <= last {
			t.Errorf("notification: %+v", n)
		}
		last = n.Progress
	}
}
