// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package picker is an interactive terminal picker for a selection file:
// a tree of the listed resources by scope (provider, account and region)
// and type, to include or exclude one at a time or a group at once, with a
// live summary of what an import would bring under Terraform.
package picker

import (
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/IgnatG/infraharvest/adapters"
	"github.com/IgnatG/infraharvest/selection"
)

// Run lets people edit f, read from path, in the terminal, and saves it to
// path if they ask to. It reports whether it saved.
func Run(path string, f *selection.File) (bool, error) {
	final, err := tea.NewProgram(New(path, f)).Run()
	if err != nil {
		return false, err
	}
	m, ok := final.(*Model)
	if !ok {
		return false, fmt.Errorf("picker: unexpected model %T", final)
	}
	return m.saved, m.err
}

// Model is the picker's state.
type Model struct {
	path string
	file *selection.File
	// calls has, by anchor type, the curated module a resource of that type
	// may become a call of.
	calls map[string]string

	expanded  map[string]bool // group keys (see row)
	cursor    int
	offset    int // first row shown
	height    int // rows the tree may take
	search    string
	searching bool
	onlyNew   bool

	changed     bool
	confirmQuit bool
	saved       bool
	err         error
}

// New returns a picker of f, which it saves to path.
func New(path string, f *selection.File) *Model {
	m := &Model{path: path, file: f, calls: map[string]string{}, expanded: map[string]bool{}, height: 20}
	for _, provider := range adapters.Providers() {
		for _, a := range adapters.For(provider) {
			if _, ok := m.calls[a.Anchor]; !ok {
				m.calls[a.Anchor] = a.Source
			}
		}
	}
	for _, scope := range m.scopes() {
		m.expanded[scopeKey(scope)] = true
	}
	return m
}

// row is a line of the tree: a scope, a type in a scope, or a resource.
type row struct {
	key       string // group key, or "" for a resource
	depth     int
	label     string
	resources []int // indexes into file.Resources, shown ones only
}

func scopeKey(scope string) string     { return "scope\x00" + scope }
func typeKey(scope, typ string) string { return "type\x00" + scope + "\x00" + typ }
func (r row) group() bool              { return r.key != "" }
func scopeLabel(scope string) string {
	if scope == "" {
		return "(no scope recorded)"
	}
	return scope
}

// scopes returns the scopes of the file's resources, sorted.
func (m *Model) scopes() []string {
	seen := map[string]bool{}
	var scopes []string
	for _, r := range m.file.Resources {
		if !seen[r.Scope] {
			seen[r.Scope] = true
			scopes = append(scopes, r.Scope)
		}
	}
	sort.Strings(scopes)
	return scopes
}

// shown reports whether the filters let a resource through.
func (m *Model) shown(r selection.Resource) bool {
	if m.onlyNew && !r.New {
		return false
	}
	if m.search == "" {
		return true
	}
	if !strings.Contains(m.search, "=") {
		return matchesText(r, strings.ToLower(m.search))
	}
	// key=value terms filter by tag, all of them; other terms search.
	for _, term := range strings.Fields(strings.ToLower(m.search)) {
		key, value, isTag := strings.Cut(term, "=")
		if !isTag || key == "" {
			if !matchesText(r, term) {
				return false
			}
			continue
		}
		if !matchesTag(r.Tags, key, value) {
			return false
		}
	}
	return true
}

// matchesText reports whether needle, in lower case, is part of the
// resource's type, ID, name, note, reason or tags.
func matchesText(r selection.Resource, needle string) bool {
	for _, s := range []string{r.Type, r.ID, r.Name, r.Note, r.Reason} {
		if strings.Contains(strings.ToLower(s), needle) {
			return true
		}
	}
	for k, v := range r.Tags {
		if strings.Contains(strings.ToLower(k+"="+v), needle) {
			return true
		}
	}
	return false
}

// matchesTag reports whether tags has key with a value that value, in lower
// case, is part of; with value "", whether tags lacks key. Keys match
// whatever their case.
func matchesTag(tags map[string]string, key, value string) bool {
	for k, v := range tags {
		if strings.ToLower(k) == key {
			return value != "" && strings.Contains(strings.ToLower(v), value)
		}
	}
	return value == ""
}

