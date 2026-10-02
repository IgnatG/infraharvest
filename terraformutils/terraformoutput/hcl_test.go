package terraformoutput

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPrintFileIsNotWritableByOthers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not enforced on Windows")
	}
	path := filepath.Join(t.TempDir(), "provider.tf")

	PrintFile(path, []byte(`provider "aws" {}`))

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o022 != 0 {
		t.Errorf("mode of %s: got %#o, want no group/other write bits", path, perm)
	}
}
