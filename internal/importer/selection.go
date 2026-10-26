// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/config"
	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/managed"
	"github.com/IgnatG/infraharvest/internal/report"
	"github.com/IgnatG/infraharvest/internal/selection"
)

// DefaultSelectionFile is where discover writes the selection file unless
// --selection says otherwise.
const DefaultSelectionFile = "selection.yaml"

// checkSelectionOptions makes an import say what to import: a selection
// file, --all, or the CloudFormation stacks to take over (AWS --cfn-stack).
// An import without any could bring a whole account under Terraform by
// accident. It also refuses --regenerate with
// --incremental: one generates the roots again, the other only adds to
// them.
func checkSelectionOptions(options Options) error {
	switch {
	case options.Regenerate && options.Incremental:
		return errors.New("use either --regenerate or --incremental")
	case options.Discover:
		if options.All {
			return errors.New("discover lists everything; --all is for import")
		}
		return nil
	case options.Selection != "" && options.All:
		return errors.New("use either --selection or --all")
	case options.Selection == "" && !options.All && len(options.CloudFormationStacks) == 0:
		return selection.ErrNoSelection
	}
	return nil
}

// excludedByDefault returns the listed resources the default selection
// leaves out, by "type lister-ID", with the reason: those the catalogue's
// skip rules match, then those the provider finds, such as the parts of a
// default network. The provider sees each resource's tags, as listed
// already, among its attributes as tags.<key>, the way listers record the
// tags they read themselves, so that a tag can tell what owns a resource
// without another call.
func excludedByDefault(ctx context.Context, provider harvest.Provider, cat *catalogue.Catalogue, listed listing) (map[string]string, error) {
	excluded := map[string]string{}
	var all []harvest.Resource
	for _, resources := range listed.resources {
		for _, r := range resources {
			if reason := cat.Skipped(r.Address.Type, r.Observed.ID, r.Observed.Attributes); reason != "" {
				excluded[r.Address.Type+" "+r.Observed.ID] = reason
			}
			all = append(all, withTagAttributes(r, listed.tagsOf(r)))
		}
	}
	withDefaults, ok := provider.(harvest.ProviderWithSelectionDefaults)
	if !ok {
		return excluded, nil
	}
	found, err := withDefaults.ExcludedByDefault(ctx, all)
	for key, reason := range found {
		if _, ok := excluded[key]; !ok {
			excluded[key] = reason
		}
	}
	return excluded, err
}

// unwantedTypes returns the types an import with the selection file f, nil
// for --all, leaves out whatever is listed: those the catalogue's default
// selection leaves out whole (see catalogue.Catalogue.SkippedWhole), unless
// f may include them (see selection.File.MayInclude).
func unwantedTypes(cat *catalogue.Catalogue, f *selection.File) []string {
	var types []string
	for _, typ := range cat.SkippedWhole() {
		if f == nil || !f.MayInclude(typ) {
			types = append(types, typ)
		}
	}
	return types
}

// unwantedTypesSetter is a provider whose listers can skip the calls that
// list only resources of types the import leaves out (see unwantedTypes),
// such as an AWS IAM user's access keys.
type unwantedTypesSetter interface {
	SetUnwantedTypes(types []string)
}

// withTagAttributes returns r with tags among its attributes, as
// tags.<key>, in a copy: what the lister recorded stays as it is, and a
// tag the lister recorded itself wins over the listed one. Without tags,
// r is returned as it is.
func withTagAttributes(r harvest.Resource, tags map[string]string) harvest.Resource {
	if len(tags) == 0 {
		return r
	}
	attributes := make(map[string]string, len(r.Observed.Attributes)+len(tags))
	for key, value := range tags {
		attributes["tags."+key] = value
	}
	maps.Copy(attributes, r.Observed.Attributes)
	observed := *r.Observed
	observed.Attributes = attributes
	r.Observed = &observed
	return r
}

