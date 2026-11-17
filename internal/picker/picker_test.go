// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package picker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/IgnatG/infraharvest/internal/selection"
)

const (
	euWest = "aws/123456789012/eu-west-2"
	global = "aws/123456789012/global"
)

func testFile() *selection.File {
	return &selection.File{Version: selection.Version, Defaults: selection.Defaults{Include: true}, Resources: []selection.Resource{
		{Type: "aws_s3_bucket", ID: "logs", Scope: euWest, Include: true},
		{Type: "aws_s3_bucket", ID: "assets", Scope: euWest, Include: true},
		{Type: "aws_vpc", ID: "vpc-default", Scope: euWest, Reason: "default VPC"},
		{Type: "aws_iam_role", ID: "ci", Scope: global, Include: true, New: true},
	}}
}

func labels(m *Model) []string {
	var out []string
	for _, r := range m.rows() {
		out = append(out, strings.Repeat(" ", r.depth)+r.label)
	}
	return out
}

func press(m *Model, keys ...string) {
	for _, k := range keys {
		text := ""
		if len(k) == 1 {
			text = k
		}
		m.key(k, text)
	}
}

func TestRows(t *testing.T) {
	m := New("selection.yaml", testFile(), nil)
	// Scopes start open, types closed.
	want := []string{euWest, " aws_s3_bucket", " aws_vpc", global, " aws_iam_role"}
	if got := labels(m); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("rows: got %q, want %q", got, want)
	}
	press(m, "down", "right")
	want = []string{euWest, " aws_s3_bucket", "  assets", "  logs", " aws_vpc", global, " aws_iam_role"}
	if got := labels(m); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("rows after opening the buckets: got %q, want %q", got, want)
	}
	// Left on a resource closes its group, and moves there.
	press(m, "down", "left")
	if m.cursor != 1 || len(m.rows()) != 5 {
		t.Errorf("cursor %d, rows %q", m.cursor, labels(m))
	}
}

func TestToggleGroup(t *testing.T) {
	f := testFile()
	m := New("selection.yaml", f, nil)
	press(m, "down") // aws_s3_bucket, all included
	press(m, "space")
	if f.Resources[0].Include || f.Resources[1].Include || !m.changed {
		t.Errorf("space on an included group: want it excluded, got %+v", f.Resources[:2])
	}
	press(m, "space")
	if !f.Resources[0].Include || !f.Resources[1].Include {
		t.Errorf("space on an excluded group: want it included, got %+v", f.Resources[:2])
	}
	// A partly included group is included whole.
	f.Resources[0].Include = false
	press(m, "space")
	if !f.Resources[0].Include {
		t.Error("space on a partly included group: want it included")
	}
}

func TestDecidingReviews(t *testing.T) {
	f := testFile()
	m := New("selection.yaml", f, nil)
	press(m, "u") // new only: the IAM role
	if got := labels(m); len(got) != 2 || got[1] != " aws_iam_role" {
		t.Fatalf("new only: %q", got)
	}
	press(m, "down", "right", "down", "space")
	if f.Resources[3].Include || f.Resources[3].New {
		t.Errorf("deciding on a new resource: want it excluded and reviewed, got %+v", f.Resources[3])
	}
}

func TestSearch(t *testing.T) {
	f := testFile()
	m := New("selection.yaml", f, nil)
	press(m, "/", "l", "o", "g", "enter")
	if m.searching || m.search != "log" {
		t.Fatalf("searching %v for %q", m.searching, m.search)
	}
	// A search opens every group with a match.
	want := []string{euWest, " aws_s3_bucket", "  logs"}
	if got := labels(m); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("rows: got %q, want %q", got, want)
	}
	press(m, "n")
	if f.Resources[0].Include || !f.Resources[1].Include {
		t.Errorf("n excludes what is shown only: %+v", f.Resources[:2])
	}
	press(m, "esc")
	if m.search != "" || len(m.rows()) != 5 {
		t.Errorf("esc: search %q, rows %q", m.search, labels(m))
	}
}

