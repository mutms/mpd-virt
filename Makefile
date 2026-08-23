# mpd-virt is a single Go binary, built from go/ into bin/mpd-virt and
# installed to $(HOME)/.local/bin/mpd-virt.
GO_DIR := $(CURDIR)/go

PREFIX ?= $(HOME)/.local
BINDIR ?= $(PREFIX)/bin
BIN    := $(BINDIR)/mpd-virt

# The `go` directive in go/go.mod picks the compiler: the go command
# fetches that toolchain itself when the installed one is older
# (GOTOOLCHAIN=auto, the default). Bump the directive to move to a newer Go.

.PHONY: build install uninstall build-static clean test vet fmt fmt-check tidy lint-shell fmt-shell

# Stamped into `mpd-virt --version`; "dev" outside a git checkout, the
# commit hash before any tag exists. Release builds are made AFTER
# tagging so the tag lands here.
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

build:
	@mkdir -p bin
	cd $(GO_DIR) && go build -ldflags "$(LDFLAGS)" -o $(CURDIR)/bin/mpd-virt ./cmd/mpd-virt
	@echo "Native binary: bin/mpd-virt"

# Self-contained binaries for GitHub releases: Linux amd64 + arm64 and
# macOS arm64. The version is part of the file name, so dist/ is cleared
# first — otherwise binaries of older versions would pile up next to the
# new ones, waiting to be uploaded by mistake. The macOS one is ad-hoc
# signed by the Go linker and runs as downloaded with curl; a browser
# download needs `xattr -d com.apple.quarantine` once (Gatekeeper).
build-static:
	rm -rf $(CURDIR)/dist
	cd $(GO_DIR) && CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o $(CURDIR)/dist/mpd-virt-$(VERSION)-macos-arm64 ./cmd/mpd-virt
	cd $(GO_DIR) && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o $(CURDIR)/dist/mpd-virt-$(VERSION)-linux-amd64 ./cmd/mpd-virt
	cd $(GO_DIR) && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o $(CURDIR)/dist/mpd-virt-$(VERSION)-linux-arm64 ./cmd/mpd-virt

install: build
	@mkdir -p "$(BINDIR)"
	@install "$(CURDIR)/bin/mpd-virt" "$(BIN)"
	@BINDIR="$(BINDIR)" BIN="$(BIN)" sh "$(CURDIR)/scripts/install-message.sh"

uninstall:
	@rm -f "$(BIN)"
	@echo "Removed: $(BIN)"

test:
	cd $(GO_DIR) && go test ./...

vet:
	cd $(GO_DIR) && go vet ./...

# Apply canonical Go formatting.
fmt:
	cd $(GO_DIR) && gofmt -w .

# Fail if anything is not gofmt-clean (for CI / pre-commit).
fmt-check:
	@out=$$(cd $(GO_DIR) && gofmt -l .); \
	if [ -n "$$out" ]; then echo "not gofmt-clean:"; echo "$$out"; exit 1; fi
	@echo "gofmt clean"

tidy:
	cd $(GO_DIR) && go mod tidy

# Shell scripts this repo ships: the installer and container entrypoints.
# Identified by content, since some are extensionless.
SHELL_FILES = $$(find scripts container -type f -exec file --mime-type {} + 2>/dev/null | grep x-shellscript | cut -d: -f1)

lint-shell:
	@shellcheck -S warning $(SHELL_FILES) && echo "shellcheck clean"

fmt-shell:
	@shfmt -w -i 4 $(SHELL_FILES) && echo "shfmt applied"

clean:
	rm -rf bin dist
	cd $(GO_DIR) && go clean -cache -testcache