// selectionFile loads the selection file options.Selection once per run;
// nil for --all. With --include-owned, its rules may include what another
// Terraform state or tool manages.
func (r *Run) selectionFile(options Options) (*selection.File, error) {
	if options.Selection == "" {
		return nil, nil
	}
	if r.selection == nil {
		f, err := selection.Load(options.Selection)
		if err != nil {
			return nil, err
		}
		f.IncludeOwned = options.IncludeOwned
		r.selection = f
	}
	return r.selection, nil
}

// selectResources keeps the listed resources to import and records the
// others as excluded. A resource the selection file lists in scope (see
// discoveryScope) follows it; one it doesn't list follows the file's
// rules, then the provider's defaults, then the file's defaults, except
// that a rule doesn't include what the defaults leave out because another
// Terraform state or tool manages it (see selection.File.ByRuleFor). With no
// file (--all), the provider's defaults decide. It also returns the
// resources it leaves out.
func (r *Run) selectResources(listed listing, defaults map[string]string, f *selection.File, scope string, importID func(harvest.Resource) (string, bool)) (map[string][]harvest.Resource, []engine.External) {
	selected := make(map[string][]harvest.Resource, len(listed.resources))
	var leftOut []engine.External
	for service, resources := range listed.resources {
		for _, res := range resources {
			id, importable := importID(res)
			if !importable {
				// importsByDir leaves it out and counts it.
				selected[service] = append(selected[service], res)
				continue
			}
			typ := res.Address.Type
			tags := listed.tagsOf(res)
			r.report.AddListed(report.Listed{Scope: scope, Type: typ, ID: id, Tags: tags})
			reason, excluded := defaults[typ+" "+res.Observed.ID]
			var d selection.Decision
			switch {
			case listed.outside[typ+" "+res.Observed.ID] != "":
				d = selection.Decision{Reason: listed.outside[typ+" "+res.Observed.ID]}
			case f != nil && f.HasIn(scope, typ, id):
				d = f.DecideIn(scope, typ, id, res.RawName, tags)
			case f != nil:
				ruled, ok := f.ByRuleFor(typ, id, res.RawName, tags, reason)
				switch {
				case ok:
					d = ruled
				case excluded:
					d = selection.Decision{Reason: reason}
				default:
					d = f.DecideIn(scope, typ, id, res.RawName, tags)
				}
			case excluded:
				d = selection.Decision{Reason: reason}
			default:
				d = selection.Decision{Include: true}
			}
			if d.Include {
				selected[service] = append(selected[service], res)
				continue
			}
			r.discovered[typ]++
			r.report.Excluded = append(r.report.Excluded, report.Excluded{Type: typ, ID: id, Reason: d.Reason, Scope: scope})
			leftOut = append(leftOut, engine.External{Type: typ, ID: id, Name: resourceName(res.Provider, res, tags)})
		}
	}
	// In a stable order: listed is a map, and the order names data sources.
	sort.Slice(leftOut, func(i, j int) bool {
		if leftOut[i].Type != leftOut[j].Type {
			return leftOut[i].Type < leftOut[j].Type
		}
		return leftOut[i].ID < leftOut[j].ID
	})
	return selected, leftOut
}

// addDiscovered adds the listed resources Terraform can import to the
// selection file discover writes, in scope (see discoveryScope), included
// unless the provider's defaults exclude them.
func (r *Run) addDiscovered(listed listing, defaults map[string]string, scope string, importID func(harvest.Resource) (string, bool)) {
	for service, resources := range listed.resources {
		for _, res := range resources {
			id, importable := importID(res)
			if !importable {
				continue
			}
			reason := defaults[res.Address.Type+" "+res.Observed.ID]
			r.listed = append(r.listed, selection.Resource{
				Type:    res.Address.Type,
				ID:      id,
				Name:    res.RawName,
				Scope:   scope,
				Service: service,
				Tags:    listed.tagsOf(res),
				Include: reason == "",
				Reason:  reason,
			})
		}
	}
}

// selectionPath is the selection file discover writes.
func (r *Run) selectionPath() string {
	if r.options.Selection == "" {
		return DefaultSelectionFile
	}
	return r.options.Selection
}

// coverKey is how Run.covered keys a service listed in scope.
func coverKey(scope, service string) string {
	return scope + "\n" + service
}

