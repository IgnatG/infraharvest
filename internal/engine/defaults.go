// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"

	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

// maxDefaultRounds bounds how often the defaults pass plans, and
// maxDefaultValidations how often it validates before each plan.
const (
	maxDefaultRounds      = 5
	maxDefaultValidations = 3
)

// defaultCandidate is an argument stripDefaults may leave out.
type defaultCandidate struct {
	address string
	path    []string // nested block types, then the argument
	null    bool     // its value is null: leaving it out is always equal
	// setting is a constant that isn't empty, zero or a value the
	// catalogue names as a default, such as handler = "index.handler":
	// what a provider's check that an argument is set is about.
	setting bool
	// proven says its value is the default the provider gives it, as
	// schema metadata of the root's major version says (see
	// schemaHints.provenDefault): leaving it out plans the same, so on its
	// own it needs no validation. Where a plan differs all the same, it is
	// put back as any other candidate is: the metadata may be of another
	// release, or wrong.
	proven bool
}

// verified reports whether leaving c out is known to change nothing: it
// is null, or a proven default.
func (c defaultCandidate) verified() bool {
	return c.null || c.proven
}

func (c defaultCandidate) key() string {
	return c.address + "\x00" + strings.Join(c.path, ".")
}

// stripDefaults leaves out of generated.tf the arguments Terraform writes
// although they change nothing:
//
//   - arguments set to null, which is the same as leaving them out;
//   - optional, non-computed arguments set to a constant that is the
//     provider's default, whatever the constant: false, 0 and "" as much
//     as 3600, "/" or "Managed by Terraform";
//   - optional arguments set empty, computed or not, but for those
//     the catalogue's keep_empty names, and blocks that set nothing but zero
//     values (see removeIn);
//   - optional arguments, computed or not, set to the value the
//     catalogue's defaults name for their type: what the provider or the
//     cloud gives an argument it isn't told, such as a bucket's STANDARD
//     storage class, by its path in nested blocks
//     (soft_delete_policy.retention_duration_seconds=604800).
//
// Terraform writes every optional argument, so a resource lists dozens of
// them. The providers' schemas don't say what their defaults are, so only
// the plan can tell a default from a setting: the candidates of every
// resource are left out at once, and where a resource then plans
// differently from baseline (see FewerChanges), its arguments behind
// the difference are put back and the rest left out again, until the plan
// stays the same; all of its arguments only where none is behind the
// difference, or in the last round but one. Before each plan, validation,
// which needs no cloud, puts back what leaving out made invalid, such as
// one of two required alternatives; errors of the plan itself, such as a
// provider's own checks, put back the arguments they name, and the next
// time the resource's settings too.
// Other computed arguments set to a constant stay: leaving one out never
// shows in the plan, so it could hide a real setting. Where the plan can't
// settle what to leave out, only nulls and proven defaults go, with no plan
// to check them (see stripNullsOnly): the pass rebases onto a new plan.
func stripDefaults(ctx context.Context, tf tfrun.Terraform, ws *workspace, rules defaultRules) (*tidyEdit, error) {
	dir := ws.dir
	current, err := ws.Load(rootfile.GeneratedFileName)
	if err != nil {
		return nil, err
	}
	// Only constants need the schema, to tell optional from computed, and
	// empty blocks, which only a plan can tell from absent ones.
	var schemas *tfjson.ProviderSchemas
	if len(rules.defaults) > 0 || hasConstants(current.Syntax) || slices.ContainsFunc(current.Syntax.Blocks, func(b *hclsyntax.Block) bool { return hasEmptyBlocks(b.Body) }) {
		if schemas, err = tf.ProvidersSchema(ctx); err != nil {
			return nil, fmt.Errorf("terraform providers schema: %w", err)
		}
	}
	// keep holds the candidates to put back, by key, and removed those the
	// last round left out.
	keep := map[string]bool{}
	var removed []defaultCandidate
	// failed counts the rounds in which the plan failed on each resource.
	failed := map[string]int{}
	return &tidyEdit{
		edit: func(a *tidyAttempt) (edited, error) {
			var err error
			removed, err = removeValid(ctx, tf, ws, a.restore, schemas, rules, keep)
			return edited{other: len(removed) > 0}, err
		},
		review: func(a *tidyAttempt, r tidyReview) (tidyDecision, error) {
			if r.verdict.Failed {
				// Leaving an argument out made the configuration invalid in a
				// way validation didn't see, such as a check the provider makes
				// when it plans: a Lambda function's "handler and runtime must
				// be set when PackageType is Zip". Keep the arguments the
				// errors name and leave the rest of the resource's out again;
				// the next time, or if they name none, its settings too (see
				// defaultCandidate), as such checks come one at a time, as in
				// a metric alarm's; then everything, as in the last round but
				// one.
				assigned, unassigned, err := assignErrors(ws, r.diags)
				if err != nil {
					return tidyDecision{}, err
				}
				if len(unassigned) > 0 || len(assigned) == 0 {
					log.Printf("%s: keeping the arguments that may be defaults, as the plan without them failed: %v", dir, tfrun.DiagnosticsError(r.diags))
					return tidyDecision{outcome: tidyFallBack}, nil
				}
				keepWhatErrorsName(removed, assigned, failed, a.round, keep)
				return defaultsRetry(dir, a), nil
			}
			differs, noise := keepWhatChanged(r.verdict.Changed, removed, r.baseline, a.round, keep)
			if !differs && (noise || r.verdict.Totals) {
				return tidyDecision{outcome: tidyAccept}, nil
			}
			if !differs {
				log.Printf("%s: keeping the arguments that may be defaults, as the plan without them changes %+v, not %+v, and no resource tells why", dir, r.plan.summary, r.baseline.summary)
				return tidyDecision{outcome: tidyFallBack}, nil
			}
			return defaultsRetry(dir, a), nil
		},
		unplanned: func(a *tidyAttempt) (bool, error) {
			return stripNullsOnly(ctx, tf, ws, a.restore, schemas, rules, keep)
		},
	}, nil
}

