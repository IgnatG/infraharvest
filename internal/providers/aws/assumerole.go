// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"fmt"
	"regexp"
)

// AssumeRole is a role the provider assumes before listing, and what it
// is assumed with: an external ID, which a role that a third party
// assumes often requires in its trust policy, and the session's name, as
// CloudTrail shows it, roleSessionName where it is "". The generated
// roots' provider blocks assume it with the same external ID and session
// name, so that their plans can assume it too. Neither is a secret: AWS
// documents the external ID as an identifier, not a password.
type AssumeRole struct {
	ARN, ExternalID, SessionName string
}

// Args returns the Init args of an AWS call in region with profile,
// through the role: the external ID and the session name follow the role
// only where set, so that a call without them has the args, and so the
// inventory (see importer.inventoryPath), it had before they existed.
func (r AssumeRole) Args(region, profile string) []string {
	args := []string{region, profile, r.ARN}
	switch {
	case r.SessionName != "":
		args = append(args, r.ExternalID, r.SessionName)
	case r.ExternalID != "":
		args = append(args, r.ExternalID)
	}
	return args
}

// The limits STS sets on AssumeRole's RoleSessionName and ExternalId.
var (
	sessionNamePattern = regexp.MustCompile(`^[\w+=,.@-]{2,64}$`)
	externalIDPattern  = regexp.MustCompile(`^[\w+=,.@:/-]+$`)
)

// Check returns an error where STS would refuse the external ID or the
// session name, so that a run fails before it lists rather than in every
// region.
func (r AssumeRole) Check() error {
	if r.SessionName != "" && !sessionNamePattern.MatchString(r.SessionName) {
		return fmt.Errorf("--role-session-name %q: 2 to 64 letters, digits and +=,.@_-", r.SessionName)
	}
	if r.ExternalID != "" && (len(r.ExternalID) < 2 || len(r.ExternalID) > 1224 || !externalIDPattern.MatchString(r.ExternalID)) {
		return fmt.Errorf("--external-id %q: 2 to 1224 letters, digits and +=,.@:/_-", r.ExternalID)
	}
	return nil
}