// cover records that discover listed options.Services in scope, and
// whether it listed them with filters (see covers).
func (r *Run) cover(scope string, options Options) {
	for _, service := range options.Services {
		r.covered[coverKey(scope, service)] = true
	}
	r.filtered = r.filtered || len(options.filterArgs()) > 0
}

// covers returns a func that reports whether discover listed everything
// that would list an entry of the selection file, so that one it no longer
// found is gone: its service in its scope, listed without filters, or for
// an entry written before entries named their service, its type in its
// scope. With partial set, as after a failed listing, it covers none: what
// didn't list may be only what failed.
func (r *Run) covers(partial bool) func(selection.Resource) bool {
	if partial || r.filtered {
		return func(selection.Resource) bool { return false }
	}
	types := map[string]bool{}
	for _, l := range r.listed {
		types[coverKey(l.Scope, l.Type)] = true
	}
	return func(e selection.Resource) bool {
		if e.Service != "" {
			return r.covered[coverKey(e.Scope, e.Service)]
		}
		return types[coverKey(e.Scope, e.Type)]
	}
}

// writeSelection writes what discover listed; it keeps the entries of the
// file in what it didn't list (see covers), with partial set, as after a
// failed listing, all of them.
func (r *Run) writeSelection(partial bool) error {
	path := r.selectionPath()
	f, err := selection.Load(path)
	if f != nil {
		f.IncludeOwned = r.options.IncludeOwned
	}
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// Merge lists each resource once; nothing is new in a first file.
		f = &selection.File{Version: selection.Version, Defaults: selection.Defaults{Include: true}}
		f.Merge(r.listed, nil)
		for i := range f.Resources {
			f.Resources[i].New = false
		}
	case err != nil:
		return err
	default:
		// Running discover again: keep the decisions people made, and bring
		// those discover made up to date, where it worked them out again in
		// full: not after a listing that failed in part, such as on tags.
		if !partial {
			f.Refresh = selection.Refresh{State: len(r.options.ManagedState) > 0, Tags: !r.options.IncludeOwned, Defaults: true}
		}
		added, dropped := f.Merge(r.listed, r.covers(partial))
		switch {
		case partial:
			log.Printf("updated %s: %d new resources marked new: true for review; the listing failed in part, so the entries it didn't find stay", path, added)
		case r.filtered:
			log.Printf("updated %s: %d new resources marked new: true for review; listed with filters, so the entries it didn't find stay", path, added)
		default:
			log.Printf("updated %s: %d new resources marked new: true for review, %d no longer found and dropped; entries of what it didn't list stay", path, added, dropped)
		}
	}
	if err := f.Save(path); err != nil {
		return err
	}
	d := r.summary()
	c := d.DiscoveryCount
	log.Printf("listed %d resources into %s: %d not managed, %d excluded by default (%d in a Terraform state, %d by Terraform tags, %d by CloudFormation, %d by other tools, %d by the cloud or other defaults); review it, then import with --selection %s",
		c.Listed, path, c.Included, c.Excluded, c.TerraformState, c.TerraformTag, c.CloudFormation, c.OtherTool, c.Cloud, path)
	if r.options.Format != FormatJSON {
		// JSON has them, by type and scope too.
		for _, line := range tagLines(d) {
			log.Print(line)
		}
	}
	return nil
}

// summary summarises what discover listed, each resource once, as the
// default selection decided it (see report.Discovery).
func (r *Run) summary() *report.Discovery {
	d := report.NewDiscovery(r.selectionPath(), r.options.ReportTags)
	seen := map[string]bool{}
	for _, l := range r.listed {
		k := l.Scope + "\n" + l.Type + " " + l.ID
		if seen[k] {
			continue
		}
		seen[k] = true
		d.Add(l.Scope, l.Type, l.Reason, l.Tags)
	}
	for _, err := range r.failures {
		d.Failures = append(d.Failures, err.Error())
	}
	d.Finish()
	return d
}

