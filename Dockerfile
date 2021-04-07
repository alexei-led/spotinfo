# syntax = docker/dockerfile:experimental

#
# ----- Go Builder Image ------
#
FROM --platform=${BUILDPLATFORM} golang:1.16-alpine AS builder

# curl git bash
RUN apk add --no-cache curl git bash make sed ca-certificates file

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
RUN --mount=type=cache,target=/go/mod go mod download

# copy sources
COPY . .

# test and build
RUN --mount=type=cache,target=/root/.cache/go-build TARGETOS=${TARGETOS} TARGETARCH=${TARGETARCH} make

#
# ------ spotinfo GitHub Release
#
FROM --platform=${BUILDPLATFORM} build as github-release

# build argument to secify if to create a GitHub release
ARG RELEASE=false
# Release Tag: `RELEASE_TAG=$(git describe --abbrev=0)`
ARG RELEASE_TAG
# release to GitHub; pass RELEASE_TOKEN ras build-arg
ARG RELEASE_TOKEN

# build spotinfo for all platforms and release to GitHub
RUN --mount=type=cache,target=/root/.cache/go-build if $RELEASE; then make github-release; fi


#
# ------ spotinfo release Docker image ------
#
FROM scratch

# copy CA certificates
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

# this is the last command since it's never cached
COPY --from=build /go/src/app/.bin/spotinfo /spotinfo

ENTRYPOINT ["/spotinfo"]