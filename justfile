set shell := ["bash", "-eu", "-o", "pipefail", "-c"]

binary := "reynholm"
image  := "reynholm:dev"
config := "test.yaml"

default:
    @just --list

# Compile a static binary into ./bin/
build:
    mkdir -p bin
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=dev -X main.commit=$(git rev-parse --short HEAD)" -o bin/{{binary}} .

# Run the full Go test suite with race detector and coverage.
test:
    go test -race -count=1 -covermode=atomic ./...

# Run the CLI against a local config (defaults to test.yaml, dry-run on).
run config=config:
    go run . --config {{config}} --dry-run

# Build the distroless container image.
docker:
    docker build -t {{image}} .

# Tidy modules.
tidy:
    go mod tidy

# Run go vet
vet:
    go vet ./...

# Run golangci-lint
lint:
    golangci-lint run ./...

# Run govulncheck
vulncheck:
    govulncheck ./...

# Run all quality gates
check: lint vet test vulncheck

# Test release locally (no publish)
release-dry-run:
    goreleaser release --snapshot --clean

# Remove build artifacts.
clean:
    rm -rf bin dist
