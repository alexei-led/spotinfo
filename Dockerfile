# syntax=docker/dockerfile:1

#
# ----- Go Builder Image ------
#
# Pinned to an explicit alpine minor so the toolchain does not shift underneath a
# rebuild, while still receiving patch updates within that line.
FROM --platform=${BUILDPLATFORM} golang:1.27-alpine3.24 AS builder

# make drives the build; ca-certificates is copied into the final scratch image.
# git is deliberately absent: .git is no longer in the build context, so there is
# nothing to stamp from and VERSION arrives as a build arg instead.
#
# Package versions intentionally unpinned: both are stable, neither ships in the
# final image (only the cert bundle is copied out), and pinning apk versions
# breaks the build on every alpine refresh. The base image pin is the meaningful
# one for reproducibility.
# hadolint ignore=DL3018
RUN apk add --no-cache make ca-certificates

#
# ----- Build and Test Image -----
#
FROM --platform=${BUILDPLATFORM} builder AS build

# passed by buildkit
ARG TARGETOS
ARG TARGETARCH

# Version stamps are passed in rather than derived from git. The .git directory is
# not in the build context (30MB of history that the image must never carry), so
# the Makefile's `git describe` cannot run here and would silently stamp v0.
# COMMIT and BRANCH must be passed for the same reason: left unset, the Makefile
# shells out to git, finds no binary, and stamps them empty. Empty defaults are
# deliberate — an argument-less `docker build` reports "dev" with no commit line
# rather than inventing one.
ARG VERSION=dev
ARG COMMIT=
ARG BRANCH=

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
    GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    make build VERSION="${VERSION}" COMMIT="${COMMIT}" BRANCH="${BRANCH}"



#
# ------ spotinfo release Docker image ------
#
FROM scratch

# The image holds exactly three things: the TLS trust store, /etc/passwd, and the
# binary. No shell, no package manager, no sources, no build cache.
#
# ca-certificates: the tool fetches the AWS feeds over HTTPS.
# passwd: with CGO_ENABLED=0 the AWS SDK resolves the shared-credentials path via
#   os.UserHomeDir() and falls back to user.Current(), which reads /etc/passwd.
#   Without it that lookup fails and the SDK searches a relative .aws/ path, so
#   mounted credentials would not be found. uid 65534 maps to home "/", making
#   the mount point /.aws.
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /etc/passwd /etc/passwd

# this is the last command since it's never cached
COPY --from=build /go/src/app/.bin/spotinfo /spotinfo

# The binary reads public feeds and optionally calls AWS; it needs no privileges
# and writes nothing. 65534 is nobody, present in the alpine passwd copied above.
USER 65534:65534

ENTRYPOINT ["/spotinfo"]