// rows lays out the tree: scopes, if there is more than one, then types,
// then resources, as far as their groups are expanded. A search expands
// every group with a match.
func (m *Model) rows() []row {
	byScope := map[string]map[string][]int{}
	for i, r := range m.file.Resources {
		if !m.shown(r) {
			continue
		}
		if byScope[r.Scope] == nil {
			byScope[r.Scope] = map[string][]int{}
		}
		byScope[r.Scope][r.Type] = append(byScope[r.Scope][r.Type], i)
	}
	scopes := make([]string, 0, len(byScope))
	for s := range byScope {
		scopes = append(scopes, s)
	}
	sort.Strings(scopes)
	withScopes := len(m.scopes()) > 1
	open := func(key string) bool { return m.expanded[key] || m.search != "" }

	var rows []row
	for _, scope := range scopes {
		types := make([]string, 0, len(byScope[scope]))
		var all []int
		for t, indexes := range byScope[scope] {
			types = append(types, t)
			all = append(all, indexes...)
		}
		sort.Strings(types)
		depth := 0
		if withScopes {
			rows = append(rows, row{key: scopeKey(scope), label: scopeLabel(scope), resources: all})
			if !open(scopeKey(scope)) {
				continue
			}
			depth = 1
		}
		for _, t := range types {
			indexes := byScope[scope][t]
			sort.Slice(indexes, func(a, b int) bool {
				return m.file.Resources[indexes[a]].ID < m.file.Resources[indexes[b]].ID
			})
			rows = append(rows, row{key: typeKey(scope, t), depth: depth, label: t, resources: indexes})
			if !open(typeKey(scope, t)) {
				continue
			}
			for _, i := range indexes {
				r := m.file.Resources[i]
				label := r.ID
				if r.Name != "" && r.Name != r.ID {
					label += "  " + r.Name
				}
				rows = append(rows, row{depth: depth + 1, label: label, resources: []int{i}})
			}
		}
	}
	return rows
}

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd { return nil }

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		// Header, blank lines, summary and help take the rest.
		m.height = max(msg.Height-8, 3)
		m.scroll()
	case tea.KeyPressMsg:
		if m.key(msg.String(), msg.Text) {
			return m, tea.Quit
		}
	}
	return m, nil
}

// key handles a key press, by name (such as "down", "space" or "q") and
// the text it types, and reports whether to quit.
func (m *Model) key(name, text string) bool {
	if name == "ctrl+c" {
		return true
	}
	if m.searching {
		switch name {
		case "enter":
			m.searching = false
		case "esc":
			m.searching, m.search = false, ""
		case "backspace":
			if m.search != "" {
				m.search = m.search[:len(m.search)-1]
			}
		default:
			if text != "" {
				m.search += text
			}
		}
		m.cursor = 0
		m.scroll()
		return false
	}
	quitting := m.confirmQuit
	m.confirmQuit = false
	rows := m.rows()
	switch name {
	case "up", "k":
		m.cursor--
	case "down", "j":
		m.cursor++
	case "pgup":
		m.cursor -= m.height
	case "pgdown":
		m.cursor += m.height
	case "home", "g":
		m.cursor = 0
	case "end", "G":
		m.cursor = len(rows) - 1
	case "right", "l":
		m.setExpanded(rows, true)
	case "left", "h":
		m.setExpanded(rows, false)
	case "enter":
		if m.cursor < len(rows) && rows[m.cursor].group() {
			m.expanded[rows[m.cursor].key] = !m.expanded[rows[m.cursor].key]
		} else {
			m.toggle(rows)
		}
	case "space":
		m.toggle(rows)
	case "a", "n":
		var all []int
		for _, r := range rows {
			if r.depth == 0 {
				all = append(all, r.resources...)
			}
		}
		m.include(all, name == "a")
	case "/":
		m.searching = true
	case "esc":
		m.search = ""
	case "u":
		m.onlyNew = !m.onlyNew
		m.cursor = 0
	case "s":
		if err := m.file.Save(m.path); err != nil {
			m.err = err
			return true
		}
		m.saved = true
		return true
	case "q":
		if !m.changed || quitting {
			return true
		}
		m.confirmQuit = true
	}
	m.scroll()
	return false
}

// setExpanded expands or collapses the group at the cursor; collapsing a
// resource collapses its group.
func (m *Model) setExpanded(rows []row, open bool) {
	if m.cursor >= len(rows) {
		return
	}
	r := rows[m.cursor]
	if r.group() {
		m.expanded[r.key] = open
		return
	}
	if open {
		return
	}
	for i := m.cursor - 1; i >= 0; i-- {
		if rows[i].group() && rows[i].depth < r.depth {
			m.expanded[rows[i].key] = false
			m.cursor = i
			return
		}
	}
}

// toggle includes the resources of the row at the cursor, or excludes them
// if they are all included already.
func (m *Model) toggle(rows []row) {
	if m.cursor >= len(rows) {
		return
	}
	indexes := rows[m.cursor].resources
	m.include(indexes, m.state(indexes) != stateAll)
}

// include sets whether to import the resources at indexes. Deciding
// reviews them: they lose their new mark.
func (m *Model) include(indexes []int, include bool) {
	for _, i := range indexes {
		r := &m.file.Resources[i]
		if r.Include != include || r.New {
			m.changed = true
		}
		r.Include, r.New = include, false
	}
}