// defaultsRetry is the outcome of a round of stripDefaults whose plan asks
// for another: it edits again, or falls back after the last round.
func defaultsRetry(dir string, a *tidyAttempt) tidyDecision {
	if a.last() {
		log.Printf("%s: keeping the arguments that may be defaults, as the plan without them still changed after %d plans", dir, a.rounds)
		return tidyDecision{outcome: tidyFallBack}
	}
	return tidyDecision{outcome: tidyRetry}
}

// stripNullsOnly is stripDefaults where the plan can't settle what to
// leave out, which keeps its constants: it leaves out of generated.tf, in
// ws, only nulls, as Terraform treats null as an omitted argument, and
// proven defaults (see defaultCandidate.proven) of the metadata's own
// release where the configuration still validates without them: no plan
// would correct a wrong one. It reports whether it changed generated.tf;
// restore puts it back as it was.
func stripNullsOnly(ctx context.Context, tf tfrun.Terraform, ws *workspace, restore func() error, schemas *tfjson.ProviderSchemas, rules defaultRules, keep map[string]bool) (bool, error) {
	type pass struct {
		schemas *tfjson.ProviderSchemas
		rules   defaultRules
	}
	// Without schemas, only nulls are candidates.
	passes := []pass{{nil, defaultRules{}}}
	if rules.hints.exact {
		verified := rules
		verified.verifiedOnly = true
		passes = append([]pass{{schemas, verified}}, passes...)
	}
	for _, p := range passes {
		if err := restore(); err != nil {
			return false, err
		}
		f, err := ws.Load(rootfile.GeneratedFileName)
		if err != nil {
			return false, err
		}
		removed := removeDefaults(f, p.schemas, p.rules, keep)
		if len(removed) == 0 {
			return false, restore()
		}
		ws.Save(f)
		if !slices.ContainsFunc(removed, func(c defaultCandidate) bool { return c.proven }) {
			return true, nil
		}
		out, err := tf.Validate(ctx)
		if err != nil {
			return false, errors.Join(fmt.Errorf("terraform validate: %w", err), restore())
		}
		if out.Valid {
			return true, nil
		}
	}
	return false, restore()
}

