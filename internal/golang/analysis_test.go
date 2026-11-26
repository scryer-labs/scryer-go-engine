package golang

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scryer-labs/scryer-go-engine/internal/analysis"
)

func TestImpactRetainsScanLinksAndStableTokenIdentity(t *testing.T) {
	root := t.TempDir()
	write := func(name, source string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.test/fixture\n\ngo 1.24\n")
	write("value.go", "package fixture\nfunc Value(x int) int { if x>0{return 1};return 0 }\nfunc Caller() int { return Value(1) }\n")
	before, err := Impact(context.Background(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	write("value.go", "package fixture\n// shifted\nfunc Value(x int) int { if x>0{return /*comment*/ 1};return 0 }\nfunc Caller() int { return Value(1) }\n")
	after, err := Impact(context.Background(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Symbols) != 2 || len(after.Calls) != 1 || after.Calls[0].Candidate == "" {
		t.Fatal("missing scan graph correspondence")
	}
	if before.Symbols[0].Fingerprint != after.Symbols[0].Fingerprint || before.Symbols[0].Key != after.Symbols[0].Key {
		t.Fatal("comments changed the identity or body fingerprint")
	}
}
func TestGoProfilesMapLocalModulesAndRetainMisses(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "value.go"), []byte("package x"), 0600); err != nil {
		t.Fatal(err)
	}
	files := map[string]*analysis.CoverageFile{}
	input := []byte("mode: count\nexample.test/x/value.go:2.1,3.2 1 4\nexample.test/x/value.go:4.1,5.2 1 0\nexternal.test/other.go:1.1,2.1 1 9\n")
	if err := readProfile(root, []Manifest{{Module: "example.test/x", File: "go.mod"}}, input, files); err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || len(files["value.go"].Regions) != 2 || files["value.go"].Regions[1].Count != 0 {
		t.Fatal(files)
	}
	if err := readProfile(root, nil, []byte("mode: count\nbroken"), files); err == nil {
		t.Fatal("accepted malformed coverage")
	}
}

func Test_should_retain_closure_bodies_and_stable_keys_when_lines_and_comments_move(t *testing.T) {
	root := filepath.Join(t.TempDir(), "工程 with spaces")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	write := func(name, source string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.test/closures\n\ngo 1.24\n")
	source := "package fixture\nfunc Target() {}\nfunc Register(f func()) {}\nfunc Parent() {\n cb := func(){Target()}\n Register(func(){Target()})\n cb()\n func(){Target()}()\n}\n"
	write("value.go", source)
	before, err := Impact(context.Background(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	write("value.go", strings.ReplaceAll("// offset moved\n"+strings.Replace(source, "Target()}\n Register", "/* body */Target()}\n Register", 1), "\n", "\r\n"))
	after, err := Impact(context.Background(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Symbols) != 6 || len(after.Symbols) != 6 {
		t.Fatalf("missing closure projections: %d %d", len(before.Symbols), len(after.Symbols))
	}
	for i, symbol := range before.Symbols {
		if symbol.Key != after.Symbols[i].Key || symbol.Fingerprint != after.Symbols[i].Fingerprint {
			t.Fatalf("line/comment move changed identity: %s", symbol.Key)
		}
	}
	ids := map[string]bool{}
	for _, symbol := range after.Symbols {
		ids[symbol.ID] = true
	}
	closureCalls, indirect, immediate := 0, false, false
	for _, call := range after.Calls {
		if !ids[call.Caller] {
			t.Fatalf("dropped caller: %+v", call)
		}
		if call.Callee == "Target" {
			closureCalls++
		}
		if call.Callee == "cb" {
			indirect = call.Candidate == "" && call.Resolution == "unresolved"
		}
		if strings.HasPrefix(call.Callee, "(func") || strings.HasPrefix(call.Callee, "func") {
			immediate = call.Candidate != ""
		}
	}
	if closureCalls != 3 || !indirect || !immediate {
		t.Fatalf("closure call boundaries changed: %+v", after.Calls)
	}
	source = strings.Replace(source, "cb := func(){Target()}", "cb := func(){Target();Target()}", 1)
	write("value.go", source)
	changed, err := Impact(context.Background(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	if before.Symbols[3].Key != changed.Symbols[3].Key || before.Symbols[3].Fingerprint == changed.Symbols[3].Fingerprint {
		t.Fatal("changed closure lost stable correspondence")
	}
}

func Test_should_keep_duplicate_callback_anchors_ambiguous_without_inventing_calls(t *testing.T) {
	root := t.TempDir()
	for name, source := range map[string]string{"go.mod": "module example.test/x\n\ngo 1.24\n", "x.go": "package x\nfunc Register(f func()){}\nfunc Parent(){ Register(func(){}); Register(func(){}) }\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := Impact(context.Background(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Symbols) != 4 || snapshot.Symbols[2].Key != snapshot.Symbols[3].Key {
		t.Fatal("duplicate callback keys must remain ambiguous", snapshot.Symbols)
	}
	if len(snapshot.Calls) != 2 {
		t.Fatal("callback registration must not invent execution edges", snapshot.Calls)
	}
}

func Test_should_preserve_type_diagnostics_and_valid_local_bindings_when_other_calls_fail(t *testing.T) {
	root := t.TempDir()
	for name, source := range map[string]string{"go.mod": "module example.test/x\n\ngo 1.24\n", "x.go": "package x\nimport \"fmt\"\nfunc Target() {}\nfunc Caller(){Target(); var bad int = \"wrong\"; fmt.Println(bad)}\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
	}
	facts, err := Scan(context.Background(), root, []string{"call-graph"}, false)
	if err != nil {
		t.Fatal(err)
	}
	g := facts["call-graph"].(graph)
	if g.Binding.Basis != "go-types-local" || g.Binding.Status != "partial" || len(g.Binding.Diagnostics) < 2 {
		t.Fatal(g.Binding)
	}
	typeError, importBoundary := false, false
	for _, diagnostic := range g.Binding.Diagnostics {
		if diagnostic.File != "x.go" || diagnostic.Line < 1 || diagnostic.Column < 1 {
			t.Fatal("diagnostic lacks source position", diagnostic)
		}
		typeError = typeError || diagnostic.Kind == "type-error"
		importBoundary = importBoundary || diagnostic.Kind == "import-unavailable"
	}
	if !typeError || !importBoundary {
		t.Fatal(g.Binding)
	}
	if len(g.Calls) < 1 || g.Calls[0].Callee != "Target" || g.Calls[0].Target == "" {
		t.Fatal("valid local edge lost beside errors", g.Calls)
	}
	snapshot, err := Impact(context.Background(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(snapshot.Resolution), "go-types-local") {
		t.Fatal("static impact dropped binding diagnostics")
	}
}

func Test_should_record_manifest_and_global_context_when_callable_tokens_are_unchanged(t *testing.T) {
	root := t.TempDir()
	write := func(name, source string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.test/x\n\ngo 1.24\n")
	write("x.go", "package x\ntype Value int\nvar Flag = 1\nfunc Target() int{return Flag}\n")
	before, err := Impact(context.Background(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	write("go.mod", "module example.test/x\n\ngo 1.25\n")
	write("x.go", "package x\ntype Value string\nvar Flag = 2\nfunc Target() int{return Flag}\n")
	after, err := Impact(context.Background(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	if before.Symbols[0].Fingerprint != after.Symbols[0].Fingerprint {
		t.Fatal("unchanged callable changed")
	}
	if len(before.Contexts) != 2 || len(after.Contexts) != 2 {
		t.Fatal(before.Contexts, after.Contexts)
	}
	for i, item := range before.Contexts {
		if item.Fingerprint == after.Contexts[i].Fingerprint {
			t.Fatalf("lost changed context %s", item.Path)
		}
	}
}

func Test_should_preserve_malformed_source_edits_as_unassessed_context_beside_valid_files(t *testing.T) {
	root := t.TempDir()
	write := func(name, source string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.test/invalid\n\ngo 1.24\n")
	write("valid.go", "package fixture\nfunc Valid(){}\n")
	write("broken.go", "package fixture\nfunc broken(){ @ }\n")
	before, err := Impact(context.Background(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	write("broken.go", "package fixture\nfunc broken(){ # }\n")
	after, err := Impact(context.Background(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Symbols) != 1 || after.Symbols[0].Location.File != "valid.go" {
		t.Fatalf("recovered malformed callable projected: %+v", after.Symbols)
	}
	context := func(snapshot analysis.ImpactSnapshot) string {
		for _, item := range snapshot.Contexts {
			if item.Path == "broken.go" {
				return item.Fingerprint
			}
		}
		t.Fatal("missing malformed file context")
		return ""
	}
	if context(before) == context(after) {
		t.Fatal("parser recovery hid changed invalid tokens")
	}
	if !strings.Contains(string(after.Resolution), "syntaxDiagnostics") || !strings.Contains(string(after.Resolution), "broken.go") || !strings.Contains(strings.Join(after.Notes, "\n"), "recovered callable projections omitted") {
		t.Fatal("missing malformed source evidence", after)
	}
}