func TestSummary(t *testing.T) {
	m := New("selection.yaml", testFile(), nil)
	got := m.summary()
	for _, want := range []string{"3 of 4 resources selected", "2 roots", "up to 2 calls of terraform-aws-modules/s3-bucket/aws", "1 new to review"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary %q misses %q", got, want)
		}
	}
	if !strings.Contains(m.render(), got) {
		t.Error("the view doesn't show the summary")
	}
}

func TestSaveAndQuit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "selection.yaml")
	f := testFile()
	m := New(path, f, nil)
	press(m, "down", "space") // exclude the buckets
	// Quitting with changes asks first.
	if quit := m.key("q", "q"); quit || !m.confirmQuit || !strings.Contains(m.render(), "Unsaved changes") {
		t.Fatalf("q with changes: quit %v, confirm %v", quit, m.confirmQuit)
	}
	if quit := m.key("s", "s"); !quit || !m.saved {
		t.Fatalf("s: quit %v, saved %v", quit, m.saved)
	}
	saved, err := selection.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Has("aws_s3_bucket", "logs") && saved.Decide("aws_s3_bucket", "logs", "").Include {
		t.Error("the saved file still includes the bucket")
	}

	unchanged := New(path, testFile(), nil)
	if quit := unchanged.key("q", "q"); !quit || unchanged.saved {
		t.Errorf("q without changes: quit %v, saved %v", quit, unchanged.saved)
	}
}

func TestScroll(t *testing.T) {
	f := &selection.File{Version: selection.Version}
	for _, id := range []string{"a", "b", "c", "d", "e", "f"} {
		f.Resources = append(f.Resources, selection.Resource{Type: "aws_sqs_queue", ID: id, Include: true})
	}
	m := New("selection.yaml", f, nil)
	m.height = 3
	press(m, "right", "end")
	if m.cursor != 6 || m.offset != 4 {
		t.Errorf("end: cursor %d, offset %d", m.cursor, m.offset)
	}
	press(m, "home")
	if m.cursor != 0 || m.offset != 0 {
		t.Errorf("home: cursor %d, offset %d", m.cursor, m.offset)
	}
}

func TestTagFilter(t *testing.T) {
	f := testFile()
	f.Resources[0].Tags = map[string]string{"Team": "Data", "env": "prod"}
	f.Resources[1].Tags = map[string]string{"team": "web"}
	m := New("selection.yaml", f, nil)
	for search, want := range map[string][]string{
		"team=data":          {"logs"},
		"team=":              {"vpc-default", "ci"},
		"team=w":             {"assets"},
		"env=prod team=data": {"logs"},
		"env=prod team=web":  nil,
		"bucket team=web":    {"assets"},
		"env=":               {"assets", "vpc-default", "ci"},
		"team=data ":         {"logs"},
	} {
		m.search = search
		var got []string
		for _, r := range f.Resources {
			if m.shown(r) {
				got = append(got, r.ID)
			}
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%q: got %v, want %v", search, got, want)
		}
	}
	m.search = "prod"
	if !m.shown(f.Resources[0]) || m.shown(f.Resources[1]) {
		t.Error("a search without = doesn't match tag values")
	}
}

// Backspace removes the last character of the search, however many bytes
// it takes.
func TestBackspaceRemovesACharacter(t *testing.T) {
	m := New("selection.yaml", testFile(), nil)
	m.key("/", "/")
	m.key("é", "é")
	m.key("ü", "ü")
	m.key("backspace", "")
	if m.search != "é" || !utf8.ValidString(m.search) {
		t.Errorf("search %q, want %q", m.search, "é")
	}
	m.key("backspace", "")
	m.key("backspace", "")
	if m.search != "" {
		t.Errorf("search %q, want it empty", m.search)
	}
}