// scroll keeps the cursor on a row, and in view.
func (m *Model) scroll() {
	n := len(m.rows())
	m.cursor = min(max(m.cursor, 0), max(n-1, 0))
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+m.height {
		m.offset = m.cursor - m.height + 1
	}
	m.offset = min(m.offset, max(n-m.height, 0))
}

type state int

const (
	stateNone state = iota
	stateSome
	stateAll
)

func (m *Model) state(indexes []int) state {
	included := 0
	for _, i := range indexes {
		if m.file.Resources[i].Include {
			included++
		}
	}
	switch {
	case included == 0:
		return stateNone
	case included == len(indexes):
		return stateAll
	}
	return stateSome
}

var (
	titleStyle  = lipgloss.NewStyle().Bold(true)
	cursorStyle = lipgloss.NewStyle().Reverse(true)
	faintStyle  = lipgloss.NewStyle().Faint(true)
	newStyle    = lipgloss.NewStyle().Bold(true)
)

// View implements tea.Model.
func (m *Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

// render draws the picker: title, tree, summary and keys.
func (m *Model) render() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("infraharvest pick: "+m.path) + "\n")
	switch {
	case m.searching:
		b.WriteString("search: " + m.search + "▏\n")
	case m.search != "" || m.onlyNew:
		var filters []string
		if m.search != "" {
			filters = append(filters, fmt.Sprintf("matching %q", m.search))
		}
		if m.onlyNew {
			filters = append(filters, "new only")
		}
		b.WriteString(faintStyle.Render("showing "+strings.Join(filters, ", ")+" (esc clears the search, u the new filter)") + "\n")
	default:
		b.WriteString("\n")
	}
	rows := m.rows()
	if len(rows) == 0 {
		b.WriteString(faintStyle.Render("  nothing matches") + "\n")
	}
	for i := m.offset; i < len(rows) && i < m.offset+m.height; i++ {
		line := m.renderRow(rows[i])
		if i == m.cursor {
			line = cursorStyle.Render(line)
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\n" + m.summary() + "\n")
	switch {
	case m.confirmQuit:
		b.WriteString(newStyle.Render("Unsaved changes: s saves them, q again discards them.") + "\n")
	case m.searching:
		b.WriteString(faintStyle.Render("type to search, key=value for a tag (key= for untagged) · enter done · esc clear") + "\n")
	default:
		b.WriteString(faintStyle.Render("↑↓ move · → ← open, close · space include/exclude · a/n all/none shown · / search · u new only · s save · q quit") + "\n")
	}
	return b.String()
}

func (m *Model) renderRow(r row) string {
	box := map[state]string{stateNone: "[ ]", stateSome: "[-]", stateAll: "[x]"}[m.state(r.resources)]
	indent := strings.Repeat("  ", r.depth)
	if r.group() {
		arrow := "▸"
		if m.expanded[r.key] || m.search != "" {
			arrow = "▾"
		}
		return fmt.Sprintf("%s%s %s %s %s", indent, arrow, box, r.label, faintStyle.Render(fmt.Sprintf("(%d/%d)", m.included(r.resources), len(r.resources))))
	}
	res := m.file.Resources[r.resources[0]]
	line := fmt.Sprintf("%s  %s %s", indent, box, r.label)
	if res.New {
		line += " " + newStyle.Render("new")
	}
	if res.Reason != "" {
		line += " " + faintStyle.Render("· "+res.Reason)
	}
	return line
}

func (m *Model) included(indexes []int) int {
	n := 0
	for _, i := range indexes {
		if m.file.Resources[i].Include {
			n++
		}
	}
	return n
}

// summary says what an import of the selection would bring under
// Terraform: how many resources, into how many roots, how many of them may
// become curated module calls, and what is left to review.
func (m *Model) summary() string {
	included, newCount := 0, 0
	scopes := map[string]bool{}
	calls := map[string]int{}
	for _, r := range m.file.Resources {
		if r.New {
			newCount++
		}
		if !r.Include {
			continue
		}
		included++
		scopes[r.Scope] = true
		if source, ok := m.calls[r.Type]; ok {
			calls[source]++
		}
	}
	parts := []string{fmt.Sprintf("%d of %d resources selected", included, len(m.file.Resources))}
	if !scopes[""] && len(scopes) > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", len(scopes), plural(len(scopes), "root", "roots")))
	}
	sources := make([]string, 0, len(calls))
	for s := range calls {
		sources = append(sources, s)
	}
	sort.Strings(sources)
	for _, s := range sources {
		parts = append(parts, fmt.Sprintf("up to %d %s of %s", calls[s], plural(calls[s], "call", "calls"), s))
	}
	if newCount > 0 {
		parts = append(parts, fmt.Sprintf("%d new to review", newCount))
	}
	if m.changed {
		parts = append(parts, "unsaved changes")
	}
	return strings.Join(parts, " · ")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
