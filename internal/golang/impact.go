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
	facts, err := Scan(ctx, root, []string{"call-graph", "files"}, resolve)
	if err != nil {
		return analysis.ImpactSnapshot{}, err
	}
	graph := facts["call-graph"].(graph)
	if resolve {
		for _, file := range facts["files"].(map[string]any)["files"].([]sourceFile) {
			if file.InBuild && file.SyntaxErrors {
				return analysis.ImpactSnapshot{}, fmt.Errorf("Go source parser errors in selected build file %s", file.Path)
			}
		}
	}
	result := analysis.ImpactSnapshot{Symbols: []analysis.ImpactSymbol{}, Calls: []analysis.ImpactCall{}, Contexts: []analysis.ImpactContext{}, Notes: []string{"Go impact reuses scan's AST/local type bindings. Interface/dynamic dispatch and native/external targets remain boundaries; go-list resolution validates build selection, not complete runtime dispatch."}}
	if resolve {
		if graph.Semantic == nil || graph.Semantic.Status != "resolved" {
			return result, fmt.Errorf("dynamic Go scan did not resolve successfully: %+v", graph.Semantic)
		}
		result.Resolution, _ = json.Marshal(map[string]any{"status": graph.Semantic.Status, "basis": graph.Semantic.Basis, "bindingBasis": graph.Basis, "goos": graph.GOOS, "goarch": graph.GOARCH, "cgoEnabled": graph.CgoEnabled, "limitations": graph.Limitations})
	}
	for _, symbol := range graph.Symbols {
		if symbol.impactKey == "" {
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
	for _, fileFact := range facts["files"].(map[string]any)["files"].([]sourceFile) {
		file := fileFact.Path
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
		if err != nil {
			return result, err
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
	return result, nil
}
