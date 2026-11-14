package golang

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type listedPackage struct {
	Dir          string
	ImportPath   string
	Name         string
	GoFiles      []string
	CgoFiles     []string
	TestGoFiles  []string
	XTestGoFiles []string
	Incomplete   bool
	Error        *struct{ Err string }
	DepsErrors   []struct{ Err string }
}

type packageModel struct {
	Basis    string          `json:"basis"`
	Status   string          `json:"status"`
	Message  string          `json:"message,omitempty"`
	Packages []listedPackage `json:"-"`
}

func loadBuildModel(ctx context.Context, root string, manifests []Manifest, workspaces []Workspace) packageModel {
	model := packageModel{Basis: "go-list", Status: "fallback"}
	if len(manifests) == 0 {
		model.Message = "no Go module manifest found"
		return model
	}
	bounded, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	tool, err := exec.LookPath("go")
	if err != nil {
		model.Message = "Go toolchain is unavailable"
		return model
	}
	seen := map[string]bool{}
	var messages []string
	for _, manifest := range manifests {
		dir := filepath.Join(root, filepath.Dir(manifest.File))
		args := []string{"list", "-e", "-json", "-deps", "-mod=readonly"}
		if info, statErr := os.Stat(filepath.Join(dir, "vendor", "modules.txt")); statErr == nil && !info.IsDir() && len(workspaces) == 0 {
			args[len(args)-1] = "-mod=vendor"
		}
		command := exec.CommandContext(bounded, tool, append(args, "./...")...)
		command.Dir = dir
		command.WaitDelay = time.Second
		command.Env = goListEnv(root, dir)
		var stderr bytes.Buffer
		command.Stderr = &stderr
		output, err := command.Output()
		if err != nil {
			message := strings.TrimSpace(stderr.String())
			if message == "" {
				message = err.Error()
			}
			messages = append(messages, message)
		}
		decoder := json.NewDecoder(bytes.NewReader(output))
		for {
			var pkg listedPackage
			err := decoder.Decode(&pkg)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				messages = append(messages, err.Error())
				break
			}
			if pkg.Error != nil {
				messages = append(messages, pkg.Error.Err)
			}
			for _, failure := range pkg.DepsErrors {
				messages = append(messages, failure.Err)
			}
			if pkg.Dir == "" || pkg.Error != nil {
				continue
			}
			key := pkg.Dir + "\x00" + pkg.ImportPath
			if !seen[key] {
				model.Packages = append(model.Packages, pkg)
				seen[key] = true
			}
		}
	}
	switch {
	case len(model.Packages) == 0:
		model.Message = "no package build model available"
	case len(messages) > 0:
		model.Status = "partial"
	default:
		model.Status = "resolved"
	}
	if len(messages) > 0 {
		model.Message = messages[0]
	}
	return model
}

func goListEnv(root, dir string) []string {
	env := slices.DeleteFunc(os.Environ(), func(setting string) bool {
		name, _, _ := strings.Cut(setting, "=")
		return slices.Contains([]string{"GOPROXY", "GOSUMDB", "GOTOOLCHAIN", "GOFLAGS", "GOENV", "GOPACKAGESDRIVER", "GONOPROXY", "GOPRIVATE", "GOWORK"}, name)
	})
	workspace := "off"
	for current := dir; ; current = filepath.Dir(current) {
		candidate := filepath.Join(current, "go.work")
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			workspace = candidate
			break
		}
		if current == root || current == filepath.Dir(current) {
			break
		}
	}
	return append(env, "GOWORK="+workspace, "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", "GOFLAGS=", "GOENV=off", "GONOPROXY=none", "GOPRIVATE=")
}

func (s *sources) applyBuildModel(root string, model packageModel) bool {
	byImport := map[string][]component{}
	active := map[string]bool{}
	members := map[string]component{}
	for _, item := range s.components {
		members[item.Directory+"\x00"+item.Name] = item
	}
	for _, pkg := range model.Packages {
		dir, err := filepath.Rel(root, pkg.Dir)
		if err != nil || !filepath.IsLocal(dir) {
			continue
		}
		dir = filepath.ToSlash(dir)
		if owner, ok := members[dir+"\x00"+pkg.Name]; ok && pkg.ImportPath != "" {
			owners := byImport[pkg.ImportPath]
			if !slices.Contains(owners, owner) {
				byImport[pkg.ImportPath] = append(owners, owner)
			}
		}
		for _, group := range [][]string{pkg.GoFiles, pkg.CgoFiles, pkg.TestGoFiles, pkg.XTestGoFiles} {
			for _, name := range group {
				if filepath.IsLocal(name) {
					active[filepath.ToSlash(filepath.Join(dir, name))] = true
				}
			}
		}
	}
	if len(active) == 0 {
		return false
	}
	for i := range s.files {
		file := &s.files[i].fact
		if active[file.Path] || model.Status == "resolved" {
			file.InBuild = active[file.Path]
		}
	}
	s.byImport = byImport
	s.allowLocalModuleImports = true
	s.imports = s.imports[:0]
	s.links = s.links[:0]
	s.collectImports()
	return true
}
