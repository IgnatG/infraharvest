// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package export

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/secret"
	"github.com/IgnatG/infraharvest/internal/report"
)

// The OCSF inventory is the inventory document's resources as events of
// OCSF 1.9.0's class Cloud Resources Inventory Info, in the category
// Discovery, with the iac extension's iac_source profile (see
// schemas/ocsf/iac), which says which configuration manages a resource.
// There is one event for each resource, not one for each scope: the class's
// cloud object holds a single provider, account and region, so a batch
// couldn't span the regions of an account; each resource has its own iac
// source, which a batch would repeat or lose; and a consumer receives
// events of a bounded size, however large the estate is.

// What the events say of OCSF (schemas/ocsf-inventory-v1.schema.json).
const (
	ocsfVersion      = "1.9.0"
	ocsfCategoryUID  = 5
	ocsfClassUID     = 5023
	ocsfActivityID   = 2 // Collect
	ocsfSeverityID   = 1 // Informational
	ocsfTypeUID      = ocsfClassUID*100 + ocsfActivityID
	ocsfProduct      = "infraharvest"
	ocsfExtension    = "iac"
	ocsfExtensionUID = "1301"
	// ocsfExtensionVersion is the version of schemas/ocsf/iac.
	ocsfExtensionVersion = "1.0.0"
	// ocsfProfileIaC is the extension's profile, named as OCSF names the
	// profiles of an extension.
	ocsfProfileIaC = ocsfExtension + "/iac_source"
)

// The tools an iac source names.
const (
	toolTerraform = "terraform"
	toolOpenTofu  = "opentofu"
)

type ocsfProductInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type ocsfExtensionInfo struct {
	Name    string `json:"name"`
	UID     string `json:"uid"`
	Version string `json:"version"`
}

type ocsfMetadata struct {
	Version        string              `json:"version"`
	CorrelationUID string              `json:"correlation_uid"`
	Product        ocsfProductInfo     `json:"product"`
	Profiles       []string            `json:"profiles"`
	Extensions     []ocsfExtensionInfo `json:"extensions"`
}

type ocsfAccount struct {
	UID string `json:"uid"`
}

type ocsfCloud struct {
	Provider string       `json:"provider"`
	Account  *ocsfAccount `json:"account,omitempty"`
	Region   string       `json:"region,omitempty"`
}

type ocsfTag struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// iacSource is the extension's object of the same name: where the
// configuration that manages a resource is.
type iacSource struct {
	Tool          string `json:"tool,omitempty"`
	Address       string `json:"address,omitempty"`
	ModuleAddress string `json:"module_address,omitempty"`
	RepoURL       string `json:"repo_url,omitempty"`
	CommitSHA     string `json:"commit_sha,omitempty"`
	Path          string `json:"path,omitempty"`
	Workspace     string `json:"workspace,omitempty"`
	StateKey      string `json:"state_key,omitempty"`
}

type ocsfResource struct {
	Type   string     `json:"type"`
	UID    string     `json:"uid"`
	Name   string     `json:"name,omitempty"`
	Region string     `json:"region,omitempty"`
	Labels []string   `json:"labels,omitempty"`
	Tags   []ocsfTag  `json:"tags,omitempty"`
	IaC    *iacSource `json:"iac,omitempty"`
}

type ocsfEvent struct {
	ActivityID   int            `json:"activity_id"`
	ActivityName string         `json:"activity_name"`
	CategoryName string         `json:"category_name"`
	CategoryUID  int            `json:"category_uid"`
	ClassName    string         `json:"class_name"`
	ClassUID     int            `json:"class_uid"`
	SeverityID   int            `json:"severity_id"`
	Time         int64          `json:"time"`
	TypeUID      int            `json:"type_uid"`
	Metadata     ocsfMetadata   `json:"metadata"`
	Cloud        ocsfCloud      `json:"cloud"`
	Resources    []ocsfResource `json:"resources"`
}

// ocsfRedaction says what the document leaves out or hashes, as the
// inventory's redaction does, so that a receiver can tell a hashed ID from a
// real one; it also says whether the addresses and paths are left out.
type ocsfRedaction struct {
	TagKeysDropped   []string `json:"tag_keys_dropped"`
	AllTagsDropped   bool     `json:"all_tags_dropped,omitempty"`
	IDsHashed        bool     `json:"ids_hashed"`
	HashAlg          string   `json:"hash_alg"`
	AddressesDropped bool     `json:"addresses_dropped"`
}

