package golang

import (
	"context"
	"os"
	"path/filepath"
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
