package golang

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/scryer-labs/scryer-go-engine/internal/analysis"
)

func Coverage(ctx context.Context, root string, request analysis.CoverageRequest) (analysis.CoverageReport, error) {
	result := analysis.CoverageReport{SchemaVersion: 1, Root: root, Engine: "go", Status: "unavailable", Runs: []analysis.CoverageRun{}}
	if len(request.TestTargets) > 0 || len(request.TestBinaries) > 0 {
		return result, fmt.Errorf("Go coverage uses go test ./...; CMake target and binary selection belong to other engines")
	}
	p := project{filesystem: os.DirFS(root)}
	if err := p.discover(ctx); err != nil {
		return result, err
	}
	if len(p.manifests) == 0 {
		return result, fmt.Errorf("no go.mod found for coverage")
	}
	bounded, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	files := map[string]*analysis.CoverageFile{}
	var artifacts []byte
	if request.Artifact != "" {
		data, err := os.ReadFile(request.Artifact)
		if err != nil {
			return result, err
		}
		if err = readProfile(root, p.manifests, data, files); err != nil {
			return result, err
		}
		artifacts = data
	} else {
		work, err := os.MkdirTemp("", "scryer-go-coverage-")
		if err != nil {
			return result, err
		}
		defer os.RemoveAll(work)
		result.Status = "collected"
		for i, manifest := range p.manifests {
			dir := filepath.Join(root, filepath.Dir(manifest.File))
			profile := filepath.Join(work, fmt.Sprintf("%d.out", i))
			patterns := make([]string, 0, len(p.manifests))
			for _, m := range p.manifests {
				patterns = append(patterns, m.Module+"/...")
			}
			args := []string{"test", "-mod=readonly", "-count=1", "-covermode=count", "-coverpkg=" + strings.Join(patterns, ","), "-coverprofile=" + profile, "./..."}
			env := map[string]string{}
			for _, value := range goListEnv(root, dir) {
				key, v, ok := strings.Cut(value, "=")
				if ok {
					env[key] = v
				}
			}
			_, failure := analysis.Command(bounded, dir, env, "go", args...)
			if bounded.Err() != nil {
				return result, bounded.Err()
			}
			run := analysis.CoverageRun{Command: append([]string{"go"}, args...), Directory: filepath.ToSlash(filepath.Dir(manifest.File)), Configuration: map[string]string{"module": manifest.Module, "GOWORK": env["GOWORK"]}, Name: "go test ./...: " + manifest.File, Status: "succeeded"}
			data, err := os.ReadFile(profile)
			if err != nil {
				run.Status = "no-profile"
				run.Message = err.Error()
				result.Status = "partial"
			} else {
				if err = readProfile(root, p.manifests, data, files); err != nil {
					return result, err
				}
				artifacts = append(artifacts, data...)
				run.Profiles = 1
			}
			if failure != nil {
				run.Status = "failed"
				run.Message = failure.Error()
				result.Status = "partial"
			}
			result.Runs = append(result.Runs, run)
		}
	}
	if len(files) == 0 {
		result.Status = "unavailable"
		result.Message = "No coverage blocks mapped to this project."
		return result, nil
	}
	if result.Status == "unavailable" {
		result.Status = "collected"
	}
	artifact := request.Artifact
	if artifact == "" {
		artifact = "go-coverprofile.out"
	}
	evidence := analysis.CoverageEvidence{Format: "go-coverprofile", Version: "1", Artifact: artifact, SHA256: analysis.Hash(artifacts), SourceMatch: "unverified", Granularity: "go-statement-blocks", Files: analysis.FinalizeFiles(files), Notes: []string{
		"Aggregate Go statement-block counts. Test files are not instrumented; individual-test attribution and branch adequacy are unavailable.",
		"Generated profiles use go test -mod=readonly -count=1 -covermode=count for each discovered module, with -coverpkg selecting all discovered project module patterns, with the project's Go workspace selection.",
	}}
	raw, err := json.Marshal(evidence)
	result.Coverage = raw
	return result, err
}

var profileBlock = regexp.MustCompile(`^(.+):([0-9]+)\.([0-9]+),([0-9]+)\.([0-9]+) ([0-9]+) ([0-9]+)$`)

func readProfile(root string, manifests []Manifest, data []byte, files map[string]*analysis.CoverageFile) error {
	scan := bufio.NewScanner(strings.NewReader(string(data)))
	scan.Buffer(make([]byte, 4096), 1<<20)
	if !scan.Scan() || !strings.HasPrefix(scan.Text(), "mode: ") {
		return fmt.Errorf("invalid Go coverage profile")
	}
	mode := strings.TrimPrefix(scan.Text(), "mode: ")
	if mode != "set" && mode != "count" && mode != "atomic" {
		return fmt.Errorf("unsupported Go cover mode %q", mode)
	}
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		if line == "" {
			continue
		}
		m := profileBlock.FindStringSubmatch(line)
		if m == nil {
			return fmt.Errorf("invalid Go coverage block %q", line)
		}
		source := m[1]
		relative := ""
		if filepath.IsAbs(source) {
			if rel, err := filepath.Rel(root, source); err == nil && filepath.IsLocal(rel) {
				relative = filepath.ToSlash(rel)
			}
		} else {
			longest := 0
			for _, manifest := range manifests {
				prefix := manifest.Module + "/"
				if strings.HasPrefix(source, prefix) && len(prefix) > longest {
					relative = filepath.ToSlash(filepath.Join(filepath.Dir(manifest.File), strings.TrimPrefix(source, prefix)))
					longest = len(prefix)
				}
			}
		}
		if relative == "" || !filepath.IsLocal(relative) {
			continue
		}
		if info, err := os.Stat(filepath.Join(root, filepath.FromSlash(relative))); err != nil || !info.Mode().IsRegular() {
			continue
		}
		number := func(i int) int { v, _ := strconv.Atoi(m[i]); return v }
		region := analysis.CoverageRegion{Start: analysis.ImpactPosition{Line: number(2), Column: number(3)}, End: analysis.ImpactPosition{Line: number(4), Column: number(5)}}
		count, err := strconv.ParseUint(m[7], 10, 64)
		if err != nil {
			return err
		}
		region.Count = count
		if region.Start.Line < 1 || region.Start.Column < 1 || region.End.Line < region.Start.Line || region.End.Column < 1 {
			return fmt.Errorf("invalid source range in Go profile")
		}
		if files[relative] == nil {
			files[relative] = &analysis.CoverageFile{Path: relative, Regions: []analysis.CoverageRegion{}}
		}
		file := files[relative]
		merged := false
		for i, existing := range file.Regions {
			if existing.Start == region.Start && existing.End == region.End {
				file.Regions[i].Count += region.Count
				merged = true
				break
			}
		}
		if !merged {
			file.Regions = append(file.Regions, region)
		}
	}
	return scan.Err()
}