// Moving the cursor reuses the tree; what changes the tree lays it out
// again.
func TestRowsAreCached(t *testing.T) {
	m := New("selection.yaml", testFile(), nil)
	first := m.rows()
	press(m, "down", "down")
	if again := m.rows(); &again[0] != &first[0] {
		t.Error("moving the cursor laid the tree out again")
	}
	press(m, "up", "right")
	if got := len(m.rows()); got != 7 {
		t.Errorf("opening a group: %d rows, want 7: %q", got, labels(m))
	}
}

// A name the cloud gives a resource, with a terminal escape sequence in it,
// is drawn with the sequence shown, not sent to the terminal.
func TestRowsShowEscapesInNames(t *testing.T) {
	f := &selection.File{Version: selection.Version, Defaults: selection.Defaults{Include: true}, Resources: []selection.Resource{
		{Type: "aws_s3_bucket", ID: "logs\x1b[2J", Name: "\x1b]0;owned\x07", Scope: euWest, Include: true},
	}}
	m := New("selection.yaml", f, nil)
	press(m, "down", "right")
	for _, label := range labels(m) {
		if strings.ContainsAny(label, "\x1b\x07") {
			t.Errorf("the row %q holds a control character", label)
		}
	}
	if got := labels(m); !strings.Contains(strings.Join(got, "|"), `logs\x1b[2J`) {
		t.Errorf("rows %q don't show the escape", got)
	}
}

// A scope's row lists its resources in the file's order, whatever the
// order the map of its types is ranged in.
func TestScopeRowOrder(t *testing.T) {
	f := &selection.File{Version: selection.Version, Defaults: selection.Defaults{Include: true}}
	for i := range 40 {
		f.Resources = append(f.Resources, selection.Resource{Type: "aws_type_" + string(rune('a'+i%20)), ID: "id", Scope: euWest, Include: true})
		f.Resources[i].ID = strings.Repeat("x", i+1)
	}
	f.Resources = append(f.Resources, selection.Resource{Type: "aws_vpc", ID: "v", Scope: global, Include: true})
	m := New(filepath.Join(t.TempDir(), "selection.yaml"), f, nil)
	for range 20 {
		m.invalidate()
		for _, r := range m.rows() {
			if r.depth != 0 {
				continue
			}
			for i := 1; i < len(r.resources); i++ {
				if r.resources[i-1] > r.resources[i] {
					t.Fatalf("scope row %q lists %v", r.label, r.resources)
				}
			}
		}
	}
}

// Saving the file leaves the rows pointing at the resources they were
// for, even when the loaded file wasn't sorted.
func TestToggleAfterSave(t *testing.T) {
	f := &selection.File{Version: selection.Version, Defaults: selection.Defaults{Include: true}, Resources: []selection.Resource{
		{Type: "aws_vpc", ID: "vpc-b", Scope: euWest, Include: true},
		{Type: "aws_iam_role", ID: "app", Scope: euWest, Include: true},
	}}
	m := New(filepath.Join(t.TempDir(), "selection.yaml"), f, nil)
	m.rows()
	if err := f.Save(m.path); err != nil {
		t.Fatal(err)
	}
	press(m, "n") // exclude everything the rows list
	for _, r := range f.Resources {
		if r.Include {
			t.Errorf("%s %s is still included", r.Type, r.ID)
		}
	}
}

// These tests drive the bubbletea model the way the program does: key and
// window-size messages through Update, and the tea.Quit command it answers
// with, so what the picker returns is checked without a terminal.

// keyMsg is the message the terminal sends for a key, by name.
func keyMsg(name string) tea.Msg {
	switch name {
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "home":
		return tea.KeyPressMsg{Code: tea.KeyHome}
	case "end":
		return tea.KeyPressMsg{Code: tea.KeyEnd}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	}
	r := []rune(name)
	if len(r) != 1 {
		panic("keyMsg: unknown key " + name)
	}
	return tea.KeyPressMsg{Code: r[0], Text: name}
}

