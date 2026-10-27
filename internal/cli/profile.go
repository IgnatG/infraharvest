// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"runtime/pprof"
)

// Environment variables that profile a run, for go tool pprof: the CPU
// profile of the whole run, the heap profile at its end, and the goroutines
// that leaked by then (blocked on what nothing else can reach any more).
const (
	cpuProfileEnv           = "INFRAHARVEST_CPU_PROFILE"
	memProfileEnv           = "INFRAHARVEST_MEM_PROFILE"
	goroutineLeakProfileEnv = "INFRAHARVEST_GOROUTINELEAK_PROFILE"
)

// startProfiles starts the profiles getenv asks for, and returns the
// function that stops and writes them.
func startProfiles(getenv func(string) string) (stop func() error, err error) {
	var cpu *os.File
	if path := getenv(cpuProfileEnv); path != "" {
		if cpu, err = os.Create(path); err != nil {
			return nil, fmt.Errorf("%s: %w", cpuProfileEnv, err)
		}
		if err := pprof.StartCPUProfile(cpu); err != nil {
			_ = cpu.Close()
			return nil, fmt.Errorf("%s: %w", cpuProfileEnv, err)
		}
	}
	memPath := getenv(memProfileEnv)
	leakPath := getenv(goroutineLeakProfileEnv)
	return func() error {
		var errs []error
		if cpu != nil {
			pprof.StopCPUProfile()
			errs = append(errs, cpu.Close())
		}
		if memPath != "" {
			errs = append(errs, writeProfile(memProfileEnv, memPath, func(w io.Writer) error {
				// Up-to-date statistics: the heap as the run leaves it.
				runtime.GC()
				return pprof.WriteHeapProfile(w)
			}))
		}
		if leakPath != "" {
			errs = append(errs, writeProfile(goroutineLeakProfileEnv, leakPath, func(w io.Writer) error {
				// Runs the garbage collection that finds the leaks first.
				return pprof.Lookup("goroutineleak").WriteTo(w, 0)
			}))
		}
		return errors.Join(errs...)
	}, nil
}

// writeProfile writes a profile with write to the file at path, which env
// named.
func writeProfile(env, path string, write func(io.Writer) error) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("%s: %w", env, err)
	}
	if err := write(f); err != nil {
		_ = f.Close()
		return fmt.Errorf("%s: %w", env, err)
	}
	return f.Close()
}