// tagLines are the log lines of what discover listed by the value of each
// tag key of --report-tags: how much of it no Terraform state, other tool
// or the cloud manages.
func tagLines(d *report.Discovery) []string {
	var lines, parts []string
	key := ""
	flush := func() {
		if key != "" {
			lines = append(lines, "by "+key+": "+strings.Join(parts, ", "))
		}
	}
	for _, t := range d.Tags {
		if t.Key != key {
			flush()
			key, parts = t.Key, nil
		}
		value := t.Value
		if value == "" {
			value = "(not tagged)"
		}
		parts = append(parts, fmt.Sprintf("%s %d of %d not managed", value, t.Included, t.Listed))
	}
	flush()
	return lines
}

// backendState is the --managed-state source that stands for the state of
// the configured backend.
const backendState = "backend"

// excludeManaged adds to defaults the listed resources Terraform already
// manages, according to the state in --managed-state (see managed.Load),
// so that an import leaves them out unless a selection file says
// otherwise. S3 state is read with --profile unless its source names a
// profile.
func excludeManaged(ctx context.Context, run *Run, options Options, listed map[string][]harvest.Resource, defaults map[string]string, importID func(harvest.Resource) (string, bool)) (map[string]string, error) {
	if len(options.ManagedState) == 0 {
		return defaults, nil
	}
	resolved := make([]string, 0, len(options.ManagedState))
	for _, s := range options.ManagedState {
		if s != backendState {
			resolved = append(resolved, withProfile(s, options.Profile))
			continue
		}
		switch {
		case run.backend != nil && run.backend.S3 != nil:
			s3 := run.backend.S3
			resolved = append(resolved, withProfile(fmt.Sprintf("s3://%s/%s?region=%s", s3.Bucket, s3.KeyPrefix, s3.Region), options.Profile))
		case run.backend != nil && run.backend.GCS != nil:
			resolved = append(resolved, fmt.Sprintf("gs://%s/%s", run.backend.GCS.Bucket, run.backend.GCS.Prefix))
		case run.backend != nil && run.backend.AzureRM != nil:
			az := run.backend.AzureRM
			resolved = append(resolved, fmt.Sprintf("https://%s.blob.core.windows.net/%s/%s", az.StorageAccountName, az.ContainerName, az.KeyPrefix))
		default:
			return nil, errors.New("--managed-state=backend needs an S3, GCS or azurerm backend in the configuration file")
		}
	}
	// Every account and region of the command reads the same state.
	state, err := run.shared.managedState(ctx, resolved, managed.DefaultStores)
	if err != nil {
		return nil, err
	}
	// A root generated again keeps what it has: left out of it, its
	// resource blocks would go, and a plan on its state would destroy
	// what they manage. What the root's own state has is the root's too,
	// at the address the state has it (see shared.previousAddressesOf).
	var held map[engine.External]string
	var own map[string]string
	if (options.Regenerate || options.Resume) && options.Out != "" {
		var dirs []string
		if held, dirs, err = generatedResources(options.Out); err != nil {
			return nil, err
		}
		own = ownStates(options.Out, dirs, run.backend)
	}
	// What the state of an environment of a root environments share has
	// is that root's: its stack merges again from the environments' roots
	// generated with it (see adoptedCheckpoint). With --incremental alone,
	// only where no merge checkpoint keeps those roots: the import adds to
	// the kept ones otherwise, which have it.
	var mergedOwn map[string]mergedState
	if (options.Regenerate || options.Resume || options.Incremental) && options.Out != "" {
		if mergedOwn, err = mergedOwnStates(options.Out, !options.Regenerate && !options.Resume); err != nil {
			return nil, err
		}
	}
	if defaults == nil {
		defaults = map[string]string{}
	}
	for _, resources := range listed {
		for _, res := range resources {
			id, _ := importID(res)
			found, ok := state.Find(res.Address.Type, id, res.Observed.ID)
			if !ok {
				continue
			}
			if dir, ok := own[stateLocation(found.Where)]; ok {
				// An address of another type would move it to a resource
				// of another type, which a moved block can't.
				if id != "" && found.Address != "" && found.Type == res.Address.Type {
					run.shared.recordStated(dir, engine.Managed{Type: res.Address.Type, ID: id, Address: found.Address})
				}
				continue
			}
			if _, ok := mergedOwn[stateLocation(found.Where)]; ok {
				continue
			}
			if dir, ok := held[engine.External{Type: res.Address.Type, ID: id}]; ok {
				log.Printf("%s %s is in the state %s, and in %s, which an earlier import generated: it stays in that root, which would destroy it if generated again without it", res.Address.Type, id, found.Where, relativePath(options.Out, dir))
				continue
			}
			defaults[res.Address.Type+" "+res.Observed.ID] = managed.Reason + " (" + found.Where + ")"
		}
	}
	return defaults, nil
}

