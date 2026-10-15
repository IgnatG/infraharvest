// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/cli/providers"
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/importer"
)

// The services reference, docs/reference/services.md, lists each
// provider's services and the Terraform types each lists, without calling
// the cloud: the types come from the lister code of each service and from
// the catalogue. It is rewritten with -update, like the CLI reference:
//
//	go test ./internal/cli -run TestGoldenServicesReference -update

const servicesReference = "../../docs/reference/services.md"

// modulePath is the module's import path, which the import paths of the
// provider packages start with.
const modulePath = "github.com/IgnatG/infraharvest/"

// providerTitles are the headings of referenceProviders on the page; a
// provider without one is headed by its name.
var providerTitles = map[string]string{"aws": "AWS", "azure": "Azure", "google": "Google Cloud"}

// terraformTypeName is what a Terraform resource type looks like.
var terraformTypeName = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)+$`)

func TestGoldenServicesReference(t *testing.T) {
	for _, p := range referenceProviders {
		if providers.New(p) == nil {
			t.Skipf("built without %s", p)
		}
	}
	got, err := renderServicesReference()
	if err != nil {
		t.Fatal(err)
	}
	if *update {
		if err := os.MkdirAll(filepath.Dir(servicesReference), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(servicesReference, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(servicesReference)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != got {
		t.Errorf("%s is out of date: run go test ./internal/cli -run TestGoldenServicesReference -update, or the remote workflow's golden task with packages ./internal/cli", filepath.Base(servicesReference))
	}
}

// renderServicesReference documents the services of referenceProviders.
func renderServicesReference() (string, error) {
	var b strings.Builder
	b.WriteString("# Services\n\n")
	b.WriteString("<!-- Generated from the providers by TestGoldenServicesReference in internal/cli/servicesreference_test.go: don't edit. -->\n\n")
	b.WriteString("The services each provider lists, as `--services` and `--skip` name them, and the Terraform resource types each one lists. " +
		"The types are those named in the service's lister code, those the [catalogue](../catalogue.md) assigns to the service, and the child resources the catalogue imports with them. " +
		"A service lists only the types an account has, and a few services decide the type of a resource from what the cloud returns, so the cloud can add types not shown here.\n\n")
	b.WriteString("`--services=*` lists every service except the opt-in ones, which list through the cloud's inventory API what other services already list: name them to use them. " +
		"`infraharvest services <provider>` prints a provider's services.\n")
	for _, name := range referenceProviders {
		if err := writeProviderServices(&b, name); err != nil {
			return "", fmt.Errorf("%s: %w", name, err)
		}
	}
	return b.String(), nil
}

// writeProviderServices writes the table of one provider's services.
func writeProviderServices(b *strings.Builder, name string) error {
	provider := providers.New(name)
	prefix := provider.Name()
	cat, err := catalogue.Builtin(prefix)
	if err != nil {
		return err
	}
	source, err := parseProviderPackage(provider)
	if err != nil {
		return err
	}
	var optIn []string
	if p, ok := provider.(harvest.ProviderWithOptInServices); ok {
		optIn = slices.Sorted(slices.Values(p.OptInServices()))
	}
	services := importer.Services(provider)
	supported := provider.Services()

	title, ok := providerTitles[name]
	if !ok {
		title = name
	}
	fmt.Fprintf(b, "\n## %s\n\n", title)
	fmt.Fprintf(b, "`infraharvest import %s` has %d services", name, len(services))
	if len(optIn) > 0 {
		fmt.Fprintf(b, ", and %d opt-in", len(optIn))
	}
	b.WriteString(".\n\n| Service | Terraform types |\n|---|---|\n")
	for _, service := range slices.Concat(services, optIn) {
		types := serviceTypes(service, supported[service], source, cat, prefix, slices.Contains(optIn, service))
		cellService := "`" + service + "`"
		if slices.Contains(optIn, service) {
			cellService += " (opt-in)"
		}
		cellTypes := "whichever types the cloud's inventory returns"
		if len(types) > 0 {
			cellTypes = "`" + strings.Join(types, "`, `") + "`"
		}
		fmt.Fprintf(b, "| %s | %s |\n", cellService, cellTypes)
	}
	return nil
}

// typedService is a lister that says which of the catalogue's
// types it lists, as Google's cloud_asset and its aliases do.
type typedService interface {
	Types(cat *catalogue.Catalogue) []string
}

// serviceTypes returns the Terraform types service lists, sorted: those
// its lister code names; those the lister says it lists, if it says,
// or else those the catalogue assigns to it (for an opt-in service, those
// the catalogue maps from the cloud's inventory); and the children the
// catalogue declares for any of them.
func serviceTypes(service string, lister harvest.Lister, source *providerSource, cat *catalogue.Catalogue, prefix string, optIn bool) []string {
	found := map[string]bool{}
	for _, typ := range source.literals(listerTypeNames(lister, source.importPath), prefix) {
		found[typ] = true
	}
	if typed, ok := lister.(typedService); ok {
		for _, typ := range typed.Types(cat) {
			found[typ] = true
		}
	} else {
		for _, typ := range cat.Types() {
			e, _ := cat.Entry(typ)
			if (!optIn && e.Service == service) || (optIn && e.Inventory != nil) {
				found[typ] = true
			}
		}
	}
	// Children, and theirs, with their parents.
	for queue := slices.Collect(maps.Keys(found)); len(queue) > 0; queue = queue[1:] {
		e, ok := cat.Entry(queue[0])
		if !ok {
			continue
		}
		for _, child := range e.Children {
			if !found[child] {
				found[child] = true
				queue = append(queue, child)
			}
		}
	}
	return slices.Sorted(maps.Keys(found))
}

// listerTypeNames returns the names of the types a lister
// is made of, in the package importPath: its own, and those of the
// listers it holds, as AWS's guardedLister holds each service's lister.
func listerTypeNames(lister harvest.Lister, importPath string) []string {
	var names []string
	var walk func(v reflect.Value, depth int)
	walk = func(v reflect.Value, depth int) {
		if depth > 3 {
			return
		}
		for v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer {
			if v.IsNil() {
				return
			}
			v = v.Elem()
		}
		if v.Kind() != reflect.Struct {
			return
		}
		t := v.Type()
		if t.PkgPath() == importPath && t.Name() != "" && !slices.Contains(names, t.Name()) {
			names = append(names, t.Name())
		}
		for i := 0; i < v.NumField(); i++ {
			if t.Field(i).Anonymous {
				continue
			}
			if k := v.Field(i).Kind(); k == reflect.Interface || k == reflect.Pointer {
				walk(v.Field(i), depth+1)
			}
		}
	}
	walk(reflect.ValueOf(lister), 0)
	return names
}

// providerSource is the parsed source of a provider's package.
type providerSource struct {
	importPath string
	// methods are the package's methods, by the name of their receiver's
	// type.
	methods map[string][]*ast.FuncDecl
	// funcs are the package's functions, by name.
	funcs map[string]*ast.FuncDecl
	// values are the values of the package's variables and constants, by
	// name, such as a map from an API's kinds to Terraform types.
	values map[string][]ast.Expr
}

// parseProviderPackage parses the Go files, without the tests, of the
// package that implements provider.
func parseProviderPackage(provider harvest.Provider) (*providerSource, error) {
	t := reflect.TypeOf(provider)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	importPath := t.PkgPath()
	relative, ok := strings.CutPrefix(importPath, modulePath)
	if !ok {
		return nil, fmt.Errorf("package %s isn't in module %s", importPath, modulePath)
	}
	dir := filepath.Join("..", "..", filepath.FromSlash(relative))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	source := &providerSource{importPath: importPath, methods: map[string][]*ast.FuncDecl{}, funcs: map[string]*ast.FuncDecl{}, values: map[string][]ast.Expr{}}
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		for _, decl := range file.Decls {
			if gen, ok := decl.(*ast.GenDecl); ok {
				source.addValues(gen)
				continue
			}
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			if fn.Recv == nil || len(fn.Recv.List) == 0 {
				source.funcs[fn.Name.Name] = fn
				continue
			}
			receiver := receiverTypeName(fn.Recv.List[0].Type)
			source.methods[receiver] = append(source.methods[receiver], fn)
		}
	}
	return source, nil
}

// addValues records the values of a declaration of package-level
// variables or constants.
func (s *providerSource) addValues(gen *ast.GenDecl) {
	if gen.Tok != token.VAR && gen.Tok != token.CONST {
		return
	}
	for _, spec := range gen.Specs {
		value, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		for _, name := range value.Names {
			s.values[name.Name] = append(s.values[name.Name], value.Values...)
		}
	}
}

// receiverTypeName returns the name of a method's receiver type.
func receiverTypeName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.StarExpr:
		return receiverTypeName(e.X)
	case *ast.ParenExpr:
		return receiverTypeName(e.X)
	case *ast.IndexExpr:
		return receiverTypeName(e.X)
	case *ast.IndexListExpr:
		return receiverTypeName(e.X)
	case *ast.Ident:
		return e.Name
	}
	return ""
}

// literals returns the Terraform types, of the provider whose types start
// with prefix, that string literals name in the methods of typeNames, in
// the package's functions those call, directly or not, and in the values
// of the package's variables and constants they refer to.
func (s *providerSource) literals(typeNames []string, prefix string) []string {
	found := map[string]bool{}
	visited := map[*ast.FuncDecl]bool{}
	visitedValues := map[string]bool{}
	var visitFunc func(fn *ast.FuncDecl)
	var inspect func(n ast.Node) bool
	inspect = func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.BasicLit:
			if n.Kind != token.STRING {
				return true
			}
			if value, err := strconv.Unquote(n.Value); err == nil && strings.HasPrefix(value, prefix+"_") && terraformTypeName.MatchString(value) {
				found[value] = true
			}
		case *ast.SelectorExpr:
			// x.f names a field or method of x, or something of another
			// package, not one of this package's functions.
			ast.Inspect(n.X, inspect)
			return false
		case *ast.Ident:
			if fn, ok := s.funcs[n.Name]; ok {
				visitFunc(fn)
			}
			if values, ok := s.values[n.Name]; ok && !visitedValues[n.Name] {
				visitedValues[n.Name] = true
				for _, value := range values {
					ast.Inspect(value, inspect)
				}
			}
		}
		return true
	}
	visitFunc = func(fn *ast.FuncDecl) {
		if visited[fn] {
			return
		}
		visited[fn] = true
		ast.Inspect(fn.Body, inspect)
	}
	for _, name := range typeNames {
		for _, fn := range s.methods[name] {
			visitFunc(fn)
		}
	}
	return slices.Sorted(maps.Keys(found))
}
