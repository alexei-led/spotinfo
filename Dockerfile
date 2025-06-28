# syntax = docker/dockerfile:experimental

#
# ----- Go Builder Image ------
#
FROM --platform=${BUILDPLATFORM} golang:1.24-alpine AS builder

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
RUN --mount=type=cache,target=/go/mod go mod download

# copy sources
COPY . .

# test and build
RUN --mount=type=cache,target=/root/.cache/go-build TARGETOS=${TARGETOS} TARGETARCH=${TARGETARCH} make build



#
# ------ spotinfo release Docker image ------
#
FROM scratch

# copy CA certificates
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

# this is the last command since it's never cached
COPY --from=build /go/src/app/.bin/spotinfo /spotinfo

ENTRYPOINT ["/spotinfo"]