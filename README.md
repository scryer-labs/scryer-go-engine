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

## Release verification

The [GitHub workflow](.github/workflows/ci.yml) builds the platform packages
and exercises a scan through the ABI before publishing a version tag.
