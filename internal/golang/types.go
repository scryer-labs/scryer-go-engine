package golang

import (
	"context"
	"fmt"
	"go/ast"
	"go/types"
	"strconv"
)

type localPackage struct {
	path  string
	files []*ast.File
	info  *types.Info
	pkg   *types.Package
	state string
}

type localChecker struct {
	ctx        context.Context
	sources    *sources
	packages   map[string]*localPackage
	testGroups map[string]*localPackage
	active     *localPackage
}

func newLocalChecker(ctx context.Context, sources *sources) *localChecker {
	c := &localChecker{ctx: ctx, sources: sources, packages: map[string]*localPackage{}, testGroups: map[string]*localPackage{}}
	for _, component := range sources.components {
		path := component.ImportPath
		if path == "" {
			path = component.ID
		}
		c.packages[component.ID] = &localPackage{path: path}
		c.testGroups[component.ID] = &localPackage{path: path}
	}
	for _, source := range sources.files {
		if source.ast == nil || len(source.fact.ComponentIDs) == 0 || source.fact.SyntaxErrors ||
			!source.fact.InBuild || hasCgo(source.ast) {
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

func hasCgo(file *ast.File) bool {
	for _, imp := range file.Imports {
		if value, _ := strconv.Unquote(imp.Path.Value); value == "C" {
			return true
		}
	}
	return false
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
	if len(choices) != 1 || choices[0].Kind != "package" {
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
	config := types.Config{Importer: c, Error: func(error) {}}
	pkg.pkg, _ = config.Check(pkg.path, c.sources.fset, pkg.files, pkg.info)
}
