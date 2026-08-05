# syntax=docker/dockerfile:1

#
# ----- Go Builder Image ------
#
FROM --platform=${BUILDPLATFORM} golang:1.26-alpine AS builder

# Only install essential tools for building
RUN apk add --no-cache git make ca-certificates

#
# ----- Build and Test Image -----
#
FROM --platform=${BUILDPLATFORM} builder AS build

# passed by buildkit
ARG TARGETOS
ARG TARGETARCH

# set working directory
RUN mkdir -p /go/src/app
WORKDIR /go/src/app

# load dependency
COPY go.mod .
COPY go.sum .
RUN --mount=type=cache,target=/go/pkg/mod go mod download

# copy sources
COPY . .

# Build for the target platform. GOOS/GOARCH, not TARGETOS/TARGETARCH: the
# Makefile's build target passes neither to `go build`, so exporting the buildkit
# names produced a host-architecture binary on every cross-build.
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    GOOS=${TARGETOS} GOARCH=${TARGETARCH} make build



#
# ------ spotinfo release Docker image ------
#
FROM scratch

# copy CA certificates
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

# this is the last command since it's never cached
COPY --from=build /go/src/app/.bin/spotinfo /spotinfo

ENTRYPOINT ["/spotinfo"]