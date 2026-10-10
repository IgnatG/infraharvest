// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cmd

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"runtime/pprof"
)

// Environment variables that profile a run, for go tool pprof: the CPU
// profile of the whole run, and the heap profile at its end.
const (
	CPUProfileEnv = "INFRAHARVEST_CPU_PROFILE"
	MemProfileEnv = "INFRAHARVEST_MEM_PROFILE"
)

// startProfiles starts the profiles getenv asks for, and returns the
// function that stops and writes them.
func startProfiles(getenv func(string) string) (stop func() error, err error) {
	var cpu *os.File
	if path := getenv(CPUProfileEnv); path != "" {
		if cpu, err = os.Create(path); err != nil {
			return nil, fmt.Errorf("%s: %w", CPUProfileEnv, err)
		}
		if err := pprof.StartCPUProfile(cpu); err != nil {
			_ = cpu.Close()
			return nil, fmt.Errorf("%s: %w", CPUProfileEnv, err)
		}
	}
	memPath := getenv(MemProfileEnv)
	return func() error {
		var errs []error
		if cpu != nil {
			pprof.StopCPUProfile()
			errs = append(errs, cpu.Close())
		}
		if memPath != "" {
			errs = append(errs, writeHeapProfile(memPath))
		}
		return errors.Join(errs...)
	}, nil
}

func writeHeapProfile(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("%s: %w", MemProfileEnv, err)
	}
	// Up-to-date statistics: the heap as the run leaves it.
	runtime.GC()
	if err := pprof.WriteHeapProfile(f); err != nil {
		_ = f.Close()
		return fmt.Errorf("%s: %w", MemProfileEnv, err)
	}
	return f.Close()
}