// drive sends the keys through m.Update, as the program would, and reports
// whether the picker asked to quit.
func drive(t *testing.T, m *Model, keys ...string) (quit bool) {
	t.Helper()
	for _, k := range keys {
		if quit {
			t.Fatalf("key %q after the picker quit", k)
		}
		next, cmd := m.Update(keyMsg(k))
		if next != tea.Model(m) {
			t.Fatalf("Update returned a different model: %T", next)
		}
		if cmd != nil {
			if _, ok := cmd().(tea.QuitMsg); !ok {
				t.Fatalf("key %q: unexpected command", k)
			}
			quit = true
		}
	}
	return quit
}

// savedFile writes testFile to a new selection file and returns its path.
func savedFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "selection.yaml")
	if err := testFile().Save(path); err != nil {
		t.Fatal(err)
	}
	return path
}

// onDisk loads the selection file at path as Include per "type id".
func onDisk(t *testing.T, path string) map[string]bool {
	t.Helper()
	f, err := selection.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, r := range f.Resources {
		got[r.Type+" "+r.ID] = r.Include
	}
	return got
}

func TestKeyMessagesMatchTheNamesTheModelHandles(t *testing.T) {
	for name, want := range map[string]string{
		"space": "space", "enter": "enter", "esc": "esc", "up": "up", "down": "down",
		"left": "left", "right": "right", "backspace": "backspace", "ctrl+c": "ctrl+c", "q": "q", "/": "/",
	} {
		if got := keyMsg(name).(tea.KeyPressMsg).String(); got != want {
			t.Errorf("key %q reads as %q, want %q", name, got, want)
		}
	}
}

func TestWindowSizeSetsHowManyRowsShow(t *testing.T) {
	m := New("selection.yaml", testFile(), nil)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if m.height != 22 {
		t.Errorf("height %d, want 22: the header, summary and help take 8 lines", m.height)
	}
	// A tiny terminal still shows a few rows.
	m.Update(tea.WindowSizeMsg{Width: 20, Height: 4})
	if m.height != 3 {
		t.Errorf("height %d, want at least 3", m.height)
	}
	// Shrinking keeps the cursor in view.
	f := &selection.File{Version: selection.Version}
	for _, id := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		f.Resources = append(f.Resources, selection.Resource{Type: "aws_sqs_queue", ID: id, Include: true})
	}
	m = New("selection.yaml", f, nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	drive(t, m, "right")
	drive(t, m, "down", "down", "down", "down", "down", "down", "down", "down")
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 11})
	if m.cursor != 8 || m.cursor < m.offset || m.cursor >= m.offset+m.height {
		t.Errorf("cursor %d outside the %d rows from %d", m.cursor, m.height, m.offset)
	}
}

func TestViewIsTheRenderedPickerOnTheAltScreen(t *testing.T) {
	m := New("selection.yaml", testFile(), nil)
	v := m.View()
	if !v.AltScreen {
		t.Error("the picker should use the alternate screen")
	}
	if want := m.render(); v.Content != want || want == "" {
		t.Errorf("view %q, want the rendered picker", v.Content)
	}
}

func TestToggleOneResource(t *testing.T) {
	path := savedFile(t)
	f, err := selection.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	m := New(path, f, nil)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})

	// Open the buckets and exclude "assets", the first of them.
	drive(t, m, "down", "right", "down")
	if got := labels(m); got[2] != "  assets" || m.cursor != 2 {
		t.Fatalf("cursor %d on rows %q", m.cursor, got)
	}
	drive(t, m, "space")
	if m.included([]int{indexOf(t, f, "assets")}) != 0 || m.included([]int{indexOf(t, f, "logs")}) != 1 {
		t.Fatalf("space on one bucket: %+v", f.Resources)
	}
	if !m.changed {
		t.Error("a decision should mark the picker changed")
	}
	// Its group is now partly included, and space includes it whole.
	drive(t, m, "up")
	if m.state(m.rows()[m.cursor].resources) != stateSome {
		t.Error("the bucket group should be partly included")
	}
	drive(t, m, "space")
	if got := onDiskAfterSave(t, m, path); !got["aws_s3_bucket assets"] || !got["aws_s3_bucket logs"] {
		t.Errorf("saved %v, want both buckets included", got)
	}
}