type ocsfDoc struct {
	Schema      string        `json:"schema"`
	RunID       string        `json:"run_id"`
	OCSFVersion string        `json:"ocsf_version"`
	Redaction   ocsfRedaction `json:"redaction"`
	Events      []ocsfEvent   `json:"events"`
}

// ocsfProvider names a cloud as OCSF's cloud.provider does.
func ocsfProvider(cloud string) string {
	switch cloud {
	case "aws":
		return "AWS"
	case "azure":
		return "Azure"
	}
	return "GCP"
}

// toolOf names the tool an import ran as an iac source does, or "" for one
// it doesn't know.
func toolOf(engine report.Component) string {
	switch strings.ToLower(engine.Name) {
	case "terraform":
		return toolTerraform
	case "tofu", "opentofu":
		return toolOpenTofu
	}
	return ""
}

// moduleAddressOf returns the address of the module call that a resource
// is in, with the keys of its calls, such as module.a["x"].module.b for
// module.a["x"].module.b.aws_s3_bucket.logs, or "" for a resource of the
// root module.
func moduleAddressOf(address string) string {
	names := hclfile.AddressNames(address)
	if len(names) < 4 || names[0] != "module" {
		return ""
	}
	// The resource is the last two names, its type and its name with any
	// key: cut at the dot before the type, which is the (len-2)-th dot
	// outside brackets.
	want := len(names) - 2
	depth, quoted, escaped, dots := 0, false, false, 0
	for i, r := range address {
		switch {
		case quoted:
			switch {
			case escaped:
				escaped = false
			case r == '\\':
				escaped = true
			case r == '"':
				quoted = false
			}
		case r == '"' && depth > 0:
			quoted = true
		case r == '[':
			depth++
		case r == ']' && depth > 0:
			depth--
		case r == '.' && depth == 0:
			dots++
			if dots == want {
				return address[:i]
			}
		}
	}
	return ""
}

// shaRE is a commit's ID: 40 hexadecimal digits, or 64 for a repository
// that uses SHA-256.
var shaRE = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

// checkRepo refuses a repository URL or a commit that can't be right, and
// either for a document that doesn't have them. The URL may not hold
// credentials, a query or a fragment, which would give away more than
// where the code is.
func checkRepo(opts Options) error {
	if opts.RepoURL == "" && opts.CommitSHA == "" {
		return nil
	}
	if opts.Document != DocOCSFInventory {
		return fmt.Errorf("a repository URL and a commit go into the %s document only, not the %s document", DocOCSFInventory, opts.Document)
	}
	if opts.CommitSHA != "" && !shaRE.MatchString(opts.CommitSHA) {
		return fmt.Errorf("the commit %q must be 40 (or 64) lowercase hexadecimal digits", opts.CommitSHA)
	}
	if opts.RepoURL == "" {
		return nil
	}
	u, err := url.Parse(opts.RepoURL)
	switch {
	case err != nil, u.Host == "", u.Scheme != "https" && u.Scheme != "http":
		return fmt.Errorf("the repository URL %q must be an http or https URL, such as https://github.com/owner/repo", opts.RepoURL)
	case u.User != nil, u.RawQuery != "", u.Fragment != "", strings.ContainsAny(opts.RepoURL, " \t\r\n"):
		return errors.New("the repository URL may not have credentials, a query, a fragment or spaces")
	}
	for _, part := range append([]string{opts.RepoURL}, strings.Split(u.Path, "/")...) {
		if secret.CredentialFormat(part) != "" {
			return errors.New("the repository URL looks like a credential: nothing is written")
		}
	}
	return nil
}

// repoURL is the repository URL as the events say it, without a trailing
// slash or .git.
func repoURL(s string) string {
	return strings.TrimSuffix(strings.TrimRight(s, "/"), ".git")
}

