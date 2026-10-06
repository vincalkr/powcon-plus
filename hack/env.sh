# Build in an environment without write access to GOPATH (e.g. an agent sandbox).
# In a normal terminal this is usually NOT needed: Go uses the default GOMODCACHE/GOCACHE.
export GOFLAGS=-mod=mod
export GOPATH="${PWD}/.cache/gopath"
export GOMODCACHE="${GOPATH}/pkg/mod"
export GOCACHE="${PWD}/.cache/build"
mkdir -p "$GOMODCACHE" "$GOCACHE"
