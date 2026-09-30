# powcon-plus — build, test, installazione
#
#   make            bin/powcon-plus (senza cgo) + bin/powcon-plus-gui (Fyne)
#   make test       unit test + vet
#   make install    copia in ~/.local/bin e scrive l'autostart XDG
#
# In un ambiente senza scrittura su GOPATH/GOCACHE (es. sandbox dell'agente):
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
	@test -z "$$(gofmt -l .)" || (gofmt -l . && echo "eseguire: gofmt -w ."; exit 1)

tidy:
	$(GO) mod tidy

install: all
	mkdir -p $(PREFIX)/bin
	install -m 755 $(HEADLESS) $(PREFIX)/bin/powcon-plus
	install -m 755 $(GUI) $(PREFIX)/bin/powcon-plus-gui
	@echo "installato in $(PREFIX)/bin (verificare che sia in PATH)"

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
