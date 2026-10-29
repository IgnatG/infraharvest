// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package importer runs what the provider commands, discover, import,
// verify and drift, ask for, with no command line in it: the commands
// fill in Options from their flags and call it, so that what they do is
// tested without one.
//
// A provider command calls Run.Import once, with its options and its
// plan: the options are checked once, before anything is listed, the plan
// expands them into scopes, each scope's Import calls are all listed
// before any generates, then each stack's environments merge. The Run
// collects what each scope and call did and finishes with the report, the
// selection file and the exit code. Each call selects what to import
// through the selection module's Sources, decides once what to do with
// each root, from the run's mode and what earlier
// imports left on disk (see rootlife.go), and has the engine generate,
// add to or vacate it through the call's tools (see call_tools.go), which
// tests replace.
//
// It reads the cloud and writes files; it never applies, and writes no
// state.
package importer
