// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && (!minimal || aws)

package e2e

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IgnatG/infraharvest/internal/cli"
	"github.com/IgnatG/infraharvest/internal/report"
)

// regionsAccount is the account testdata/aws-regions creates its
// resources in, which no other test uses.
const regionsAccount = "333333333333"

// parallelRegions are the regions testdata/aws-regions creates a log
// group in, named after its region.
var parallelRegions = []string{"eu-west-1", "eu-central-1"}

// TestAWSParallelRegions imports the global services and two regions of
// an account of the emulator, one region at a time (--parallel=1) and
// both at once (--parallel=2), into two directories: the log shows the
// regions listing or generating at the same time only with --parallel=2,
// both imports write the same files, and every root plans as imports
// with no changes.
func TestAWSParallelRegions(t *testing.T) {
	endpoint := os.Getenv("AWS_ENDPOINT_URL")
	if endpoint == "" {
		skipUnconfigured(t, "AWS_ENDPOINT_URL is not set; start the emulator in e2e/compose.yaml")
	}
	if !isLoopback(endpoint) {
		t.Fatalf("AWS_ENDPOINT_URL=%s: the test creates resources, so it only runs against a local emulator", endpoint)
	}
	isolateAWSConfig(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	cache := t.TempDir()
	engineName, binary := e2eEngine(t)
	execPath, err := binary.Find(ctx, "", filepath.Join(cache, binary.Name))
	if err != nil {
		t.Fatal(err)
	}
	plugins := filepath.Join(cache, "plugins")
	seed(ctx, t, "aws-regions", execPath, plugins)
	// The imports and plans sign in to the account of the estate, so that
	// they see nothing the other tests create.
	t.Setenv("AWS_ACCESS_KEY_ID", regionsAccount)

	importRegions := func(parallel int) (string, *stepLog) {
		t.Helper()
		out := t.TempDir()
		logged := &stepLog{}
		writer := log.Writer()
		log.SetOutput(io.MultiWriter(writer, logged))
		defer log.SetOutput(writer)
		root := cli.NewCmdRoot()
		root.SetArgs([]string{
			"import", "aws",
			"--engine=" + engineName,
			"--terraform-path=" + execPath,
			"--regions=" + strings.Join(parallelRegions, ","),
			"--services=iam,logs",
			"--parallel=" + strconv.Itoa(parallel),
			"--out=" + out,
			"--all",
		})
		if err := root.ExecuteContext(ctx); err != nil {
			t.Fatalf("infraharvest import --parallel=%d: %v", parallel, err)
		}
		return out, logged
	}
	sequential, sequentialLog := importRegions(1)
	parallel, parallelLog := importRegions(len(parallelRegions))

	first, second := "region "+parallelRegions[0], "region "+parallelRegions[1]
	if steps := sequentialLog.overlapping(t, first, second); len(steps) > 0 {
		t.Errorf("with --parallel=1, %s and %s ran at the same time: %v", first, second, steps)
	}
	if steps := parallelLog.overlapping(t, first, second); len(steps) == 0 {
		t.Errorf("with --parallel=2, %s and %s ran one after the other", first, second)
	} else {
		t.Logf("with --parallel=2, %s and %s ran at the same time: %v", first, second, steps)
	}

	// The output doesn't depend on the order the regions finish in.
	sequentialFiles, parallelFiles := outputFiles(t, sequential), outputFiles(t, parallel)
	for name, content := range sequentialFiles {
		if got, ok := parallelFiles[name]; !ok {
			t.Errorf("--parallel=2 didn't write %s", name)
		} else if got != content {
			t.Errorf("%s differs between --parallel=1 and --parallel=2:\n%s\n---\n%s", name, content, got)
		}
	}
	for name := range parallelFiles {
		if _, ok := sequentialFiles[name]; !ok {
			t.Errorf("--parallel=2 wrote %s, --parallel=1 didn't", name)
		}
	}

	var coverage report.Report
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(parallel, report.Dir, "report.json"))), &coverage); err != nil {
		t.Fatal(err)
	}
	if len(coverage.Failures) != 0 || coverage.Totals.LeftOut != 0 || coverage.Totals.Failed != 0 {
		t.Errorf("report.json: %+v, failures %v", coverage.Totals, coverage.Failures)
	}
	imported := map[string]int{}
	for _, dir := range generatedDirs(t, parallel) {
		for typ, n := range checkNoChanges(ctx, t, dir, execPath, plugins, nil) {
			imported[typ] += n
		}
	}
	if imported["aws_iam_role"] == 0 || imported["aws_cloudwatch_log_group"] != len(parallelRegions) {
		t.Errorf("the roots import %v, want the role and a log group of each region", imported)
	}
	for _, region := range parallelRegions {
		dir := filepath.Join(parallel, "aws", regionsAccount, region)
		if logGroup := "/infraharvest-e2e-regions/" + region; !strings.Contains(configuration(t, dir), logGroup) {
			t.Errorf("%s: the log group %s is missing", dir, logGroup)
		}
	}
}

// stepLog is a log writer that keeps each message the import logs, with
// when it was logged, to tell which of its calls ran at the same time.
type stepLog struct {
	mu      sync.Mutex
	entries []loggedEntry
}

// loggedEntry is a message stepLog kept.
type loggedEntry struct {
	at      time.Time
	message string
}

func (w *stepLog) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.entries = append(w.entries, loggedEntry{at: time.Now(), message: strings.TrimSpace(string(p))})
	return len(p), nil
}

// callSteps are the steps of an AWS Import call the log shows, by the
// messages that start and finish them (see importer.Run.Import).
var callSteps = []struct{ start, end string }{
	{"importing resources", "listed resources"},
	{"generating the roots", "generated the roots"},
}

// interval returns when the call that covers where, such as "region
// eu-west-1", started a step and when it finished it: the first message
// "aws: <start> in <where>", and the first "aws: <end> in <where>" after
// it.
func (w *stepLog) interval(where, start, end string) (from, to time.Time, ok bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	started := false
	for _, e := range w.entries {
		switch {
		case !started && strings.HasSuffix(e.message, "aws: "+start+" in "+where):
			from, started = e.at, true
		case started && strings.HasSuffix(e.message, "aws: "+end+" in "+where):
			return from, e.at, true
		}
	}
	return from, to, false
}

// overlapping returns the steps (see callSteps) the calls that cover a
// and b ran at the same time in, one starting before the other finished.
// It fails the test for a step the log doesn't show both start and
// finish.
func (w *stepLog) overlapping(t *testing.T, a, b string) []string {
	t.Helper()
	var steps []string
	for _, s := range callSteps {
		aFrom, aTo, aOK := w.interval(a, s.start, s.end)
		bFrom, bTo, bOK := w.interval(b, s.start, s.end)
		if !aOK || !bOK {
			t.Errorf("the log doesn't show both %s and %s from %q to %q", a, b, s.start, s.end)
			continue
		}
		if aFrom.Before(bTo) && bFrom.Before(aTo) {
			steps = append(steps, s.start)
		}
	}
	return steps
}
