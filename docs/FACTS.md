# Go engine project facts

The engine ID is `go`. Its ABI v1 scan response uses the common
`{schemaVersion, root, facts}` report. The Scryer project report exposes
selected outcomes at `engineFacts[]`. Read the entry with `engine: "go"`,
the desired `fact`, and `status: "collected"`. In a workspace report this
array belongs to each member at `projects[].facts.engineFacts`. Version 1
fields are additive and array ordering carries no domain meaning.

| Fact | Payload |
| --- | --- |
| `dependencies` | `manifests[]` describes `go.mod` modules and their declared requirements, exclusions and replacements. `workspaces[]` describes `go.work` uses and replacements. A declared version is the text of a requirement, without an effective selected version. |
| `architecture` | `components[]` contains Go package declarations with `componentId`, `name`, `kind`, `file`, `directory` and optional `importPath` and `manifest`. Kinds are `package`, `command` and `external-test-package`. `links[]` contains import sites with `fromComponentId`, `toComponentId`, original import path and resolution. |
| `files` | `files[]` includes repository-relative `path`, package name, `componentIds` and `inBuild`. Optional `syntaxErrors` and `buildError` indicate incomplete source evidence. `imports[]` retains original paths, aliases when explicit, and source locations, including unresolved imports. |
| `call-graph` | `symbols[]` contains functions, methods and closures. Join `calls[].callerSymbolId` and `targetSymbolId` to `symbols[].symbolId`. Both arrays include `componentId` and source ranges. File paths, component IDs and symbol IDs are scoped to the project and engine. |

An import into a uniquely identified package in the same module can be
`resolved`. A matching package in a separate checkout-local module is
`local-module-candidate`. That status does not establish the effective
`go.work` or replacement selection. Imports with multiple visible source
candidates are `ambiguous`, the cgo pseudo-package is `cgo`, and other
imports can be `unresolved`.

A call includes `callSiteId`, the source expression in `callee`, `site`
and `resolution`. Its target ID appears on resolved edges only. A call
at package initialization may lack a caller ID. Unresolved calls retain
`resolutionReason`, such as `dynamic-method-dispatch`, `indirect-call`,
`binding-unavailable`, `file-not-type-checked` or `native-symbol-resolution`.
`builtin` and `declaration-outside-local-source` also record calls with
no checkout-local destination. Locations use one-based lines and byte
columns and an exclusive end. IDs are intended for joins within one scan.

The call graph records `goos`, `goarch` and `cgoEnabled` for the selected
build context. Inactive files still contribute syntax observations.
`testOnly: true` means the file or source range is within a Go `*_test.go`
file. An internal test file can share a component with implementation files.
The marker applies to the individual evidence and does not classify a whole
mixed component. Calls and symbols from files with syntax or build-selection
problems may still be present as source evidence.

The scan does not build or run the project, read the module cache for type
information, or resolve runtime dispatch. `limitations[]` describes gaps
in collection. cgo uses the Go type checker with an incomplete native import.
Native calls and lookups retain source evidence without creating concrete
Go target IDs or workspace edges. The system's engine inventory and ABI
symbol name coincidences supply no evidence about dependencies in the target.

The installed Go engine advertises `scan` for these four facts, plus separate
`coverage` and `impact` operations. Runtime profiles and impact projections use
the shared Core analysis contract. IPC and effective dependency resolution
remain outside its capabilities.

## Toolchain-assisted resolution

`--facts files,call-graph --resolve` adds a `semantic` object to the selected
fact payloads. Its `basis` is `go-list`, and `status` is `resolved`, `partial`
or `fallback`. `message`, when present, retains the available diagnostic.
The engine loads the package model without downloading dependencies and uses
it to refine build membership and checkout-local type joins, including supported
`go.work` and local module replacements. Existing IDs still refer to the same
source locations. Interface dispatch and declarations outside checkout source
retain unresolved call sites. The `dependencies` fact remains declaration-based.
