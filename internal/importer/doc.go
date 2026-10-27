// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package importer runs what the provider commands, discover, import,
// verify and drift, ask for, with no command line in it: the commands
// fill in Options from their flags and call it, so that what they do is
// tested without one.
//
// CheckOptions rejects what no Import call could do before anything is
// listed. A command's Run collects what each of its scopes did (see
// Run.ImportEach) and finishes with the report, the selection file and the
// exit code. ImportCalls lists every call of a scope before any generates,
// selects what to import through the selection module's Sources, decides
// once what to do with each root, from the run's mode and what earlier
// imports left on disk (see rootlife.go), and has the engine generate,
// add to or vacate it through the call's tools (see call_tools.go), which
// tests replace. Run.MergeEnvironments then merges each stack's
// environments into the root they share.
//
// It reads the cloud and writes files; it never applies, and writes no
// state.
package importer
