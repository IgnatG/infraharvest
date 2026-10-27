// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package fsutil

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// While private, the directory and what is written into it grant access
// to the current user, SYSTEM and Administrators only, inheriting nothing;
// restore gives the directory its inherited access back.
func TestMakePrivateSetsAProtectedAccessList(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "aws", "sqs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if protected, _ := accessList(t, dir); protected {
		t.Fatal("a new directory's access list is protected already")
	}

	restore, err := MakePrivate(dir)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "generated.tf")
	if err := os.WriteFile(file, []byte("# secrets\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		t.Fatal(err)
	}
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{user.User.Sid.String(): true, system.String(): true, admins.String(): true}
	protected, sids := accessList(t, dir)
	if !protected {
		t.Error("the private directory inherits its parent's access")
	}
	checkGrants(t, dir, sids, want)
	_, sids = accessList(t, file)
	checkGrants(t, file, sids, want)

	if err := restore(); err != nil {
		t.Fatal(err)
	}
	if protected, _ := accessList(t, dir); protected {
		t.Error("restore left the directory's access list protected")
	}
}

// checkGrants checks that the access list of path, with the SIDs sids,
// grants access to each of want and to nothing else.
func checkGrants(t *testing.T, path string, sids []string, want map[string]bool) {
	t.Helper()
	got := map[string]bool{}
	for _, sid := range sids {
		if !want[sid] {
			t.Errorf("%s: access granted to %s", path, sid)
		}
		got[sid] = true
	}
	for sid := range want {
		if !got[sid] {
			t.Errorf("%s: no access for %s", path, sid)
		}
	}
}

// accessList reads the access list of path back: whether it is protected
// from inheritance, and the SID of each entry. Every entry must allow.
func accessList(t *testing.T, path string) (protected bool, sids []string) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := sd.Control()
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if dacl == nil {
		t.Fatalf("%s: a null access list lets everyone in", path)
	}
	for i := range uint32(dacl.AceCount) {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			t.Fatal(err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			t.Errorf("%s: entry %d is of type %d, not one that allows", path, i, ace.Header.AceType)
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		sids = append(sids, sid.String())
	}
	return control&windows.SE_DACL_PROTECTED != 0, sids
}

// MkdirPrivate gives a directory it creates the private access list.
func TestMkdirPrivateSetsAProtectedAccessList(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "out", ".infraharvest")
	if err := MkdirPrivate(dir); err != nil {
		t.Fatal(err)
	}
	if protected, _ := accessList(t, dir); !protected {
		t.Error("the private directory inherits its parent's access")
	}
	if protected, _ := accessList(t, filepath.Dir(dir)); protected {
		t.Error("its parent's access list is protected")
	}
}