// keepWhatErrorsName puts back, of removed, what the errors of a failed
// plan in round name, by the address of the resource each is about
// (assigned), and counts the round in failed: the arguments they name
// the first time, and the resource's settings too the second time or
// where they name none (see defaultCandidate); every argument of the
// resource from the third time, or in the last round but one.
func keepWhatErrorsName(removed []defaultCandidate, assigned map[string][]tfjson.Diagnostic, failed map[string]int, round int, keep map[string]bool) {
	for address, ds := range assigned {
		failed[address]++
		kept := false
		if failed[address] <= 2 && round < maxDefaultRounds-2 {
			kept = keepNamed(removed, address, ds, keep)
			if !kept || failed[address] == 2 {
				kept = keepSettings(removed, address, keep) || kept
			}
		}
		if !kept {
			keepAll(removed, address, keep)
		}
	}
}

// keepWhatChanged puts back, of removed, the arguments behind each
// resource of changed, those that plan in round a change worse than the
// baseline's (see FewerChanges): all of its constants in the
// last round but one. It reports whether a resource with arguments left
// out differs, and whether one without differs all the same, which is
// noise, as an emulator's plan may differ by itself. Proven defaults
// count as left out: where the plan differs, the metadata may be wrong,
// and the plan corrects it.
func keepWhatChanged(changed []*tfjson.ResourceChange, removed []defaultCandidate, baseline Baseline, round int, keep map[string]bool) (differs, noise bool) {
	for _, rc := range changed {
		if !slices.ContainsFunc(removed, func(c defaultCandidate) bool { return !c.null && c.address == rc.Address }) {
			noise = true
			continue
		}
		differs = true
		if round == maxDefaultRounds-2 {
			// There is one round left: keep the resource's constants
			// rather than plan again.
			keepAll(removed, rc.Address, keep)
			continue
		}
		keepChanged(removed, rc.Address, newlyChanged(tfrun.ChangeSignature(rc), baseline.Planned(rc.Address)), changedPaths(rc), keep)
	}
	return differs, noise
}

// removeValid leaves the candidates not in keep out of generated.tf, as
// restore has it, until the configuration validates: the candidates of a
// resource that validation rejects, those its errors name or else all of
// them, are kept. After maxDefaultValidations, the plan has the last word.
// It returns the candidates it left out.
func removeValid(ctx context.Context, tf tfrun.Terraform, ws *workspace, restore func() error, schemas *tfjson.ProviderSchemas, rules defaultRules, keep map[string]bool) ([]defaultCandidate, error) {
	for validation := 0; ; validation++ {
		if err := restore(); err != nil {
			return nil, err
		}
		generated, err := ws.Load(rootfile.GeneratedFileName)
		if err != nil {
			return nil, err
		}
		removed := removeDefaults(generated, schemas, rules, keep)
		if len(removed) == 0 {
			return nil, nil
		}
		ws.Save(generated)
		// Leaving out nulls never makes configuration invalid, nor do proven
		// defaults, which no constraint names (see schemaHints.provenDefault).
		if validation == maxDefaultValidations || !slices.ContainsFunc(removed, func(c defaultCandidate) bool { return !c.verified() }) {
			return removed, nil
		}
		out, err := tf.Validate(ctx)
		if err != nil {
			return nil, fmt.Errorf("terraform validate: %w", err)
		}
		if out.Valid {
			return removed, nil
		}
		assigned, _, err := assignErrors(ws, tfrun.ErrorDiagnostics(out.Diagnostics))
		if err != nil {
			return nil, err
		}
		if len(assigned) == 0 {
			// Nothing to tell the resources apart by: let the plan say.
			return removed, nil
		}
		for address, ds := range assigned {
			if !keepNamed(removed, address, ds, keep) {
				keepAll(removed, address, keep)
			}
		}
	}
}

