# Local build and install. Releases go through goreleaser.
PREFIX  ?= $(HOME)/.local
BIN     := $(PREFIX)/bin
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

.PHONY: build install uninstall test smoke

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o dist-local/meetstream ./cmd/meetstream

install: build
	mkdir -p $(BIN)
	install -m 0755 dist-local/meetstream $(BIN)/meetstream
	@echo "Installed $(BIN)/meetstream"
	@case ":$$PATH:" in *":$(BIN):"*) ;; *) echo "Add it to PATH:  export PATH=\"$(BIN):\$$PATH\"";; esac

uninstall:
	rm -f $(BIN)/meetstream

test:
	go vet ./... && go test ./...

# One-line check after install: version, setup and the key in use (no API calls that cost anything).
smoke:
	meetstream version && meetstream doctor --no-input; meetstream auth status
