// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package secretscan tells secrets from other text, and keeps them out of
// what is written about a cloud estate. It is the part of infraharvest's
// secret handling that tools reading or reporting infrastructure need: the
// detectors that say whether a name or a value is a secret, the filter
// that drops secret and unwanted tags from a resource's tags, and the
// keyed hash that replaces IDs and names (infraharvest export --hash-ids),
// so that the same key gives the same hash in infraharvest and in a tool
// that imports this package.
//
// # Promises
//
// What a function answers for a given input does not change between
// releases without a note in the changelog: a detector may learn a new
// credential format, never forget one. Hashes never change: the same key
// and value give the same hash, so a hash stays valid across versions.
//
// The package imports no other infraharvest package that links the
// engine, Terraform or a cloud SDK, and no module outside the standard
// library. Until infraharvest's first tagged release, everything in
// pkg/ is unstable.
//
// # Licence
//
// Like the rest of infraharvest it is AGPL-3.0-only. A product that is not
// licensed under the AGPL needs infraharvest's commercial licence to
// import it (see the licensing section of the README).
package secretscan

import (
	"github.com/IgnatG/infraharvest/internal/secretdetect"
)

// IsSecretName reports whether key names a secret, such as API_SECRET,
// DB_PASSWORD or clientSecret. A key that names where a secret is kept
// rather than the secret itself, such as SECRET_ARN, ACCESS_KEY_ID or
// TOKEN_URL, doesn't. Words are told apart by case and separators, so
// dbPassword and DB_PASSWORD are both secrets.
func IsSecretName(key string) bool {
	return secretdetect.IsName(key)
}

// CredentialFormat returns the name of the credential format value has,
// such as "GitHub token", "private key" or "password in a URL", or "" if
// it has none of the formats it knows. A credential format is a secret
// wherever it appears, under any key.
func CredentialFormat(value string) string {
	return secretdetect.CredentialFormat(value)
}

// LooksSecret reports whether a tag, or any name and value pair, looks
// like it holds a secret: the key names one (see IsSecretName), the key or
// the value has the format of a credential (see CredentialFormat), or the
// value looks random enough to be a key or a token under a key someone
// chose. It is what infraharvest export uses to drop a tag from an
// inventory.
func LooksSecret(key, value string) bool {
	return secretdetect.IsName(key) || secretdetect.CredentialFormat(key) != "" || secretdetect.Reason(key, value, true) != ""
}