// generatedResources returns the roots earlier imports generated in out
// that still have configuration, and by type and import ID, the root that
// has each of their resources (see previousAddresses), those of roots
// still being generated again too. A root whose checkpoint can't be read
// fails it: what that root has can't be told, and leaving any of it out
// would make a plan on its state destroy it.
func generatedResources(out string) (map[engine.External]string, []string, error) {
	dirs, err := checkpointDirs(out)
	if err != nil {
		return nil, nil, err
	}
	held := map[engine.External]string{}
	var roots []string
	for _, dir := range dirs {
		dir = filepath.Clean(dir)
		names, err := tfFiles(dir)
		if err != nil {
			return nil, nil, err
		}
		if len(names) == 0 {
			continue
		}
		previous, err := previousAddresses(out, dir)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w: with --managed-state, generating roots again needs to know what each has, so that none of it is left out", relativePath(out, dir), err)
		}
		roots = append(roots, dir)
		for _, p := range previous {
			key := engine.External{Type: p.Type, ID: p.ID}
			if _, ok := held[key]; !ok {
				held[key] = dir
			}
		}
	}
	return held, roots, nil
}

// ownStates returns the root of dirs whose own state each state location
// (see stateLocation) is: its local terraform.tfstate, and the state
// backend, the configured backend, if any, keeps for a root with a
// backend.tf. The states of other workspaces aren't the root's; those of
// the environments of a root environments share are that root's (see
// mergedOwnStates).
func ownStates(out string, dirs []string, backend *config.Backend) map[string]string {
	own := map[string]string{}
	for _, dir := range dirs {
		own[stateLocation(filepath.Join(dir, localStateFile))] = dir
		if backend == nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, BackendFileName)); err == nil {
			if location := backend.StateURL(relativePath(out, dir)); location != "" {
				own[location] = dir
			}
		}
	}
	return own
}

// localStateFile is the state of a root applied without a backend.
const localStateFile = "terraform.tfstate"

// stateLocation returns where, a state managed.Load read, as ownStates
// names roots' states: a URL as it is, and a file by its absolute path.
func stateLocation(where string) string {
	if strings.Contains(where, "://") {
		return where
	}
	if abs, err := filepath.Abs(where); err == nil {
		return abs
	}
	return filepath.Clean(where)
}

// withProfile adds profile, the import's --profile, to an s3:// source that
// names none, which would otherwise read with the default credential chain:
// the profile the import was told to use. Other sources stay as they are.
func withProfile(source, profile string) string {
	if !strings.HasPrefix(source, "s3://") || profile == "" || profile == "default" {
		return source
	}
	u, err := url.Parse(source)
	if err != nil {
		return source // managed.Load reports it.
	}
	query := u.Query()
	if query.Get("profile") != "" {
		return source
	}
	query.Set("profile", profile)
	u.RawQuery = query.Encode()
	return u.String()
}

// discoveryScope names where provider lists, as the roots are laid out:
// the provider's directory (see providerDir), account and region
// (aws/123456789012/eu-west-2), or "" if the provider can't say.
func discoveryScope(ctx context.Context, provider harvest.Provider) string {
	withScope, ok := provider.(harvest.ProviderWithScope)
	if !ok {
		return ""
	}
	account, region, err := withScope.Scope(ctx)
	if err != nil {
		log.Printf("%s: the selection file won't say which account and region the resources are in: %v", provider.Name(), err)
		return ""
	}
	return providerDir(provider) + "/" + account + "/" + region
}