// onDiskAfterSave presses s and reads the file back.
func onDiskAfterSave(t *testing.T, m *Model, path string) map[string]bool {
	t.Helper()
	if !drive(t, m, "s") || !m.saved || m.err != nil {
		t.Fatalf("s: saved %v, err %v", m.saved, m.err)
	}
	return onDisk(t, path)
}

func indexOf(t *testing.T, f *selection.File, id string) int {
	t.Helper()
	for i, r := range f.Resources {
		if r.ID == id {
			return i
		}
	}
	t.Fatalf("no resource %q", id)
	return -1
}

func TestEnterOnAResourceTogglesAndOnAGroupOpensIt(t *testing.T) {
	f := testFile()
	m := New("selection.yaml", f, nil)
	drive(t, m, "down", "enter") // the bucket group opens
	if got := len(m.rows()); got != 7 {
		t.Fatalf("enter on a group: %d rows, want 7: %q", got, labels(m))
	}
	drive(t, m, "down", "enter") // assets: excluded
	if f.Resources[indexOf(t, f, "assets")].Include {
		t.Error("enter on a resource should exclude it")
	}
	drive(t, m, "enter")
	if !f.Resources[indexOf(t, f, "assets")].Include {
		t.Error("enter again should include it")
	}
}

func TestSelectAllAndNone(t *testing.T) {
	path := savedFile(t)
	f, err := selection.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	m := New(path, f, nil)

	drive(t, m, "n")
	for _, r := range f.Resources {
		if r.Include {
			t.Errorf("n left %s %s included", r.Type, r.ID)
		}
	}
	if got := m.summary(); got == "" || !m.changed {
		t.Errorf("summary %q, changed %v", got, m.changed)
	}
	drive(t, m, "a")
	for _, r := range f.Resources {
		if !r.Include || r.New {
			t.Errorf("a: %s %s include %v new %v, want included and reviewed", r.Type, r.ID, r.Include, r.New)
		}
	}
	// Even the default VPC, which the file left out, is included now.
	got := onDiskAfterSave(t, m, path)
	for key, include := range got {
		if !include {
			t.Errorf("saved %s as excluded", key)
		}
	}
	if len(got) != 4 {
		t.Errorf("saved %d resources, want 4", len(got))
	}
}

func TestSearchThenDecideThroughUpdate(t *testing.T) {
	path := savedFile(t)
	f, err := selection.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	m := New(path, f, nil)

	// While typing, letters go to the search, not to the commands (n, a, q).
	drive(t, m, "/", "a", "n", "q")
	if m.search != "anq" || !m.searching || m.changed {
		t.Fatalf("typing: search %q, searching %v, changed %v", m.search, m.searching, m.changed)
	}
	drive(t, m, "backspace", "backspace", "backspace", "s", "s", "e", "t", "s", "enter")
	if m.search != "ssets" || m.searching {
		t.Fatalf("search %q, searching %v", m.search, m.searching)
	}
	if got := labels(m); len(got) != 3 || got[2] != "  assets" {
		t.Fatalf("rows %q, want the assets bucket only", got)
	}
	// n excludes what the search shows, and nothing else.
	drive(t, m, "n")
	got := onDiskAfterSave(t, m, path)
	want := map[string]bool{
		"aws_s3_bucket assets": false,
		"aws_s3_bucket logs":   true,
		"aws_vpc vpc-default":  false,
		"aws_iam_role ci":      true,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("saved %s include %v, want %v", k, got[k], v)
		}
	}
}