// buildOCSFInventory builds the OCSF inventory of the resources the
// inventory document lists, in the same order, with the redaction of the
// inventory: tags that look like secrets are dropped, with --drop-tag and
// --drop-tags the ones named, --hash-ids hashes IDs, names, and addresses
// and paths too, and --drop-addresses leaves addresses and paths out.
func buildOCSFInventory(r *report.Report, opts Options, runID string, now time.Time) (*ocsfDoc, error) {
	listed, err := readInventories(opts.Out)
	if err != nil {
		return nil, err
	}
	red := newRedactor(opts.DropTags, opts.DropAllTags, opts.HashKey)
	list := entries(r, listed)
	scopes := make([]string, 0, len(list))
	for _, e := range list {
		if e.scope != "" {
			scopes = append(scopes, e.scope)
		}
	}
	for _, s := range r.Scopes {
		scopes = append(scopes, s.Scope)
	}
	cloud, err := cloudOf(r, scopes)
	if err != nil {
		return nil, err
	}
	meta := ocsfMetadata{
		Version:        ocsfVersion,
		CorrelationUID: runID,
		Product:        ocsfProductInfo{Name: ocsfProduct, Version: toComponent(r.Tool).Version},
		Profiles:       []string{"cloud", ocsfProfileIaC},
		Extensions:     []ocsfExtensionInfo{{Name: ocsfExtension, UID: ocsfExtensionUID, Version: ocsfExtensionVersion}},
	}
	doc := &ocsfDoc{Schema: schemaOCSFInventory, RunID: runID, OCSFVersion: ocsfVersion, Events: []ocsfEvent{}}
	tool := toolOf(r.Engine)
	for _, e := range list {
		if secret.CredentialFormat(e.id) != "" {
			return nil, fmt.Errorf("a %s has an ID that looks like a credential: nothing is written", e.typ)
		}
		account, region := scopeParts(e.scope)
		status, basis := report.StatusOf(e.owner)
		res := ocsfResource{
			Type:   e.typ,
			UID:    red.hash(e.id),
			Region: region,
			Labels: []string{"status:" + status, "status_basis:" + basis},
			Tags:   ocsfTags(red.tags(e.tags, e.id, e.name)),
		}
		if e.name != "" && secret.CredentialFormat(e.name) == "" {
			res.Name = red.hash(e.name)
		}
		if status != report.StatusUnmanaged || e.owner == report.OwnerCloud {
			res.Labels = append(res.Labels, "owner:"+e.owner)
		}
		if res.IaC, err = iacOf(e, tool, opts, red); err != nil {
			return nil, err
		}
		event := ocsfEvent{
			ActivityID: ocsfActivityID, ActivityName: "Collect",
			CategoryName: "Discovery", CategoryUID: ocsfCategoryUID,
			ClassName: "Cloud Resources Inventory Info", ClassUID: ocsfClassUID,
			SeverityID: ocsfSeverityID, Time: now.UnixMilli(), TypeUID: ocsfTypeUID,
			Metadata:  meta,
			Cloud:     ocsfCloud{Provider: ocsfProvider(cloud), Region: region},
			Resources: []ocsfResource{res},
		}
		if account != "" {
			event.Cloud.Account = &ocsfAccount{UID: account}
		}
		doc.Events = append(doc.Events, event)
	}
	// Set once every event is built: the redactor lists the tag keys it
	// dropped as it goes through them.
	doc.Redaction = ocsfRedaction{
		TagKeysDropped: red.droppedKeys(), AllTagsDropped: opts.DropAllTags,
		IDsHashed: opts.HashKey != nil, HashAlg: hashAlg, AddressesDropped: opts.DropAddresses,
	}
	return doc, nil
}

// iacOf returns the iac source of a resource the import took over: the
// tool, its address, the module call it is in and the path of its root,
// and the repository and the commit, if the caller named them. It is nil
// for any other resource, which has no address in the configuration this
// import wrote, whoever manages it.
func iacOf(e *entry, tool string, opts Options, red *redactor) (*iacSource, error) {
	var src iacSource
	if e.address != "" {
		src.Tool = tool
		if !opts.DropAddresses {
			// The address and the path are as sensitive as an ID: a key
			// of a for_each can be a name.
			for _, s := range []string{e.address, e.dir} {
				if secret.CredentialFormat(s) != "" {
					return nil, fmt.Errorf("the address of a %s has a format of a credential: nothing is written", e.typ)
				}
			}
			src.Address = red.hash(e.address)
			if m := moduleAddressOf(e.address); m != "" {
				src.ModuleAddress = red.hash(m)
			}
			src.Path = red.hash(e.dir)
		}
		src.RepoURL, src.CommitSHA = repoURL(opts.RepoURL), opts.CommitSHA
	}
	if src == (iacSource{}) {
		return nil, nil
	}
	return &src, nil
}

// ocsfTags returns tags as OCSF's key-value objects, sorted by name; nil
// for none.
func ocsfTags(tags map[string]string) []ocsfTag {
	if len(tags) == 0 {
		return nil
	}
	out := make([]ocsfTag, 0, len(tags))
	for k, v := range tags {
		out = append(out, ocsfTag{Name: k, Value: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
