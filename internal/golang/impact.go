package golang

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/printer"
	"go/scanner"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"

	"github.com/scryer-labs/scryer-go-engine/internal/analysis"
)

func tokenFingerprint(source string) string {
	var scan scanner.Scanner
	fs := token.NewFileSet()
	f := fs.AddFile("", -1, len(source))
	scan.Init(f, []byte(source), nil, 0)
	var normalized strings.Builder
	for {
		_, kind, literal := scan.Scan()
		if kind == token.EOF {
			break
		}
		fmt.Fprintf(&normalized, "%d:%s;", kind, literal)
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(normalized.String())))
}
func (s *sources) impactIdentity(node ast.Node) (string, string, string, string) {
	if literal, ok := node.(*ast.FuncLit); ok {
		return s.closureImpactIdentity(literal)
	}
	fn, ok := node.(*ast.FuncDecl)
	if !ok {
		return "", "", "", ""
	}
	clone := *fn
	clone.Doc = nil
	var source, signature, receiver bytes.Buffer
	_ = printer.Fprint(&source, s.fset, &clone)
	_ = printer.Fprint(&signature, s.fset, fn.Type)
	if fn.Recv != nil && len(fn.Recv.List) > 0 {
		_ = printer.Fprint(&receiver, s.fset, fn.Recv.List[0].Type)
	}
	file := s.fset.PositionFor(fn.Pos(), false).Filename
	name := fn.Name.Name
	if receiver.Len() > 0 {
		name = "(" + receiver.String() + ")." + name
	}
	display := name + strings.TrimPrefix(signature.String(), "func")
	return file + "|" + display, display, tokenFingerprint(source.String()), source.String()
}

// Impact projects the very same graph returned by Scan, including build-model
// resolution and its limitations. It never substitutes a second call collector.
func Impact(ctx context.Context, root string, resolve bool) (analysis.ImpactSnapshot, error) {
	if !filepath.IsAbs(root) {
		return analysis.ImpactSnapshot{}, fmt.Errorf("impact requires absolute snapshot roots")
	}
	facts, err := Scan(ctx, root, []string{"call-graph", "files", "dependencies"}, resolve)
	if err != nil {
		return analysis.ImpactSnapshot{}, err
	}
	graph := facts["call-graph"].(graph)
	files := facts["files"].(map[string]any)["files"].([]sourceFile)
	invalidFiles := map[string]bool{}
	syntaxDiagnostics := []map[string]string{}
	for _, file := range files {
		if file.SyntaxErrors {
			invalidFiles[file.Path] = true
			syntaxDiagnostics = append(syntaxDiagnostics, map[string]string{"file": file.Path, "message": file.SyntaxDiagnostic})
		}
	}
	if resolve {
		for _, file := range files {
			if file.InBuild && file.SyntaxErrors {
				return analysis.ImpactSnapshot{}, fmt.Errorf("Go source parser errors in selected build file %s", file.Path)
			}
		}
	}
	result := analysis.ImpactSnapshot{Symbols: []analysis.ImpactSymbol{}, Calls: []analysis.ImpactCall{}, Contexts: []analysis.ImpactContext{}, Notes: []string{"Go impact reuses scan's AST/local type bindings. Interface/dynamic dispatch and native/external targets remain boundaries; go-list resolution validates build selection, not complete runtime dispatch."}}
	result.Resolution, _ = json.Marshal(map[string]any{"binding": graph.Binding, "syntaxDiagnostics": syntaxDiagnostics})
	if resolve {
		if graph.Semantic == nil || graph.Semantic.Status != "resolved" {
			return result, fmt.Errorf("dynamic Go scan did not resolve successfully: %+v", graph.Semantic)
		}
		result.Resolution, _ = json.Marshal(map[string]any{"status": graph.Semantic.Status, "basis": graph.Semantic.Basis, "bindingBasis": graph.Basis, "goos": graph.GOOS, "goarch": graph.GOARCH, "cgoEnabled": graph.CgoEnabled, "limitations": graph.Limitations, "binding": graph.Binding, "syntaxDiagnostics": syntaxDiagnostics})
	}
	for _, symbol := range graph.Symbols {
		if symbol.impactKey == "" || invalidFiles[symbol.Definition.File] {
			continue
		}
		role := "PRODUCTION"
		executionKind := ""
		if symbol.TestOnly {
			role = "TEST"
			executionKind = "test-harness"
		}
		raw, _ := json.Marshal(symbol.Definition)
		var loc analysis.ImpactRange
		_ = json.Unmarshal(raw, &loc)
		result.Symbols = append(result.Symbols, analysis.ImpactSymbol{ID: symbol.ID, Key: symbol.impactKey, Signature: symbol.impactSignature, Fingerprint: symbol.impactFingerprint, Source: symbol.impactSource, Location: loc, Role: role, ExecutionKind: executionKind})
	}
	for _, call := range graph.Calls {
		raw, _ := json.Marshal(call.Site)
		var loc analysis.ImpactRange
		_ = json.Unmarshal(raw, &loc)
		edge := analysis.ImpactCall{ID: call.ID, Caller: call.Caller, Callee: call.Callee, Resolution: call.Resolution, Site: loc}
		if resolve {
			edge.Target = call.Target
			edge.Kind = "scan-resolved"
		} else {
			edge.Candidate = call.Target
		}
		result.Calls = append(result.Calls, edge)
	}
	// Context fingerprints retain imports, types and package/global declarations.
	byFile := map[string][]analysis.ImpactRange{}
	for _, s := range result.Symbols {
		byFile[s.Location.File] = append(byFile[s.Location.File], s.Location)
	}
	for _, fileFact := range files {
		file := fileFact.Path
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
		if err != nil {
			return result, err
		}
		if fileFact.SyntaxErrors {
			// Parser recovery can discard malformed tokens. Keep the complete raw
			// source as unassessed context instead of masking recovered functions.
			result.Contexts = append(result.Contexts, analysis.ImpactContext{Path: file, Fingerprint: rawContextFingerprint(data)})
			result.Notes = append(result.Notes, "Parser errors in "+file+"; recovered callable projections omitted and complete source retained as unassessed context: "+fileFact.SyntaxDiagnostic)
			continue
		}
		lines := strings.SplitAfter(string(data), "\n")
		for _, loc := range byFile[file] {
			for line := loc.Start.Line; line <= loc.End.Line && line <= len(lines); line++ {
				// Preserve tokens outside a function even when it shares a line.
				text := lines[line-1]
				start, end := 0, len(text)
				if line == loc.Start.Line {
					start = loc.Start.Column - 1
				}
				if line == loc.End.Line {
					end = loc.End.Column - 1
				}
				if start <= end && end <= len(text) {
					lines[line-1] = text[:start] + strings.Repeat(" ", end-start) + text[end:]
				}
			}
		}
		result.Contexts = append(result.Contexts, analysis.ImpactContext{Path: file, Fingerprint: tokenFingerprint(strings.Join(lines, ""))})
	}
	dependencies := facts["dependencies"].(map[string]any)
	for _, manifest := range dependencies["manifests"].([]Manifest) {
		for _, file := range []string{manifest.File, filepath.ToSlash(filepath.Join(filepath.Dir(manifest.File), "go.sum"))} {
			if err := appendImpactConfiguration(root, file, &result); err != nil {
				return result, err
			}
		}
	}
	for _, workspace := range dependencies["workspaces"].([]Workspace) {
		for _, file := range []string{workspace.File, workspace.File + ".sum"} {
			if err := appendImpactConfiguration(root, file, &result); err != nil {
				return result, err
			}
		}
	}
	result.Notes = append(result.Notes, "Module/workspace manifests and checksums are context evidence; their effects on unchanged declarations are unassessed. Closure keys use lexical binding/call-site anchors; duplicate anchors remain ambiguous. Callback registration is not invocation.")
	return result, nil
}

