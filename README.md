[![build](https://github.com/doitintl/spotinfo/workflows/docker/badge.svg)](https://github.com/doitintl/spotinfo/actions?query=workflow%3A"docker") [![Go Report Card](https://goreportcard.com/badge/github.com/alexei-led/spotinfo)](https://goreportcard.com/report/github.com/alexei-led/spotinfo) [![Docker Pulls](https://img.shields.io/docker/pulls/alexeiled/spotinfo.svg?style=popout)](https://hub.docker.com/r/alexeiled/spotinfo) [![](https://images.microbadger.com/badges/image/alexeiled/spotinfo.svg)](https://microbadger.com/images/alexeiled/spotinfo "Get your own image badge on microbadger.com") 

# spotinfo

The `spotinfo` is a bootstrap project for Go CLI application.

## Docker

The `spotinfo` uses Docker both as a CI tool and for releasing the final `spotinfo` Multi-Architecture Docker image (`scratch` with updated `ca-credentials` package).

## Makefile

The `spotinfo` `Makefile` is used for task automation only: compile, lint, test, etc.
The project requires Go version 1.16.

Specify Go version with `GO` variable:

```sh
make GO=go1.16rc1
```

## Continuous Integration

The GitHub action `docker` is used for the `spotinfo` CI.

## Build with Docker

Use Docker `buildx` plugin to build multi-architecture Docker image.

```sh
docker buildx build --platform=linux/arm64,linux/amd64 -t spotinfo -f Dockerfile .
```

### Required GitHub secrets

Please specify the following GitHub secrets:

1. `DOCKER_USERNAME` - Docker Registry username
1. `DOCKER_PASSWORD` - Docker Registry password or token
1. `CR_PAT` - Current GitHub Personal Access Token (with `write/read` packages permission)
1. `DOCKER_REGISTRY` - _optional_; Docker Registry name, default to `docker.io`
1. `DOCKER_REPOSITORY` - _optional_; Docker image repository name, default to `$GITHUB_REPOSITORY` (i.e. `user/repo`)
