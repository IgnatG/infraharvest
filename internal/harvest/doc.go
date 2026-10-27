// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package harvest is the API between the providers and the importer: a
// Provider maps each of its services to a Lister, which lists the
// service's resources and records each as a Resource. What sets a provider
// apart from the others, such as its command's name, how it applies tags
// and which file of a root a resource goes in, is data in its Profile; what
// only some providers do is an optional ProviderWith* interface. Listers
// only read the cloud.
package harvest
