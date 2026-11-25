# Scryer Go Engine

An Apache-2.0 Scryer engine for Go source facts and optional build-aware
resolution. The packages expose Scryer's C ABI.

## Install and scan

With a current Scryer CLI, install or upgrade the official macOS arm64 or
Windows amd64 package:

```sh
scryer engine install go
scryer engine upgrade go
scryer project scan /path/to/project --facts dependencies,architecture,files,call-graph --json --compact
```

For engine development, build with the Go version in `go.mod`, cgo and a C
compiler. Install the resulting local package into Scryer:

```sh
./scripts/build
scryer engine install go --from "$PWD/build/install/scryer-go-engine"
```

## Facts

The scan reads files with Go's parser and checks checkout-local packages with
`go/types`. See the [Go facts contract](docs/FACTS.md) for payload fields,
source evidence and limitations. Add `--resolve` for Go-toolchain-backed
file and call bindings. This mode requires an available Go toolchain and
records partial or fallback evidence when the project model cannot be loaded.

## Coverage and change impact

With a current Core CLI and this engine:

```sh
scryer project analyze --operations coverage -o coverage.html
scryer project analyze --resolve --operations impact,coverage -o impact.html
scryer project analyze --json --compact
```

Impact defaults to static scan evidence; `--resolve` also runs native tests and
collects after-snapshot coverage. Failed dynamic resolution/tests/coverage stop
analysis without static fallback. Core owns snapshot comparison, traversal and
report rendering; the engine supplies shared scan projections and normalized
JSON coverage. Multi-engine projects use the same command; CMake target selectors
belong to C++ and are not interpreted here.

Requires an installed Go toolchain and offline-resolvable dependencies. Coverage
runs `go test -mod=readonly -count=1` per discovered module, with project-module
coverage instrumentation. Import supports Go set/count/atomic coverprofiles.
Dynamic impact reuses scan's go-list build selection and local bindings;
interface dispatch remains a boundary. Test files are not instrumented, and
there is no individual-test attribution.

Native runtime acceptance currently targets macOS. Windows package builds and
ABI smoke checks do not establish equivalent runtime toolchain acceptance.
Coverage is aggregate execution evidence, not assertion effectiveness or a
causal test-to-caller path. The common JSON contract is maintained in Scryer
Core's `docs/contracts/COVERAGE.md` and `docs/contracts/IMPACT.md`.

## Known gaps

- Dependency facts describe declarations; effective selected/transitive versions
  and remote version metadata are not collected.
- Static imports/build replacements can remain candidates. Dynamic Go build
  selection refines local joins, but repository-external types, interface dispatch,
  native/cgo targets and runtime-loaded code are not fully bound.
- IPC/REST communication facts are not implemented.
- Coverage has no per-test attribution or branch-adequacy evidence. Test files
  are not instrumented. Test effectiveness and code-smell rules are not implemented.
- Native Windows runtime analysis acceptance is deferred; package/ABI checks
  are separate from a target project's build and test success.

## Release verification

The [GitHub workflow](.github/workflows/ci.yml) builds the platform packages
and exercises scan, static impact and coverage import through the ABI before publishing a version tag.

## Contributor workflow

Start with [AGENTS.md](AGENTS.md) and [working conventions](docs/WORKING-NOTES.md)
for repository-specific analysis, coding, independent review and Orrery acceptance.