func TestSearchEscapeAndNoMatch(t *testing.T) {
	m := New("selection.yaml", testFile(), nil)
	drive(t, m, "/", "z", "z", "enter")
	if got := m.rows(); len(got) != 0 {
		t.Errorf("rows %q, want none", labels(m))
	}
	// Nothing shown: the commands that act on rows do nothing.
	drive(t, m, "space", "a", "n", "down", "end")
	if m.changed {
		t.Error("deciding with nothing shown changed something")
	}
	drive(t, m, "esc")
	if len(m.rows()) != 5 {
		t.Errorf("esc: rows %q", labels(m))
	}
}

func TestConfirmWritesTheSelectionFile(t *testing.T) {
	path := savedFile(t)
	f, err := selection.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	m := New(path, f, nil)
	drive(t, m, "down", "space") // exclude both buckets
	if !drive(t, m, "s") {
		t.Fatal("s should quit")
	}
	if !m.saved || m.err != nil {
		t.Fatalf("saved %v, err %v", m.saved, m.err)
	}
	got := onDisk(t, path)
	if got["aws_s3_bucket logs"] || got["aws_s3_bucket assets"] {
		t.Errorf("the buckets are still included on disk: %v", got)
	}
	if !got["aws_iam_role ci"] {
		t.Errorf("the role should still be included: %v", got)
	}
}

func TestConfirmReportsAFailedSave(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	m := New(filepath.Join(blocker, "selection.yaml"), testFile(), nil)
	if !drive(t, m, "s") {
		t.Fatal("s should quit even when the save fails")
	}
	if m.saved || m.err == nil {
		t.Errorf("saved %v, err %v: want the error kept", m.saved, m.err)
	}
}

func TestCancelLeavesTheSelectionFileAsItWas(t *testing.T) {
	cancels := map[string][]string{
		"q twice":            {"q", "q"},
		"ctrl+c":             {"ctrl+c"},
		"ctrl+c in a search": {"/", "l", "ctrl+c"},
	}
	for name, keys := range cancels {
		t.Run(name, func(t *testing.T) {
			path := savedFile(t)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			f, err := selection.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			m := New(path, f, nil)
			drive(t, m, "n") // decide everything out
			if !m.changed {
				t.Fatal("n should change the selection")
			}
			if !drive(t, m, keys...) {
				t.Fatalf("%v should quit", keys)
			}
			if m.saved || m.err != nil {
				t.Errorf("saved %v, err %v", m.saved, m.err)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Errorf("the file changed:\n%s", after)
			}
		})
	}
}

func TestQuitAsksOnlyAboutUnsavedChanges(t *testing.T) {
	path := savedFile(t)
	f, err := selection.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	m := New(path, f, nil)
	if !drive(t, m, "q") || m.saved {
		t.Error("q without changes should quit without saving")
	}

	m = New(path, f, nil)
	drive(t, m, "n")
	if drive(t, m, "q") || !m.confirmQuit {
		t.Fatal("q with changes should ask first")
	}
	// Any other key takes the question back.
	if drive(t, m, "down") || m.confirmQuit {
		t.Error("a key after the question should cancel it")
	}
	if drive(t, m, "q") || !m.confirmQuit {
		t.Error("q should ask again")
	}
	// s on the question saves.
	if !drive(t, m, "s") || !m.saved {
		t.Error("s on the question should save and quit")
	}
	if got := onDisk(t, path); got["aws_s3_bucket logs"] {
		t.Errorf("saved %v, want everything excluded", got)
	}
}

func TestOnlyNewFilterThroughUpdate(t *testing.T) {
	f := testFile()
	m := New("selection.yaml", f, nil)
	drive(t, m, "u")
	if got := labels(m); len(got) != 2 || got[1] != " aws_iam_role" {
		t.Fatalf("new only: %q", got)
	}
	// Deciding reviews the resource, so it leaves the filter.
	drive(t, m, "a")
	if got := m.rows(); len(got) != 0 {
		t.Errorf("rows %q after reviewing the only new resource", labels(m))
	}
	drive(t, m, "u")
	if len(m.rows()) != 5 {
		t.Errorf("u again: rows %q", labels(m))
	}
}