// assignErrors assigns diags to the resources of ws they are about (see
// byResource).
func assignErrors(ws *workspace, diags []tfjson.Diagnostic) (map[string][]tfjson.Diagnostic, []tfjson.Diagnostic, error) {
	generated, err := ws.Load(rootfile.GeneratedFileName)
	if err != nil {
		return nil, nil, err
	}
	imports, err := ws.Load(rootfile.ImportsFileName)
	if err != nil {
		return nil, nil, err
	}
	assigned, unassigned := byResource(diags, generated, imports)
	return assigned, unassigned, nil
}

// keepNamed keeps the constant candidates of address that diags name, such
// as the alternatives of "one of `a,b` must be specified", and reports
// whether they name any.
func keepNamed(removed []defaultCandidate, address string, diags []tfjson.Diagnostic, keep map[string]bool) bool {
	var text strings.Builder
	for _, d := range diags {
		text.WriteString(d.Summary + " " + d.Detail + " ")
	}
	named := argumentNames(text.String())
	matched := false
	for _, c := range removed {
		if !c.null && c.address == address && (named[c.path[0]] || named[c.path[len(c.path)-1]]) {
			keep[c.key()] = true
			matched = true
		}
	}
	return matched
}

// keepSettings keeps the candidates of address that are settings (see
// defaultCandidate), and reports whether it found any.
func keepSettings(removed []defaultCandidate, address string, keep map[string]bool) bool {
	found := false
	for _, c := range removed {
		if c.setting && c.address == address {
			keep[c.key()] = true
			found = true
		}
	}
	return found
}

// argumentNames returns the words of text that could name an argument.
func argumentNames(text string) map[string]bool {
	names := map[string]bool{}
	for _, word := range strings.FieldsFunc(text, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_'
	}) {
		names[word] = true
	}
	return names
}

// keepAll keeps every constant candidate of address. Nulls never count:
// Terraform treats null as an omitted argument.
func keepAll(removed []defaultCandidate, address string, keep map[string]bool) {
	for _, c := range removed {
		if !c.null && c.address == address {
			keep[c.key()] = true
		}
	}
}

// keepChanged keeps the constant candidates of address behind changed,
// the newly changed top-level arguments: under each, those on the path of
// an attribute the plan changes (see changedPaths), or all of them if
// none is, so that a cache behaviour whose grpc_config block changes
// keeps that block, not its compress = false too; and all of the
// resource's if none is under changed. Nulls never count: Terraform
// treats null as an omitted argument.
func keepChanged(removed []defaultCandidate, address string, changed, paths []string, keep map[string]bool) {
	matched := false
	for _, top := range changed {
		var under []defaultCandidate
		onPath := false
		for _, c := range removed {
			if c.null || c.address != address || c.path[0] != top {
				continue
			}
			under = append(under, c)
			if onChangedPath(strings.Join(c.path, "."), paths) {
				keep[c.key()] = true
				onPath = true
			}
		}
		if len(under) == 0 {
			continue
		}
		matched = true
		if !onPath {
			for _, c := range under {
				keep[c.key()] = true
			}
		}
	}
	if !matched {
		keepAll(removed, address, keep)
	}
}

// changedPaths returns the paths, without indexes, of the attributes a
// resource's planned change changes, down to nested blocks, such as
// default_cache_behavior.grpc_config.enabled.
func changedPaths(rc *tfjson.ResourceChange) []string {
	var paths []string
	for _, l := range tfrun.ChangedLeaves(rc.Change.Before, rc.Change.After, rc.Change.AfterUnknown) {
		paths = append(paths, tfrun.WithoutIndexes(l.Path))
	}
	return paths
}

