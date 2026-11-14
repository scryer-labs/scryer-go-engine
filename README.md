# Scryer Go Engine

An Apache-2.0 Scryer engine for static Go source facts. The macOS development
package exposes Scryer's C ABI.

```sh
./scripts/build
scryer engine install go --from build/install/scryer-go-engine
scryer project scan /path/to/project --facts dependencies --json --compact
```
