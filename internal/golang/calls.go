package golang

import (
	"context"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
)

type point struct {
	Line   int `json:"line"`
	Column int `json:"column"`
}

type span struct {
	File  string `json:"file"`
	Start point  `json:"start"`
	End   point  `json:"end"`
}

type symbol struct {
	ID          string `json:"symbolId"`
	Name        string `json:"name,omitempty"`
	Kind        string `json:"kind"`
	Definition  span   `json:"definition"`
	ComponentID string `json:"componentId"`
	TestOnly    bool   `json:"testOnly,omitempty"`
}

type call struct {
	ID          string `json:"callSiteId"`
	Caller      string `json:"callerSymbolId,omitempty"`
	Callee      string `json:"callee"`
	Target      string `json:"targetSymbolId,omitempty"`
	Resolution  string `json:"resolution"`
	Reason      string `json:"resolutionReason,omitempty"`
	Site        span   `json:"site"`
	ComponentID string `json:"componentId"`
	TestOnly    bool   `json:"testOnly,omitempty"`
}

type graph struct {
	ProjectID   string   `json:"projectId"`
	Basis       string   `json:"basis"`
	GOOS        string   `json:"goos"`
	GOARCH      string   `json:"goarch"`
	CgoEnabled  bool     `json:"cgoEnabled"`
	Limitations []string `json:"limitations"`
	Symbols     []symbol `json:"symbols"`
	Calls       []call   `json:"calls"`
}

func (s *sources) callGraph(ctx context.Context, root string) (graph, error) {
	g := graph{
		ProjectID: root, Basis: "go-ast-and-local-types",
		GOOS: s.buildContext.GOOS, GOARCH: s.buildContext.GOARCH, CgoEnabled: s.buildContext.CgoEnabled,
		Limitations: []string{"external-package-types", "dynamic-dispatch", "effective-module-replacements", "inactive-build-files", "native-symbol-resolution"},
		Symbols:     []symbol{}, Calls: []call{},
	}
	checker := newLocalChecker(ctx, s)
	declarations := map[token.Pos]string{}
	for _, source := range s.files {
		if err := ctx.Err(); err != nil {
			return graph{}, err
		}
		if source.ast == nil || len(source.fact.ComponentIDs) == 0 {
			continue
		}
		ast.Inspect(source.ast, func(node ast.Node) bool {
			item, pos := s.functionSymbol(node, source.fact)
			if item.ID != "" {
				g.Symbols = append(g.Symbols, item)
				if pos.IsValid() {
					declarations[pos] = item.ID
				}
			}
			return ctx.Err() == nil
		})
	}
	for _, source := range s.files {
		if err := ctx.Err(); err != nil {
			return graph{}, err
		}
		if source.ast == nil || len(source.fact.ComponentIDs) == 0 {
			continue
		}
		var info *types.Info
		if source.fact.InBuild && !source.fact.SyntaxErrors && !hasCgo(source.ast) {
			info = checker.info(source.fact)
		}
		var parents []string
		var caller string
		ast.Inspect(source.ast, func(node ast.Node) bool {
			if node == nil {
				caller = parents[len(parents)-1]
				parents = parents[:len(parents)-1]
				return true
			}
			parents = append(parents, caller)
			switch node := node.(type) {
			case *ast.FuncDecl, *ast.FuncLit:
				caller = s.symbolID(node.Pos())
			case *ast.CallExpr:
				target, reason, include := callTarget(node.Fun, info, declarations)
				if include {
					g.Calls = append(g.Calls, s.callSite(source.fact, node, caller, target, reason))
				}
			}
			return true
		})
	}
	return g, ctx.Err()
}

func (s *sources) functionSymbol(node ast.Node, file sourceFile) (symbol, token.Pos) {
	name, kind := "", "function"
	var pos token.Pos
	switch n := node.(type) {
	case *ast.FuncDecl:
		name, pos = n.Name.Name, n.Name.Pos()
		if n.Recv != nil {
			kind = "method"
		}
	case *ast.FuncLit:
		kind = "lambda"
	default:
		return symbol{}, token.NoPos
	}
	return symbol{s.symbolID(node.Pos()), name, kind, s.sourceSpan(node), file.ComponentIDs[0], file.TestOnly}, pos
}

func (s *sources) symbolID(pos token.Pos) string {
	p := s.fset.PositionFor(pos, false)
	return fmt.Sprintf("%s:%d", p.Filename, p.Offset)
}

func (s *sources) sourceSpan(node ast.Node) span {
	start, end := s.fset.PositionFor(node.Pos(), false), s.fset.PositionFor(node.End(), false)
	return span{start.Filename, point{start.Line, start.Column}, point{end.Line, end.Column}}
}

func (s *sources) callSite(file sourceFile, node *ast.CallExpr, caller, target, reason string) call {
	resolution := "unresolved"
	if target != "" {
		resolution = "resolved"
	}
	return call{
		ID:     fmt.Sprintf("%s-%d", s.symbolID(node.Pos()), s.fset.PositionFor(node.End(), false).Offset),
		Caller: caller, Callee: types.ExprString(node.Fun), Target: target, Resolution: resolution,
		Reason: reason, Site: s.sourceSpan(node), ComponentID: file.ComponentIDs[0], TestOnly: file.TestOnly,
	}
}

func callTarget(expr ast.Expr, info *types.Info, declarations map[token.Pos]string) (target, reason string, include bool) {
	if info == nil {
		return "", "file-not-type-checked", true
	}
	if value, ok := info.Types[expr]; ok && value.IsType() {
		return "", "", false
	}
	for {
		switch node := expr.(type) {
		case *ast.ParenExpr:
			expr = node.X
		case *ast.IndexExpr:
			// A function value from a container is not a generic instantiation.
			if id := instanceIdentifier(node.X); id == nil || info.Instances[id].Type == nil {
				return "", "indirect-call", true
			}
			expr = node.X
		case *ast.IndexListExpr:
			expr = node.X
		default:
			goto binding
		}
	}
binding:
	id := instanceIdentifier(expr)
	if id == nil {
		return "", "indirect-call", true
	}
	if selector, ok := expr.(*ast.SelectorExpr); ok {
		if selection := info.Selections[selector]; selection != nil {
			receiver := selection.Recv()
			if ptr, ok := receiver.(*types.Pointer); ok {
				receiver = ptr.Elem()
			}
			if _, generic := receiver.(*types.TypeParam); generic || types.IsInterface(receiver) {
				return "", "dynamic-method-dispatch", true
			}
		}
	}
	switch object := info.Uses[id].(type) {
	case *types.Func:
		if target := declarations[object.Pos()]; target != "" {
			return target, "", true
		}
		return "", "declaration-outside-local-source", true
	case *types.Builtin:
		return "", "builtin", true
	case *types.Var:
		return "", "indirect-call", true
	}
	return "", "binding-unavailable", true
}

func instanceIdentifier(expr ast.Expr) *ast.Ident {
	switch node := expr.(type) {
	case *ast.Ident:
		return node
	case *ast.SelectorExpr:
		return node.Sel
	}
	return nil
}