// onChangedPath reports whether the argument or block at path is one of
// paths, holds one, or is in one, such as a block whose whole list
// changes.
func onChangedPath(path string, paths []string) bool {
	return slices.ContainsFunc(paths, func(p string) bool {
		return p == path || strings.HasPrefix(p, path+".") || strings.HasPrefix(path, p+".")
	})
}

// newlyChanged returns the attributes a change signature, now, changes
// that the one before didn't.
func newlyChanged(now, before string) []string {
	_, nowAttributes, _ := strings.Cut(now, ":")
	_, beforeAttributes, _ := strings.Cut(before, ":")
	previous := strings.Split(beforeAttributes, ",")
	var changed []string
	for _, a := range strings.Split(nowAttributes, ",") {
		if a != "" && !slices.Contains(previous, a) {
			changed = append(changed, a)
		}
	}
	return changed
}

// removeDefaults removes the candidates not in keep from every resource,
// and returns them. Without schemas, only nulls are candidates.
func removeDefaults(f *hclfile.File, schemas *tfjson.ProviderSchemas, rules defaultRules, keep map[string]bool) []defaultCandidate {
	var removed []defaultCandidate
	for _, r := range f.Resources() {
		typ := AddressType(r.Address)
		removed = append(removed, removeIn(r.Address, typ, nil, r.Syntax.Body, r.Write.Body(), schemas, typeRules{defaults: defaultValues(rules.defaults[typ]), keepEmpty: rules.keepEmpty[typ], tags: rules.tags, hints: rules.hints, verifiedOnly: rules.verifiedOnly}, keep)...)
	}
	return removed
}

// removeIn removes the candidates not in keep from a resource's body, or
// the body of one of its nested blocks at prefix, and returns them:
//
//   - nulls;
//   - the values the catalogue's defaults name, computed or not;
//   - empty values ("", [] or {}) of optional arguments, computed or not,
//     but for those of keep_empty: an empty computed argument, such as a
//     key vault's access_policy = [], only fights the separate resources
//     that hold the setting, and left out it keeps what the cloud has;
//   - other constants of optional arguments the provider doesn't compute,
//     doesn't mark sensitive, but for the tags (see defaultRules);
//   - constants of optional arguments the provider computes that are the
//     default a Plugin Framework resource's metadata names (see
//     schemaHints.frameworkDefault);
//   - nested blocks of optional types that set nothing but zero values,
//     none of an argument the provider computes (see setsComputed),
//     such as access_logs { bucket = "", enabled = false }, as a whole,
//     and the blocks left empty once their arguments are out.
//
// A constant exact schema metadata says isn't the argument's default is
// never a candidate (see schemaHints.provenSetting), and one it says is,
// is a proven one (see defaultCandidate.proven). With verifiedOnly, only
// nulls and proven defaults are.
func removeIn(address, typ string, prefix []string, syntax *hclsyntax.Body, write *hclwrite.Body, schemas *tfjson.ProviderSchemas, rules typeRules, keep map[string]bool) []defaultCandidate {
	var removed []defaultCandidate
	for name, attr := range syntax.Attributes {
		path := append(append([]string(nil), prefix...), name)
		c := defaultCandidate{address: address, path: path}
		switch {
		case hclfile.IsNullLiteral(attr.Expr):
			c.null = true
		case rules.hints.provenSetting(typ, path, attr.Expr):
			// Left out, it would plan as the default.
			continue
		case isNamedDefault(rules.defaults, path, attr.Expr):
			s := tfrun.SchemaAttribute(schemas, typ, path)
			if s == nil || !s.Optional || s.Required {
				continue
			}
		case isEmptyValue(attr.Expr):
			s := tfrun.SchemaAttribute(schemas, typ, path)
			if s == nil || !s.Optional || s.Required || slices.Contains(rules.keepEmpty, strings.Join(path, ".")) {
				continue
			}
		case isConstant(attr.Expr):
			s := tfrun.SchemaAttribute(schemas, typ, path)
			// A sensitive value, such as a parameter's, is what the cloud has,
			// never a default; nor are the tags (see defaultRules).
			if s == nil || !s.Optional || s.Required || s.Sensitive || (rules.tags != "" && strings.Join(path, ".") == rules.tags) {
				continue
			}
			if s.Computed && !rules.hints.frameworkDefault(typ, path, attr.Expr) {
				continue
			}
			c.setting = !isZeroConstant(attr.Expr)
		default:
			continue
		}
		if !c.null && rules.hints.provenDefault(typ, path, attr.Expr) {
			// The Plugin SDK doesn't let a computed argument have a
			// default, so the schema agreeing says the metadata is of
			// this argument.
			if s := tfrun.SchemaAttribute(schemas, typ, path); s != nil && s.Optional && !s.Computed && !s.Required {
				c.proven, c.setting = true, false
			}
		}
		if keep[c.key()] || rules.verifiedOnly && !c.verified() {
			continue
		}
		write.RemoveAttribute(name)
		removed = append(removed, c)
	}
	blocks := write.Blocks()
	for i, b := range syntax.Blocks {
		if i >= len(blocks) {
			continue
		}
		blockPath := append(append([]string(nil), prefix...), b.Type)
		c := defaultCandidate{address: address, path: blockPath}
		if !rules.verifiedOnly && !keep[c.key()] && len(b.Body.Attributes)+len(b.Body.Blocks) > 0 && unsetBody(b.Body) && optionalBlock(schemas, typ, blockPath) && !setsComputed(schemas, typ, blockPath, b.Body) {
			write.RemoveBlock(blocks[i])
			removed = append(removed, c)
			continue
		}
		removed = append(removed, removeIn(address, typ, blockPath, b.Body, blocks[i].Body(), schemas, rules, keep)...)
		// A block left with nothing set, such as ttl {}, only says it is
		// there: it goes too, where the plan says that's the same.
		if !rules.verifiedOnly && schemas != nil && isEmpty(blocks[i].Body()) && !keep[c.key()] {
			write.RemoveBlock(blocks[i])
			removed = append(removed, c)
		}
	}
	return removed
}

