package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/scryer-labs/scryer-go-engine/internal/analysis"
	"github.com/scryer-labs/scryer-go-engine/internal/golang"
)

const (
	MaxRequestBytes   = 1 << 20
	MaxRequestIDBytes = 256
)

type Request struct {
	ProtocolVersion int                      `json:"protocolVersion"`
	ID              string                   `json:"requestId"`
	Stack           string                   `json:"stack"`
	Operation       string                   `json:"operation"`
	Coverage        analysis.CoverageRequest `json:"coverage"`
	Impact          struct {
		Before, After, BeforeRoot, AfterRoot string
		Resolve                              bool
	} `json:"impact"`
	Scan struct {
		Repository string   `json:"repository"`
		Static     *bool    `json:"static"`
		Facts      []string `json:"facts"`
	} `json:"scan"`
}

func ParseRequest(data []byte) (Request, error) {
	var request Request
	if err := json.Unmarshal(data, &request); err != nil || request.ID == "" || len(request.ID) > MaxRequestIDBytes {
		return Request{}, errors.New("invalid request JSON or requestId")
	}
	return request, nil
}

type protocolError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type report struct {
	SchemaVersion int            `json:"schemaVersion"`
	Root          string         `json:"root"`
	Facts         map[string]any `json:"facts"`
}

func (r Request) Execute(ctx context.Context) ([]byte, error) {
	var result any
	var failure *protocolError
	if r.ProtocolVersion != 1 || r.Stack != "go" {
		failure = &protocolError{"INVALID_REQUEST", "unsupported protocol version or stack"}
	} else {
		switch r.Operation {
		case "scan":
			result, failure = r.scan(ctx)
		case "coverage":
			if !filepath.IsAbs(r.Coverage.Repository) {
				failure = &protocolError{"INVALID_REQUEST", "coverage requires an absolute repository"}
				break
			}
			value, err := golang.Coverage(ctx, r.Coverage.Repository, r.Coverage)
			if err != nil {
				failure = &protocolError{"GO_COVERAGE_ERROR", err.Error()}
			} else {
				result = value
			}
		case "impact":
			before, err := golang.Impact(ctx, r.Impact.BeforeRoot, r.Impact.Resolve)
			if err != nil {
				failure = &protocolError{"GO_IMPACT_ERROR", "before: " + err.Error()}
				break
			}
			after, err := golang.Impact(ctx, r.Impact.AfterRoot, r.Impact.Resolve)
			if err != nil {
				failure = &protocolError{"GO_IMPACT_ERROR", "after: " + err.Error()}
				break
			}
			result = analysis.ImpactFacts{SchemaVersion: 1, BeforeSHA: r.Impact.Before, AfterSHA: r.Impact.After, Before: before, After: after}
		default:
			failure = &protocolError{"UNSUPPORTED_OPERATION", "Go engine does not support operation " + r.Operation}
		}
	}
	return json.Marshal(struct {
		ProtocolVersion int            `json:"protocolVersion"`
		ID              string         `json:"requestId"`
		Stack           string         `json:"stack"`
		Operation       string         `json:"operation"`
		Report          any            `json:"report"`
		Error           *protocolError `json:"error"`
	}{1, r.ID, "go", r.Operation, result, failure})
}

func (r Request) scan(ctx context.Context) (*report, *protocolError) {
	if r.ProtocolVersion != 1 || r.Stack != "go" {
		return nil, &protocolError{"INVALID_REQUEST", "unsupported protocol version or stack"}
	}
	if r.Operation != "scan" {
		return nil, &protocolError{"UNSUPPORTED_OPERATION", "Go engine does not support operation " + r.Operation}
	}
	selected := r.Scan.Facts
	if len(selected) == 0 {
		selected = []string{"dependencies"}
	}
	for _, fact := range selected {
		if !golang.Supports(fact) {
			return nil, &protocolError{"UNSUPPORTED_FACT", "Go engine does not support " + fact}
		}
	}
	root := r.Scan.Repository
	if !filepath.IsAbs(root) {
		return nil, &protocolError{"INVALID_REQUEST", "scan requires an absolute repository directory"}
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return nil, &protocolError{"INVALID_REQUEST", "scan repository must be an existing directory"}
	}
	result, err := golang.Scan(ctx, root, selected, r.Scan.Static != nil && !*r.Scan.Static)
	if err != nil {
		return nil, &protocolError{"GO_SCAN_ERROR", err.Error()}
	}
	return &report{1, root, result}, nil
}
