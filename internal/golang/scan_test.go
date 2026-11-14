package golang

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestScanLocalPackagesAndCallEvidence(t *testing.T) {
	root := t.TempDir()
	for name, body := range map[string]string{
		"go.mod":  "module example.org/lab\ngo 1.25\nrequire example.org/dep v1.2.3 // indirect\nreplace example.org/dep => ../dep\n",
		"go.work": "go 1.25\nuse .\n",
		"lib/lib.go": `package lib
type Worker struct{}
func (Worker) Run() {}
func Direct[T any](value T) {}
`,
		"main.go": `package main
import "example.org/lab/lib"
type Runner interface{ Run() }
func main() {
	lib.Direct[int](1)
	lib.Worker{}.Run()
	var r Runner = lib.Worker{}
	r.Run()
	f := lib.Worker{}.Run
	f()
	func(){ lib.Direct(2) }()
	_ = int(1)
}
`,
		"main_test.go": `package main
func testHelper() { main() }
`,
		"native.go": `package main
import "C"
func native() { C.execute_v1() }
`,
		"foreign.go": "//go:build scryer_fixture_disabled\n\npackage main\nfunc foreign() { main() }\n",
	} {
		file := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	result, err := Scan(context.Background(), root, scanFacts, false)
	if err != nil {
		t.Fatal(err)
	}
	manifests := result["dependencies"].(map[string]any)["manifests"].([]Manifest)
	if len(manifests) != 1 || len(manifests[0].Requirements) != 1 ||
		!manifests[0].Requirements[0].Indirect || len(manifests[0].Replacements) != 1 {
		t.Fatalf("missing dependency declarations: %+v", manifests)
	}
	architecture := result["architecture"].(map[string]any)
	if links := architecture["links"].([]packageReference); len(links) != 1 || links[0].To != "lib:lib" {
		t.Fatalf("invalid package links: %+v", links)
	}
	calls := result["call-graph"].(graph)
	want := map[string]string{
		"lib.Direct[int]": "resolved", "lib.Worker{}.Run": "resolved", "r.Run": "dynamic-method-dispatch",
		"f": "indirect-call", "C.execute_v1": "native-symbol-resolution", "lib.Direct": "resolved",
	}
	testEdge := false
	inactiveEdge := false
	for _, call := range calls.Calls {
		if call.Callee == "int" {
			t.Fatal("conversion was reported as a function call")
		}
		if expected, ok := want[call.Callee]; ok {
			if (call.Resolution == expected && call.Target != "") || (call.Reason == expected && call.Target == "") {
				delete(want, call.Callee)
			} else {
				t.Errorf("unexpected call: %+v", call)
			}
		}
		testEdge = testEdge || call.Site.File == "main_test.go" && call.TestOnly && call.Resolution == "resolved"
		inactiveEdge = inactiveEdge || call.Site.File == "foreign.go" && call.Reason == "file-not-type-checked"
	}
	if len(want) != 0 || !testEdge || !inactiveEdge {
		t.Fatalf("missing call evidence: %v; test %v; inactive %v; collected %+v", want, testEdge, inactiveEdge, calls.Calls)
	}
}

func TestResolveUsesGoWorkAndLocalModuleReplacement(t *testing.T) {
	root := t.TempDir()
	for file, data := range map[string]string{
		"go.work":      "go 1.25.0\nuse .\n",
		"go.mod":       "module example.org/main\ngo 1.25.0\nrequire example.org/lib v0.0.0\nreplace example.org/lib => ./local\n",
		"main.go":      "package main\nimport \"example.org/lib\"\nfunc main() { lib.Run() }\n",
		"local/go.mod": "module example.org/lib\ngo 1.25.0\n",
		"local/lib.go": "package lib\nfunc Run() {}\n",
	} {
		target := filepath.Join(root, file)
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	scan := func(resolve bool) graph {
		t.Helper()
		facts, err := Scan(context.Background(), root, []string{"call-graph"}, resolve)
		if err != nil {
			t.Fatal(err)
		}
		return facts["call-graph"].(graph)
	}
	if g := scan(false); len(g.Calls) != 1 || g.Calls[0].Target != "" {
		t.Fatalf("static: %+v", g)
	}
	if g := scan(true); g.Semantic == nil || g.Semantic.Status == "fallback" || len(g.Calls) != 1 ||
		g.Calls[0].Target != "local/lib.go:12" {
		t.Fatalf("resolved: %+v", g)
	}
}
