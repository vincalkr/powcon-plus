# powcon-plus — build, test, install
#
#   make            show this help (no arguments)
#   make all        bin/powcon-plus (no cgo) + bin/powcon-plus-gui (Fyne)
#   make test       unit tests (headless and gui tag sets)
#   make check      verify the system dependencies
#   make install    verify the requirements, then copy the binaries to ~/.local/bin
#
# In an environment without write access to GOPATH/GOCACHE (e.g. an agent
# sandbox):
#   . hack/env.sh && make <target>

GO      ?= go
PKG     := ./cmd/powcon-plus
BIN     := bin
HEADLESS := $(BIN)/powcon-plus
GUI      := $(BIN)/powcon-plus-gui
PREFIX  ?= $(HOME)/.local

.PHONY: help all build gui test vet fmt tidy check install uninstall install-autostart uninstall-autostart run clean

# `make` with no arguments shows the help (first target = default).
help:
	@echo "powcon-plus — available targets:"
	@echo
	@echo "  make                    this help"
	@echo "  make all                build the headless and the GUI binaries (default build)"
	@echo "  make build              headless binary only (no cgo)"
	@echo "  make gui                GUI binary (Fyne, -tags gui)"
	@echo "  make test               run the unit tests"
	@echo "  make vet                go vet (both build tag sets)"
	@echo "  make fmt                check gofmt (prints unformatted files)"
	@echo "  make tidy               go mod tidy"
	@echo "  make check              verify the system dependencies (sensor, session bus, tray, libraries)"
	@echo "  make install            build, verify the requirements, then copy to $(PREFIX)/bin"
	@echo "                          (SKIP_CHECK=1 copies even if a requirement is missing)"
	@echo "  make uninstall          remove the binaries from $(PREFIX)/bin"
	@echo "  make install-autostart  write the XDG autostart entry"
	@echo "  make uninstall-autostart remove the XDG autostart entry"
	@echo "  make run                run the daemon in verbose mode (go run)"
	@echo "  make clean              remove bin/"
	@echo
	@echo "Tip: without write access to GOPATH/GOCACHE run first: . hack/env.sh"

all: build gui

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-s -w" -o $(HEADLESS) $(PKG)

gui:
	$(GO) build -trimpath -tags gui -ldflags "-s -w" -o $(GUI) $(PKG)

test:
	$(GO) test ./...
	$(GO) test -tags gui ./...

vet:
	$(GO) vet ./...
	$(GO) vet -tags gui ./...

fmt:
	@test -z "$$(gofmt -l cmd internal)" || (gofmt -l cmd internal && echo "run: gofmt -w cmd internal"; exit 1)

tidy:
	$(GO) mod tidy

# The dependency check runs against the freshly built binary. install refuses
# to copy anything when an essential requirement is missing; SKIP_CHECK=1
# bypasses it (cross installs, machines without the PSU attached).
check: all
	$(HEADLESS) check

install: all
ifeq ($(SKIP_CHECK),)
	@$(HEADLESS) check || { echo "install aborted: the requirements above are missing (SKIP_CHECK=1 to install anyway)"; exit 1; }
else
	@echo "skipping the dependency check (SKIP_CHECK=$(SKIP_CHECK))"
endif
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
