# Build in ambiente senza scrittura su GOPATH (es. sandbox agente).
# In un terminale normale di norma NON serve: Go usa GOMODCACHE/GOCACHE di default.
export GOFLAGS=-mod=mod
export GOPATH="${PWD}/.cache/gopath"
export GOMODCACHE="${GOPATH}/pkg/mod"
export GOCACHE="${PWD}/.cache/build"
mkdir -p "$GOMODCACHE" "$GOCACHE"
