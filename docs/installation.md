# Installation

spotinfo is one static binary. It carries its price snapshots inside itself, so it needs no
configuration, no credentials and — with `--offline` — no network.

Platforms: macOS, Linux and Windows, on AMD64 and ARM64.

## Homebrew (macOS and Linux)

```bash
brew install alexei-led/tap/spotinfo
```

To upgrade later:

```bash
brew upgrade spotinfo
```

## Release binary

Download the archive for your platform, unpack it, and put the binary on your `PATH`.

```bash
# Linux, AMD64
curl -L https://github.com/alexei-led/spotinfo/releases/latest/download/spotinfo_linux_amd64.tar.gz | tar xz
sudo install -m 0755 spotinfo /usr/local/bin/spotinfo
```

```bash
# macOS, Apple silicon
curl -L https://github.com/alexei-led/spotinfo/releases/latest/download/spotinfo_darwin_arm64.tar.gz | tar xz
sudo install -m 0755 spotinfo /usr/local/bin/spotinfo
```

Replace the platform in the file name with `linux_arm64`, `darwin_amd64` or `windows_amd64`.
Every release is listed at
[github.com/alexei-led/spotinfo/releases](https://github.com/alexei-led/spotinfo/releases).

If macOS refuses to open the binary, remove the quarantine attribute:

```bash
xattr -d com.apple.quarantine /usr/local/bin/spotinfo
```

## Docker

```bash
docker run --rm ghcr.io/alexei-led/spotinfo:latest \
  recommend --cloud azure --architecture arm64 --min-vcpu 4 --min-memory-gib 16
```

The image is built from `scratch` with CA certificates only, for linux/amd64 and
linux/arm64.

The image runs as UID 65534 and sets no `HOME`, so the AWS SDK cannot find `~/.aws`. Pass
credentials as environment variables:

```bash
docker run --rm \
  -e AWS_ACCESS_KEY_ID -e AWS_SECRET_ACCESS_KEY -e AWS_SESSION_TOKEN -e AWS_REGION \
  ghcr.io/alexei-led/spotinfo:latest \
  list --machine "m5.*" --region us-east-1 --with-score
```

To use a shared credentials file instead, mount it and name it. The file must be readable by
UID 65534:

```bash
docker run --rm -v ~/.aws:/aws:ro \
  -e AWS_SHARED_CREDENTIALS_FILE=/aws/credentials -e AWS_CONFIG_FILE=/aws/config \
  -e AWS_PROFILE ghcr.io/alexei-led/spotinfo:latest \
  list --machine "m5.*" --region us-east-1 --with-score
```

## Build from source

Go 1.26 or later, plus make.

```bash
git clone https://github.com/alexei-led/spotinfo.git
cd spotinfo
make build
```

The binary lands in `.bin/spotinfo`. The build is hermetic: it embeds the data that is
committed in the repository and downloads nothing.

## Make sure that the install works

```bash
spotinfo --version
spotinfo recommend --cloud azure --architecture arm64 --min-vcpu 2 --min-memory-gib 8
```

The second command needs no credentials and no network. If it prints three ranked machines,
the install is good:

```text
RANK  CLOUD  REGION        MACHINE            ARCHITECTURE  vCPU  MEMORY GiB  USD/HOUR    SAVINGS  RISK          WHY
   1  azure  centralindia  Standard_D2ps_v6   arm64            2         8.0  0.008538        81%  unavailable   ARCHITECTURE_MATCH,COST_POLICY,KNOWN_POSITIVE_PRICE,RESOURCE_MINIMUMS_MET
   2  azure  centralindia  Standard_D2ps_v5   arm64            2         8.0  0.009351        81%  unavailable   ARCHITECTURE_MATCH,COST_POLICY,KNOWN_POSITIVE_PRICE,RESOURCE_MINIMUMS_MET
   3  azure  centralindia  Standard_D2pds_v5  arm64            2         8.0  0.011162        81%  unavailable   ARCHITECTURE_MATCH,COST_POLICY,KNOWN_POSITIVE_PRICE,RESOURCE_MINIMUMS_MET
```

Filter flags belong to a subcommand, so they follow `list` or `recommend`. Only `--mcp`,
`--debug`, `--quiet`, `--json-log`, `--version` and `--help` are accepted before it.

To start MCP server mode:

```bash
spotinfo --mcp
```

The process waits for JSON-RPC on standard input. Press Ctrl+C to stop it.

## Configure an assistant

Add spotinfo to the MCP configuration of your assistant:

```json
{
  "mcpServers": {
    "spotinfo": { "command": "spotinfo", "args": ["--mcp"] }
  }
}
```

If the assistant cannot find the command, give the full path. Find it with `which spotinfo`.
Step-by-step instructions for Claude Desktop are in
[claude-desktop-setup.md](claude-desktop-setup.md).

## AWS credentials

Credentials are optional. Read the table in [clouds.md](clouds.md#aws) for what they add.
spotinfo uses the standard AWS SDK chain: environment variables, the shared configuration
file, and IAM roles.

## Environment variables

| Variable | Effect |
|---|---|
| `SPOTINFO_MODE=mcp` | Start in MCP server mode, the same as `--mcp` |
| `MCP_TRANSPORT` | `stdio` (the default) or `sse` |
| `MCP_PORT` | The port for the `sse` transport. Default: `8080` |
| `SPOTINFO_CACHE_DIR` | Move the feed cache. Default: a `spotinfo` folder in the user cache directory |
| `SPOTINFO_CACHE=off` | Disable the feed cache |
| `GOOGLE_CLOUD_PROJECT` | The project that authenticated GCP calls are billed to, the same as `--gcp-project` |
| `GOOGLE_APPLICATION_CREDENTIALS` | A service account key file for `--live-risk` and `--with-score` on GCP |
| `SPOTINFO_GCP_BILLING_KEY` | A Cloud Billing Catalog API key, the same as `--gcp-billing-key` |

## Uninstall

```bash
brew uninstall spotinfo          # Homebrew
sudo rm /usr/local/bin/spotinfo  # manual install
```

spotinfo writes one thing: the feed cache, which holds the AWS advisor and price feeds and
one file per Azure region it has fetched prices for. It lives in a `spotinfo` folder inside
the user cache directory — `~/Library/Caches` on macOS, `~/.cache` on Linux. Delete that folder
to remove every file spotinfo wrote.
