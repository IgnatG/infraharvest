// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package filelock takes exclusive locks on files that hold across
// processes, with the operating system's own locks: flock on Unix,
// LockFileEx on Windows. The system releases a lock when the process that
// holds it ends, however it ends, so a run that is killed never leaves one
// behind for the next to break.
package filelock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

// ErrLocked is the error of TryLock for a file whose lock another holder
// has, in this process or another.
var ErrLocked = errors.New("locked by another process")

// Lock is a lock this process holds on a file.
type Lock struct {
	f *os.File
}

// TryLock opens the file at path, creating it if need be, and takes its
// lock without waiting. It fails with ErrLocked when another holder has
// the lock: another process, or another TryLock of the same file in this
// one.
func TryLock(path string) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	if err := lock(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	return &Lock{f: f}, nil
}

// pollInterval is how long Wait waits before it tries a taken lock again.
var pollInterval = 100 * time.Millisecond

// Wait takes the lock on the file at path as TryLock does, trying again
// while another holder has it, until it is free or ctx is done. busy, if
// not nil, is called once, when the lock is first found taken, such as to
// log what the caller is waiting for.
func Wait(ctx context.Context, path string, busy func()) (*Lock, error) {
	for {
		l, err := TryLock(path)
		if !errors.Is(err, ErrLocked) {
			return l, err
		}
		if busy != nil {
			busy()
			busy = nil
		}
		timer := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// Unlock clears the holder Record wrote, releases the lock and closes the
// file. The file stays: removing it could let two processes lock two
// different files of the same name.
func (l *Lock) Unlock() error {
	truncateErr := l.f.Truncate(0)
	return errors.Join(truncateErr, unlock(l.f), l.f.Close())
}

// Holder is the process that holds a lock, as it records itself in the
// locked file (see Lock.Record).
type Holder struct {
	PID     int       `json:"pid"`
	Host    string    `json:"host"`
	Started time.Time `json:"started"`
}

// String names the holder, as an error message about the lock does.
func (h Holder) String() string {
	return fmt.Sprintf("process %d on %s, since %s", h.PID, h.Host, h.Started.Format(time.RFC3339))
}

// Record writes this process into the locked file as the lock's holder,
// so that ReadHolder can name it to a process that finds the lock taken.
func (l *Lock) Record() error {
	host, err := os.Hostname()
	if err != nil {
		host = "an unknown host"
	}
	content, err := json.Marshal(Holder{PID: os.Getpid(), Host: host, Started: time.Now().UTC().Truncate(time.Second)})
	if err != nil {
		return err
	}
	if err := l.f.Truncate(0); err != nil {
		return err
	}
	_, err = l.f.WriteAt(append(content, '\n'), 0)
	return err
}

// ReadHolder returns the holder recorded in the file at path (see
// Lock.Record), and false if none is, as when the lock is free or its
// holder hasn't recorded itself yet.
func ReadHolder(path string) (Holder, bool) {
	content, err := os.ReadFile(path)
	if err != nil {
		return Holder{}, false
	}
	var h Holder
	if err := json.Unmarshal(content, &h); err != nil || h.PID == 0 {
		return Holder{}, false
	}
	return h, true
}
