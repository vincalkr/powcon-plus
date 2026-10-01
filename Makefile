# powcon-plus — build, test, install
#
#   make            bin/powcon-plus (no cgo) + bin/powcon-plus-gui (Fyne)
#   make test       unit tests + vet
#   make install    copy to ~/.local/bin and write the XDG autostart entry
#
# In an environment without write access to GOPATH/GOCACHE (e.g. an agent
# sandbox):
#   . hack/env.sh && make

GO      ?= go
PKG     := ./cmd/powcon-plus
BIN     := bin
HEADLESS := $(BIN)/powcon-plus
GUI      := $(BIN)/powcon-plus-gui
PREFIX  ?= $(HOME)/.local

.PHONY: all build gui test vet fmt tidy install uninstall install-autostart uninstall-autostart run clean

all: build gui

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-s -w" -o $(HEADLESS) $(PKG)

gui:
	$(GO) build -trimpath -tags gui -o $(GUI) $(PKG)

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...
	$(GO) vet -tags gui ./...

fmt:
	@test -z "$$(gofmt -l .)" || (gofmt -l . && echo "run: gofmt -w ."; exit 1)

tidy:
	$(GO) mod tidy

install: all
	mkdir -p $(PREFIX)/bin
	install -m 755 $(HEADLESS) $(PREFIX)/bin/powcon-plus
	install -m 755 $(GUI) $(PREFIX)/bin/powcon-plus-gui
	@echo "installed to $(PREFIX)/bin (make sure it is on PATH)"

uninstall:
	rm -f $(PREFIX)/bin/powcon-plus $(PREFIX)/bin/powcon-plus-gui

install-autostart:
	$(PREFIX)/bin/powcon-plus install-autostart --mode xdg

uninstall-autostart:
	$(PREFIX)/bin/powcon-plus uninstall-autostart --mode xdg

run:
	$(GO) run $(PKG) run --verbose

clean:
	rm -rf $(BIN)