// optionalBlock reports whether the schemas describe the nested block at
// path of typ as one a resource may leave out.
func optionalBlock(schemas *tfjson.ProviderSchemas, typ string, path []string) bool {
	if schemas == nil {
		return false
	}
	for _, provider := range schemas.Schemas {
		resource, ok := provider.ResourceSchemas[typ]
		if !ok || resource.Block == nil {
			continue
		}
		block := resource.Block
		var nested *tfjson.SchemaBlockType
		for _, name := range path {
			if block == nil {
				return false
			}
			if nested, ok = block.NestedBlocks[name]; !ok {
				return false
			}
			block = nested.Block
		}
		return nested != nil && nested.MinItems == 0
	}
	return false
}

// isEmptyValue reports whether expr is "", [] or {}.
func isEmptyValue(expr hclsyntax.Expression) bool {
	switch e := expr.(type) {
	case *hclsyntax.TemplateExpr:
		if len(e.Parts) == 0 {
			return true
		}
		s, ok := hclfile.LiteralString(e)
		return ok && s == ""
	case *hclsyntax.TupleConsExpr:
		return len(e.Exprs) == 0
	case *hclsyntax.ObjectConsExpr:
		return len(e.Items) == 0
	}
	return false
}

// defaultRules are what the catalogue tells stripDefaults, by type: the
// values arguments default to, as path=value (see Options.Defaults), and
// the arguments whose empty value is a setting (see Options.KeepEmpty).
// tags, if set, is the attribute the resources' tags are in where an
// import leaves them out of it (see EffectiveTags), such as a Google
// resource's labels: its constants are the resource's tags, never a
// default, though leaving them out plans the same, as the labels stay on
// the resource, no longer managed.
//
// hints are what the provider's code says of its arguments' defaults (see
// schemaHints), and verifiedOnly limits the candidates to those leaving
// out is known not to change: nulls and proven defaults.
type defaultRules struct {
	defaults, keepEmpty map[string][]string
	tags                string
	hints               schemaHints
	verifiedOnly        bool
}

