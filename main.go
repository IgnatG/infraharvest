// Copyright 2018 The Terraformer Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"bytes"
	"io"
	"log"
	"os"
	"regexp"

	"github.com/IgnatG/infraharvest/cmd"
)

// logWriter writes log messages to w (stderr), so stdout carries only
// results (--output json), and hides trace and debug messages that client
// libraries log through the standard logger, some of them whole HTTP
// requests at [DEBUG]. A message is hidden only when it starts with the
// level, so messages that mention one, such as a resource named
// "[DEBUG] logs", are kept.
type logWriter struct {
	w io.Writer
}

// logPrefix matches the date and time the standard logger writes before each
// message (log.LstdFlags, with or without microseconds).
var logPrefix = regexp.MustCompile(`^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}(\.\d+)? `)

func (l logWriter) Write(p []byte) (n int, err error) {
	message := p[len(logPrefix.Find(p)):]
	if bytes.HasPrefix(message, []byte("[TRACE]")) || bytes.HasPrefix(message, []byte("[DEBUG]")) {
		return len(p), nil
	}
	return l.w.Write(p)
}

func main() {
	log.SetOutput(logWriter{w: os.Stderr})
	if err := cmd.Execute(); err != nil {
		log.Println(err)
		os.Exit(cmd.ExitCode(err))
	}
}
