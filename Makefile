# Version is derived from the git tag — `git describe` gives v0.1.0 at the tag
# and v0.1.0-3-gabc1234 three commits later. There is no version constant to
# hand-edit; `git tag vX.Y.Z` is the single source of truth.
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: install build test lint fmt version

# Install to $GOBIN/$GOPATH/bin with the version baked in.
install:
	go install -ldflags "$(LDFLAGS)" ./cmd/gusset

# Build a local ./gusset binary with the version baked in.
build:
	go build -ldflags "$(LDFLAGS)" -o gusset ./cmd/gusset

test:
	go test -race ./...

lint:
	golangci-lint run

# Format edited files in place (scoped per house style; CI checks gofmt -l).
fmt:
	gofmt -w .

# Print the version that a build would stamp.
version:
	@echo $(VERSION)