// typeRules are a type's defaultRules: its defaults by path.
type typeRules struct {
	defaults     map[string]string
	keepEmpty    []string
	tags         string
	hints        schemaHints
	verifiedOnly bool
}

// defaultValues parses a type's defaults, path=value entries such as
// storage_class=STANDARD or bgp.keepalive_interval=20, by path.
func defaultValues(entries []string) map[string]string {
	if len(entries) == 0 {
		return nil
	}
	values := make(map[string]string, len(entries))
	for _, entry := range entries {
		path, value, ok := strings.Cut(entry, "=")
		if ok && path != "" {
			values[path] = value
		}
	}
	return values
}

// isNamedDefault reports whether an attribute at path, set to expr, has the
// value defaults names for that path.
func isNamedDefault(defaults map[string]string, path []string, expr hclsyntax.Expression) bool {
	value, ok := defaults[strings.Join(path, ".")]
	return ok && matchesValue(expr, value, nil)
}

// isEmpty reports whether body sets nothing.
func isEmpty(body *hclwrite.Body) bool {
	return len(body.Attributes()) == 0 && len(body.Blocks()) == 0
}

// hasEmptyBlocks reports whether a body's nested blocks, or theirs, set
// nothing but null.
func hasEmptyBlocks(body *hclsyntax.Body) bool {
	for _, b := range body.Blocks {
		if nullBody(b.Body) || hasEmptyBlocks(b.Body) {
			return true
		}
	}
	return false
}

// isConstant reports whether expr is a value that refers to nothing and
// calls nothing, such as 3600, "/", true, ["a"] or {}, and isn't null.
func isConstant(expr hclsyntax.Expression) bool {
	if len(expr.Variables()) > 0 {
		return false
	}
	v, diags := expr.Value(nil)
	return !diags.HasErrors() && !v.IsNull() && v.IsWhollyKnown()
}

// isZeroConstant reports whether expr is false, 0, "", [] or {}.
func isZeroConstant(expr hclsyntax.Expression) bool {
	switch e := expr.(type) {
	case *hclsyntax.LiteralValueExpr:
		v := e.Val
		if v.IsNull() || !v.IsKnown() {
			return false
		}
		switch {
		case v.Type() == cty.Bool:
			return v.False()
		case v.Type() == cty.Number:
			return v.Equals(cty.Zero).True()
		}
	case *hclsyntax.TemplateExpr:
		if len(e.Parts) == 0 {
			return true
		}
		s, ok := hclfile.LiteralString(e)
		return ok && s == ""
	case *hclsyntax.TupleConsExpr:
		return len(e.Exprs) == 0
	case *hclsyntax.ObjectConsExpr:
		return len(e.Items) == 0
	}
	return false
}

// hasConstants reports whether a body or its nested blocks set an argument
// to a constant (see isConstant).
func hasConstants(body *hclsyntax.Body) bool {
	for _, attr := range body.Attributes {
		if isConstant(attr.Expr) {
			return true
		}
	}
	for _, b := range body.Blocks {
		if hasConstants(b.Body) {
			return true
		}
	}
	return false
}

// setsComputed reports whether a nested block's body at path, or a block
// in it, sets an argument the provider computes to something but null:
// left out, such as soft_delete_policy { retention_duration_seconds = 0 },
// it would keep what the cloud has, which the plan can't tell from the
// zero value it was.
func setsComputed(schemas *tfjson.ProviderSchemas, typ string, path []string, body *hclsyntax.Body) bool {
	for name, attr := range body.Attributes {
		if s := tfrun.SchemaAttribute(schemas, typ, append(append([]string(nil), path...), name)); !hclfile.IsNullLiteral(attr.Expr) && (s == nil || s.Computed) {
			return true
		}
	}
	for _, b := range body.Blocks {
		if setsComputed(schemas, typ, append(append([]string(nil), path...), b.Type), b.Body) {
			return true
		}
	}
	return false
}