func appendImpactConfiguration(root, file string, snapshot *analysis.ImpactSnapshot) error {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	// Manifests are not Go source; retain all content except portable line endings.
	fingerprint := rawContextFingerprint(data)
	snapshot.Contexts = append(snapshot.Contexts, analysis.ImpactContext{Path: file, Fingerprint: fingerprint})
	return nil
}

func rawContextFingerprint(data []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))))
}

// Closure correspondence uses lexical anchors, not byte offsets or body hashes.
// Repeated indistinguishable anchors deliberately share a key, so Core can mark
// them ambiguous instead of silently pairing an arbitrary callback after edits.
func (s *sources) closureImpactIdentity(fn *ast.FuncLit) (string, string, string, string) {
	if s.impactParents == nil {
		s.impactParents = map[ast.Node]ast.Node{}
		for _, file := range s.files {
			if file.ast == nil {
				continue
			}
			var stack []ast.Node
			ast.Inspect(file.ast, func(node ast.Node) bool {
				if node == nil {
					stack = stack[:len(stack)-1]
					return true
				}
				if len(stack) > 0 {
					s.impactParents[node] = stack[len(stack)-1]
				}
				stack = append(stack, node)
				return true
			})
		}
	}
	anchor, scope := "anonymous", "package"
	child := ast.Node(fn)
	for parent := s.impactParents[child]; parent != nil; parent = s.impactParents[child] {
		if anchor == "anonymous" {
			switch p := parent.(type) {
			case *ast.AssignStmt:
				var names []string
				for _, lhs := range p.Lhs {
					names = append(names, types.ExprString(lhs))
				}
				anchor = "binding:" + strings.Join(names, ",")
			case *ast.ValueSpec:
				var names []string
				for _, name := range p.Names {
					names = append(names, name.Name)
				}
				anchor = "binding:" + strings.Join(names, ",")
			case *ast.CallExpr:
				for index, arg := range p.Args {
					if arg == child {
						anchor = fmt.Sprintf("argument:%s:%d", types.ExprString(p.Fun), index)
					}
				}
				if p.Fun == child {
					anchor = "immediate-call"
				}
			case *ast.ReturnStmt:
				anchor = "return"
			}
		}
		switch parent.(type) {
		case *ast.FuncDecl, *ast.FuncLit:
			scope, _, _, _ = s.impactIdentity(parent)
			parent = nil
		}
		if parent == nil {
			break
		}
		child = parent
	}
	var source, signature bytes.Buffer
	_ = printer.Fprint(&source, s.fset, fn)
	_ = printer.Fprint(&signature, s.fset, fn.Type)
	file := s.fset.PositionFor(fn.Pos(), false).Filename
	display := "closure " + anchor + " " + signature.String()
	return file + "|" + scope + "|" + display, display, tokenFingerprint(source.String()), source.String()
}
