package golang

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"slices"

	"golang.org/x/mod/modfile"
)

type Location struct {
	File string `json:"file"`
	Line int    `json:"line"`
}

type Requirement struct {
	Location
	Module   string `json:"module"`
	Version  string `json:"version"`
	Indirect bool   `json:"indirect"`
}

type Replacement struct {
	Location
	Module             string `json:"module"`
	Version            string `json:"version,omitempty"`
	Replacement        string `json:"replacement"`
	ReplacementVersion string `json:"replacementVersion,omitempty"`
}

type Manifest struct {
	File         string        `json:"file"`
	Module       string        `json:"module"`
	GoVersion    string        `json:"goVersion,omitempty"`
	Requirements []Requirement `json:"requirements"`
	Replacements []Replacement `json:"replacements"`
	Exclusions   []Requirement `json:"exclusions,omitempty"`
}

type Workspace struct {
	File         string        `json:"file"`
	GoVersion    string        `json:"goVersion,omitempty"`
	Uses         []string      `json:"uses"`
	Replacements []Replacement `json:"replacements"`
}

type project struct {
	filesystem fs.FS
	manifests  []Manifest
	workspaces []Workspace
	goFiles    []string
}

var scanFacts = []string{"dependencies", "architecture", "files", "call-graph"}

func Supports(fact string) bool {
	return slices.Contains(scanFacts, fact)
}

func Scan(ctx context.Context, root string, selected []string, resolveMode ...bool) (map[string]any, error) {
	p := project{filesystem: os.DirFS(root), manifests: []Manifest{}, workspaces: []Workspace{}}
	if err := p.discover(ctx); err != nil {
		return nil, err
	}
	facts := map[string]any{}
	if slices.Contains(selected, "dependencies") {
		facts["dependencies"] = map[string]any{
			"basis": "go-module-declarations", "manifests": p.manifests, "workspaces": p.workspaces,
			"limitations": []string{"selected-module-versions", "transitive-dependencies", "remote-version-metadata"},
		}
	}
	wantCalls := slices.Contains(selected, "call-graph")
	if slices.Contains(selected, "architecture") || slices.Contains(selected, "files") || wantCalls {
		sources, err := p.readSources(ctx)
		if err != nil {
			return nil, err
		}
		var model *packageModel
		if len(resolveMode) == 1 && resolveMode[0] && (wantCalls || slices.Contains(selected, "files")) {
			result := loadBuildModel(ctx, root, p.manifests, p.workspaces)
			if !sources.applyBuildModel(root, result) && result.Status != "fallback" {
				result.Status, result.Message = "fallback", "no scanned source files in Go package model"
			}
			model = &result
		}
		if slices.Contains(selected, "architecture") {
			facts["architecture"] = sources.architecture(root)
		}
		if slices.Contains(selected, "files") {
			data := sources.fileFacts(root)
			if model != nil {
				data["semantic"] = model
			}
			facts["files"] = data
		}
		if wantCalls {
			graph, err := sources.callGraph(ctx, root)
			if err != nil {
				return nil, err
			}
			graph.Semantic = model
			facts["call-graph"] = graph
		}
	}
	return facts, nil
}

func (p *project) discover(ctx context.Context) error {
	return fs.WalkDir(p.filesystem, ".", func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".orrery", ".scryer", ".tooling", ".gradle", "vendor", "node_modules", "target", "build", "dist", "testdata":
				return fs.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		switch entry.Name() {
		case "go.mod", "go.work":
			return p.readManifest(file)
		}
		if path.Ext(file) == ".go" {
			p.goFiles = append(p.goFiles, file)
		}
		return nil
	})
}

func (p *project) readManifest(file string) error {
	data, err := fs.ReadFile(p.filesystem, file)
	if err != nil {
		return err
	}
	if path.Base(file) == "go.work" {
		work, err := modfile.ParseWork(file, data, nil)
		if err != nil {
			return err
		}
		result := Workspace{File: file, Uses: []string{}, Replacements: replacements(file, work.Replace)}
		if work.Go != nil {
			result.GoVersion = work.Go.Version
		}
		for _, use := range work.Use {
			result.Uses = append(result.Uses, use.Path)
		}
		p.workspaces = append(p.workspaces, result)
		return nil
	}
	mod, err := modfile.Parse(file, data, nil)
	if err != nil {
		return err
	}
	if mod.Module == nil {
		return fmt.Errorf("%s: missing module directive", file)
	}
	result := Manifest{File: file, Module: mod.Module.Mod.Path, Requirements: []Requirement{}, Replacements: replacements(file, mod.Replace)}
	if mod.Go != nil {
		result.GoVersion = mod.Go.Version
	}
	for _, require := range mod.Require {
		result.Requirements = append(result.Requirements, Requirement{Location{file, require.Syntax.Start.Line}, require.Mod.Path, require.Mod.Version, require.Indirect})
	}
	for _, exclude := range mod.Exclude {
		result.Exclusions = append(result.Exclusions, Requirement{Location{file, exclude.Syntax.Start.Line}, exclude.Mod.Path, exclude.Mod.Version, false})
	}
	p.manifests = append(p.manifests, result)
	return nil
}

func replacements(file string, directives []*modfile.Replace) []Replacement {
	result := make([]Replacement, 0, len(directives))
	for _, directive := range directives {
		result = append(result, Replacement{Location{file, directive.Syntax.Start.Line}, directive.Old.Path, directive.Old.Version, directive.New.Path, directive.New.Version})
	}
	return result
}
