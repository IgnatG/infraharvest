// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/infraharvest/infraharvest-cli/internal/filelock"
)

// outputLockPath is the file in the output directory out whose lock a run
// holds while it writes there (see lockOutput).
func outputLockPath(out string) string {
	return filepath.Join(out, CheckpointDir, "lock")
}

// lockOutput takes the lock of the output directory out, so that two runs,
// such as two terminals or two MCP tool calls, never write the same roots,
// checkpoints and report at once. It fails, naming the run that holds it,
// when another run does. The system releases the lock of a run that is
// killed, so the next one takes it over. With force, a lock that is taken,
// or that the file system can't take, is logged and the run goes on
// without it, returning a nil lock.
func lockOutput(out string, force bool) (*filelock.Lock, error) {
	if err := os.MkdirAll(filepath.Join(out, CheckpointDir), 0o755); err != nil {
		return nil, err
	}
	path := outputLockPath(out)
	lock, err := filelock.TryLock(path)
	if err == nil {
		if err := lock.Record(); err != nil {
			log.Printf("%s: couldn't record this run as the lock's holder: %v", path, err)
		}
		return lock, nil
	}
	holder := "another infraharvest run"
	if h, ok := filelock.ReadHolder(path); ok {
		holder += " (" + h.String() + ")"
	}
	if errors.Is(err, filelock.ErrLocked) {
		if force {
			log.Printf("warning: --force-unlock: writing to %s although %s is writing there too", out, holder)
			return nil, nil
		}
		return nil, fmt.Errorf("%s is in use by %s: wait for it to finish, or pass --force-unlock if it is stuck", out, holder)
	}
	if force {
		log.Printf("warning: --force-unlock: writing to %s without its lock: %v", out, err)
		return nil, nil
	}
	return nil, fmt.Errorf("lock %s: %w (--force-unlock writes there without the lock)", out, err)
}

// lockOut takes the lock of options.Out for r's command (see lockOutput),
// unless it holds it already: the command's Import calls, in every account
// and region, write the same output directory. The run that finishes the
// command releases it (see unlockOut).
func (r *Run) lockOut(options Options) error {
	if options.Out == "" {
		return nil
	}
	s := r.shared
	s.outMu.Lock()
	defer s.outMu.Unlock()
	if s.outLocked {
		return nil
	}
	lock, err := lockOutput(options.Out, options.ForceUnlock)
	if err != nil {
		return err
	}
	s.outLock, s.outLocked = lock, true
	return nil
}

// unlockOut releases the lock lockOut took, if any.
func (r *Run) unlockOut() {
	s := r.shared
	s.outMu.Lock()
	defer s.outMu.Unlock()
	if s.outLock != nil {
		if err := s.outLock.Unlock(); err != nil {
			log.Printf("couldn't release the lock of the output directory: %v", err)
		}
	}
	s.outLock, s.outLocked = nil, false
}
