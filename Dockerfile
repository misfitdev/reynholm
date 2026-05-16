# syntax=docker/dockerfile:1.7

# Builder stage: compile a fully static binary so distroless/static is enough.
FROM golang:1.26-bookworm AS builder

WORKDIR /src

# Cache module downloads independently of source edits.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

COPY . .

ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG VERSION=dev
ARG COMMIT=unknown

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build \
        -trimpath \
        -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
        -o /out/reynholm \
        .

# Runtime stage: distroless static, nonroot UID/GID, no shell, no package manager.
FROM gcr.io/distroless/static-debian12:nonroot AS runtime

WORKDIR /app

COPY --from=builder /out/reynholm /app/reynholm

USER nonroot:nonroot

ENTRYPOINT ["/app/reynholm"]
