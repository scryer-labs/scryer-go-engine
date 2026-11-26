package golang

import (
	"context"
	"fmt"
	"go/ast"
	"go/types"
	"sort"
	"strings"
)

type localPackage struct {
	path     string
	manifest string
	files    []*ast.File
	info     *types.Info
	pkg      *types.Package
	state    string
}

type localChecker struct {
	ctx         context.Context
	sources     *sources
	packages    map[string]*localPackage
	testGroups  map[string]*localPackage
	diagnostics []bindingDiagnostic
	active      *localPackage
}

func newLocalChecker(ctx context.Context, sources *sources) *localChecker {
	c := &localChecker{ctx: ctx, sources: sources, packages: map[string]*localPackage{}, testGroups: map[string]*localPackage{}}
	for _, component := range sources.components {
		path := component.ImportPath
		if path == "" {
			path = component.ID
		}
		c.packages[component.ID] = &localPackage{path: path, manifest: component.Manifest}
		c.testGroups[component.ID] = &localPackage{path: path, manifest: component.Manifest}
	}
	for _, source := range sources.files {
		if source.ast == nil || len(source.fact.ComponentIDs) == 0 || source.fact.SyntaxErrors ||
			!source.fact.InBuild {
			continue
		}
		id := source.fact.ComponentIDs[0]
		c.testGroups[id].files = append(c.testGroups[id].files, source.ast)
		if !source.fact.TestOnly {
			c.packages[id].files = append(c.packages[id].files, source.ast)
		}
	}
	return c
}

func (c *localChecker) info(file sourceFile) *types.Info {
	id := file.ComponentIDs[0]
	pkg := c.packages[id]
	if file.TestOnly {
		pkg = c.testGroups[id]
	}
	c.check(pkg)
	return pkg.info
}

func (c *localChecker) Import(path string) (*types.Package, error) {
	choices := c.sources.byImport[path]
	if len(choices) != 1 || choices[0].Kind != "package" || c.active == nil || (!c.sources.allowLocalModuleImports && c.active.manifest != choices[0].Manifest) {
		return nil, fmt.Errorf("no unique local package for %q", path)
	}
	group := c.packages[choices[0].ID]
	if group == c.active || group.state == "checking" || len(group.files) == 0 {
		return nil, fmt.Errorf("local package %q is unavailable", path)
	}
	c.check(group)
	if group.pkg == nil {
		return nil, fmt.Errorf("local package %q has no type information", path)
	}
	return group.pkg, nil
}

func (c *localChecker) check(pkg *localPackage) {
	if pkg.state != "" || len(pkg.files) == 0 || c.ctx.Err() != nil {
		return
	}
	pkg.state = "checking"
	previous := c.active
	c.active = pkg
	defer func() { c.active, pkg.state = previous, "checked" }()
	pkg.info = &types.Info{
		Uses: map[*ast.Ident]types.Object{}, Types: map[ast.Expr]types.TypeAndValue{},
		Selections: map[*ast.SelectorExpr]*types.Selection{}, Instances: map[*ast.Ident]types.Instance{},
	}
	config := types.Config{Importer: c, FakeImportC: true, Error: func(err error) {
		diagnostic := bindingDiagnostic{Message: err.Error(), Kind: "type-error"}
		if located, ok := err.(types.Error); ok {
			pos := c.sources.fset.PositionFor(located.Pos, false)
			diagnostic.File, diagnostic.Line, diagnostic.Column, diagnostic.Message = pos.Filename, pos.Line, pos.Column, located.Msg
			if strings.Contains(located.Msg, "could not import") {
				diagnostic.Kind = "import-unavailable"
			}
		}
		c.diagnostics = append(c.diagnostics, diagnostic)
	}}
	pkg.pkg, _ = config.Check(pkg.path, c.sources.fset, pkg.files, pkg.info)
}

// Local type checking intentionally has no external importer. Its partial
// diagnostics are separate from go-list build selection, not compiler success.
type bindingDiagnostic struct {
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	Column  int    `json:"column,omitempty"`
	Kind    string `json:"kind"`
	Message string `json:"message"`
}
type bindingModel struct {
	Packages    int                 `json:"packages"`
	Basis       string              `json:"basis"`
	Status      string              `json:"status"`
	Diagnostics []bindingDiagnostic `json:"diagnostics"`
}

func (c *localChecker) bindingModel() bindingModel {
	result := bindingModel{Basis: "go-types-local", Status: "resolved", Diagnostics: []bindingDiagnostic{}}
	for _, pkg := range c.packages {
		if pkg.state == "checked" {
			result.Packages++
		}
	}
	for _, pkg := range c.testGroups {
		if pkg.state == "checked" {
			result.Packages++
		}
	}
	if result.Packages == 0 {
		result.Status = "not-collected"
	}
	seen := map[bindingDiagnostic]bool{}
	for _, diagnostic := range c.diagnostics {
		if !seen[diagnostic] {
			result.Diagnostics = append(result.Diagnostics, diagnostic)
			seen[diagnostic] = true
		}
	}
	sort.Slice(result.Diagnostics, func(i, j int) bool {
		a, b := result.Diagnostics[i], result.Diagnostics[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Column != b.Column {
			return a.Column < b.Column
		}
		return a.Message < b.Message
	})
	if len(result.Diagnostics) > 0 {
		result.Status = "partial"
	}
	return result
}
