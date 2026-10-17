// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"errors"
	"fmt"
	"runtime"

	"golang.org/x/sys/windows"
)

// makePrivate makes dir private to the user running infraharvest: its
// access list grants full control to that user, SYSTEM and the
// Administrators group only, and inherits nothing from the parent, so
// that whatever the output folder lets others read, they can't read what
// is written into dir. Files created in dir inherit the same access. It
// returns the function that gives dir its access list back, inherited
// entries and all, and passes it on to what dir holds.
func makePrivate(dir string) (restore func() error, err error) {
	sd, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return nil, fmt.Errorf("read the access list of %s: %w", dir, err)
	}
	// A directory without a security descriptor or access list lets
	// everyone in; restore gives it the same.
	var dacl *windows.ACL
	protection := windows.SECURITY_INFORMATION(windows.UNPROTECTED_DACL_SECURITY_INFORMATION)
	if sd != nil {
		if dacl, _, err = sd.DACL(); err != nil && !errors.Is(err, windows.ERROR_OBJECT_NOT_FOUND) {
			return nil, fmt.Errorf("read the access list of %s: %w", dir, err)
		}
		var control windows.SECURITY_DESCRIPTOR_CONTROL
		if control, _, err = sd.Control(); err != nil {
			return nil, fmt.Errorf("read the access list of %s: %w", dir, err)
		}
		if control&windows.SE_DACL_PROTECTED != 0 {
			protection = windows.PROTECTED_DACL_SECURITY_INFORMATION
		}
	}

	private, err := privateACL()
	if err != nil {
		return nil, err
	}
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, private, nil); err != nil {
		return nil, fmt.Errorf("make %s private: %w", dir, err)
	}
	return func() error {
		// With the parent's entries again, if dir had them: Windows
		// replaces the inherited entries of dacl with the parent's.
		err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|protection, nil, nil, dacl, nil)
		runtime.KeepAlive(sd)
		if err != nil {
			return fmt.Errorf("give %s its access list back: %w", dir, err)
		}
		return nil
	}, nil
}

// privateACL returns an access list that grants full control to the
// current user, SYSTEM and the Administrators group, inherited by what the
// directory holds.
func privateACL() (*windows.ACL, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("read the current user: %w", err)
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return nil, err
	}
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return nil, err
	}
	trustees := []struct {
		sid  *windows.SID
		kind windows.TRUSTEE_TYPE
	}{
		{user.User.Sid, windows.TRUSTEE_IS_USER},
		{system, windows.TRUSTEE_IS_WELL_KNOWN_GROUP},
		{admins, windows.TRUSTEE_IS_WELL_KNOWN_GROUP},
	}
	entries := make([]windows.EXPLICIT_ACCESS, 0, len(trustees))
	for _, t := range trustees {
		entries = append(entries, windows.EXPLICIT_ACCESS{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.SET_ACCESS,
			Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  t.kind,
				TrusteeValue: windows.TrusteeValueFromSID(t.sid),
			},
		})
	}
	acl, err := windows.ACLFromEntries(entries, nil)
	// The entries refer to the SIDs by address only.
	runtime.KeepAlive(trustees)
	return acl, err
}
