package terraformutils

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWriteSecretFileIsOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "terraform.tfstate")

	if err := WriteSecretFile(path, []byte("secret")); err != nil {
		t.Fatal(err)
	}

	assertContent(t, path, "secret")
	assertNoAccessForOthers(t, path)
}

func TestWriteSecretFileTightensExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "terraform.tfstate")
	if err := os.WriteFile(path, []byte("old state from an earlier run"), 0o777); err != nil {
		t.Fatal(err)
	}

	if err := WriteSecretFile(path, []byte("new")); err != nil {
		t.Fatal(err)
	}

	assertContent(t, path, "new")
	assertNoAccessForOthers(t, path)
}

func TestWriteSecretFileLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()

	if err := WriteSecretFile(filepath.Join(dir, "plan.json"), []byte("{}")); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("want only plan.json in %s, got %v", dir, entries)
	}
}

func TestWriteSecretFileMissingDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "terraform.tfstate")

	if err := WriteSecretFile(path, []byte("x")); err == nil {
		t.Error("want an error when the directory does not exist")
	}
}

func assertContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("content of %s: got %q, want %q", path, got, want)
	}
}

// assertNoAccessForOthers checks that group and other have no permission
// bits. The umask can only remove bits, so this holds for any umask.
func assertNoAccessForOthers(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Log("POSIX permission bits are not enforced on Windows")
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("mode of %s: got %#o, want no group/other bits", path, perm)
	}
}
