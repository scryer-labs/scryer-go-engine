package golang

import (
	"context"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

type sourceFile struct {
	Path         string   `json:"path"`
	ComponentIDs []string `json:"componentIds"`
	Package      string   `json:"package,omitempty"`
	SyntaxErrors bool     `json:"syntaxErrors,omitempty"`
	TestOnly     bool     `json:"testOnly,omitempty"`
	InBuild      bool     `json:"inBuild"`
	BuildError   string   `json:"buildError,omitempty"`
}

type sourceImport struct {
	Location
	Column            int    `json:"column"`
	Path              string `json:"path"`
	Alias             string `json:"alias,omitempty"`
	TargetComponentID string `json:"targetComponentId,omitempty"`
	Resolution        string `json:"resolution"`
}

type component struct {
	ID         string `json:"componentId"`
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	File       string `json:"file"`
	Directory  string `json:"directory"`
	ImportPath string `json:"importPath,omitempty"`
	Manifest   string `json:"manifest,omitempty"`
}

type packageReference struct {
	From string `json:"fromComponentId"`
	To   string `json:"toComponentId"`
	sourceImport
}

type parsedFile struct {
	fact sourceFile
	ast  *ast.File
}

type sources struct {
	fset           *token.FileSet
	files          []parsedFile
	components     []component
	imports        []sourceImport
	links          []packageReference
	byImport       map[string][]component
	componentsByID map[string]component
	buildContext   build.Context
}

func (p *project) readSources(ctx context.Context) (*sources, error) {
	cfg := build.Default
	cfg.JoinPath = path.Join
	cfg.OpenFile = func(name string) (io.ReadCloser, error) {
		return p.filesystem.Open(filepath.ToSlash(name))
	}
	s := &sources{
		fset: token.NewFileSet(), files: []parsedFile{}, components: []component{}, imports: []sourceImport{},
		links: []packageReference{}, byImport: map[string][]component{}, componentsByID: map[string]component{}, buildContext: cfg,
	}
	modules := map[string]Manifest{}
	for _, manifest := range p.manifests {
		modules[path.Dir(manifest.File)] = manifest
	}
	seen := map[string]int{}
	for _, file := range p.goFiles {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		data, err := fs.ReadFile(p.filesystem, file)
		if err != nil {
			return nil, err
		}
		node, parseErr := parser.ParseFile(s.fset, file, data, parser.ParseComments|parser.SkipObjectResolution)
		dir := path.Dir(file)
		active, buildErr := cfg.MatchFile(dir, path.Base(file))
		fact := sourceFile{Path: file, ComponentIDs: []string{}, SyntaxErrors: parseErr != nil, TestOnly: strings.HasSuffix(file, "_test.go"), InBuild: active}
		if buildErr != nil {
			fact.BuildError = buildErr.Error()
		}
		if node != nil && node.Name != nil && node.Name.Name != "_" {
			fact.Package = node.Name.Name
			id := dir + ":" + fact.Package
			fact.ComponentIDs = append(fact.ComponentIDs, id)
			if i, exists := seen[id]; !exists {
				kind := "package"
				switch {
				case fact.TestOnly && strings.HasSuffix(fact.Package, "_test"):
					kind = "external-test-package"
				case fact.Package == "main":
					kind = "command"
				}
				manifest := owningModule(modules, dir)
				item := component{ID: id, Name: fact.Package, Kind: kind, File: file, Directory: dir, Manifest: manifest.File}
				if manifest.Module != "" && kind != "external-test-package" {
					moduleDir := path.Dir(manifest.File)
					suffix := strings.TrimPrefix(dir, moduleDir+"/")
					if moduleDir == "." {
						suffix = dir
					}
					if dir == moduleDir {
						suffix = ""
					}
					item.ImportPath = path.Join(manifest.Module, suffix)
				}
				seen[id] = len(s.components)
				s.components = append(s.components, item)
			} else if !fact.TestOnly && strings.HasSuffix(s.components[i].File, "_test.go") {
				s.components[i].File = file
			}
		}
		s.files = append(s.files, parsedFile{fact, node})
	}
	for _, item := range s.components {
		s.componentsByID[item.ID] = item
		if item.ImportPath != "" {
			s.byImport[item.ImportPath] = append(s.byImport[item.ImportPath], item)
		}
	}
	s.collectImports()
	return s, nil
}

func owningModule(modules map[string]Manifest, dir string) Manifest {
	for {
		if mod, ok := modules[dir]; ok {
			return mod
		}
		if dir == "." {
			return Manifest{}
		}
		dir = path.Dir(dir)
	}
}

func (s *sources) collectImports() {
	for _, parsed := range s.files {
		if parsed.ast == nil || len(parsed.fact.ComponentIDs) == 0 {
			continue
		}
		for _, spec := range parsed.ast.Imports {
			value, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				continue
			}
			position := s.fset.PositionFor(spec.Path.Pos(), false)
			ref := sourceImport{Location: Location{parsed.fact.Path, position.Line}, Column: position.Column, Path: value, Resolution: "unresolved"}
			if spec.Name != nil {
				ref.Alias = spec.Name.Name
			}
			owner := s.componentsByID[parsed.fact.ComponentIDs[0]]
			targets := s.byImport[value]
			switch {
			case value == "C":
				ref.Resolution = "cgo"
			case len(targets) == 1 && targets[0].Kind == "package":
				ref.Resolution, ref.TargetComponentID = "local-module-candidate", targets[0].ID
				if owner.Manifest == targets[0].Manifest {
					ref.Resolution = "resolved"
				}
				s.links = append(s.links, packageReference{owner.ID, targets[0].ID, ref})
			case len(targets) > 1:
				ref.Resolution = "ambiguous"
			}
			s.imports = append(s.imports, ref)
		}
	}
}

func (s *sources) architecture(root string) map[string]any {
	return map[string]any{"projectId": root, "basis": "go-package-declarations", "components": s.components,
		"links": s.links, "limitations": []string{"external-module-packages", "effective-module-replacements"}}
}

func (s *sources) fileFacts(root string) map[string]any {
	files := make([]sourceFile, 0, len(s.files))
	for _, file := range s.files {
		files = append(files, file.fact)
	}
	return map[string]any{"projectId": root, "basis": "go-source", "files": files, "imports": s.imports}
}
