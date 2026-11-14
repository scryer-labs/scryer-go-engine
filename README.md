# Scryer Go Engine

An Apache-2.0 Scryer engine for static Go source facts. The macOS development
package exposes Scryer's C ABI.

```sh
./scripts/build
scryer engine install go --from build/install/scryer-go-engine
scryer project scan /path/to/project --facts dependencies,architecture,files,call-graph --json --compact
```


The scan reads files with Go's parser and checks checkout-local packages with
`go/types`. See the [Go facts contract](docs/FACTS.md) for payload fields,
source evidence and limitations.
