// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/config"
	"github.com/IgnatG/infraharvest/internal/engine"
)

// failingWriter fails every write.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errStub }

// WriteBootstrap writes the backend's bootstrap root into the bootstrap
// directory of the output directory, with a .gitignore beside it that
// keeps the root's local state out of version control, and says what to do
// with it; it refuses a configuration it can't read or without a backend.
func TestWriteBootstrap(t *testing.T) {
	t.Parallel()
	const s3 = "version: 1\nbackend:\n  s3:\n    bucket: acme-state\n    region: eu-west-2\n"
	for _, tc := range []struct {
		name   string
		config string // "" leaves no configuration file
		// gitignore, if set, is the output directory's .gitignore already.
		gitignore string
		// outIsFile makes the output directory a file.
		outIsFile bool
		failWrite bool
		want      string // in the error; "" wants none
	}{
		{name: "s3", config: s3},
		{name: "gcs", config: "version: 1\nbackend:\n  gcs:\n    bucket: acme-state\n"},
		{name: "keeps the .gitignore there is", config: s3, gitignore: "mine\n"},
		{name: "no configuration file", want: "config.yaml"},
		{name: "configuration that doesn't load", config: "version: 2\n", want: "version 2 isn't supported"},
		{name: "no backend", config: "version: 1\n", want: "has no backend to bootstrap"},
		{name: "output directory is a file", config: s3, outIsFile: true, want: "mkdir "}, // "not a directory", or on Windows "cannot find the path",
		{name: "message can't be written", config: s3, failWrite: true, want: errStub.Error()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			configFile := filepath.Join(dir, "config.yaml")
			if tc.config != "" {
				if err := os.WriteFile(configFile, []byte(tc.config), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			out := filepath.Join(dir, "out")
			switch {
			case tc.outIsFile:
				if err := os.WriteFile(out, nil, 0o644); err != nil {
					t.Fatal(err)
				}
			case tc.gitignore != "":
				if err := os.MkdirAll(out, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(out, ".gitignore"), []byte(tc.gitignore), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			var message strings.Builder
			var w io.Writer = &message
			if tc.failWrite {
				w = failingWriter{}
			}
			err := WriteBootstrap(configFile, out, w, nil)
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("err = %v, want one about %q", err, tc.want)
				}
				if tc.failWrite {
					// The root is written before the message.
					if _, statErr := os.Stat(filepath.Join(out, config.BootstrapDir, "main.tf")); statErr != nil {
						t.Error(statErr)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			bootstrap := filepath.Join(out, config.BootstrapDir)
			for _, name := range []string{"versions.tf", "main.tf", "README.md"} {
				if _, err := os.Stat(filepath.Join(bootstrap, name)); err != nil {
					t.Error(err)
				}
			}
			gitignore, err := os.ReadFile(filepath.Join(out, ".gitignore"))
			if err != nil {
				t.Fatal(err)
			}
			wantIgnore := engine.GitignoreFile
			if tc.gitignore != "" {
				wantIgnore = tc.gitignore
			}
			if string(gitignore) != wantIgnore {
				t.Errorf(".gitignore = %q, want %q", gitignore, wantIgnore)
			}
			if want := "Wrote " + bootstrap + ": apply it once"; !strings.HasPrefix(message.String(), want) {
				t.Errorf("message %q, want it to start with %q", message.String(), want)
			}
		})
	}
}
