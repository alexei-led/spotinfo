# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

`spotinfo` is a Go CLI tool and MCP server for exploring Spot machine prices across AWS, GCP
and Azure. Every cloud ships a reviewed snapshot compiled into the binary, so every cloud
answers with no credentials; each also has a live path that degrades back to the snapshot
rather than failing.

**Two commands, both cloud-neutral, both served by the same provider-neutral seam
(`internal/cloud`).**

- `spotinfo list` — filter and show every match, with a risk column. Requires nothing and
  answers "what is there".
- `spotinfo recommend` — rank the top N against a stated requirement. **Requires**
  `--architecture`, `--min-vcpu` and `--min-memory-gib`, and answers "what should I run".

They share a vocabulary, not a purpose. Folding them would force three required flags onto a
browse command.

Bare `spotinfo` prints help and exits 1 (`no command given; run "spotinfo list" to browse or
"spotinfo recommend" to rank`) — **except** when a mode flag is set: `--mcp` and `--version`
are dispatched from the root Action and keep working. Only six flags are global: `--mcp`,
`--debug`, `--quiet`, `--json-log`, `--help/-h` and `--version/-v`. Every other flag belongs
to a subcommand and must follow it.

AWS is the only cloud that publishes a redistributable interruption figure, so GCP and Azure
report `risk.status: "unavailable"` — never a zero, never a low bucket — and serve only the
risk-free `cost` workload. GCP can fetch a per-project preemption rate with `--live-risk`
— see the note under Data Sources; it is visible but never filterable.

## The flag vocabulary

**One name per concept, decided in `cmd/spotinfo/vocabulary.go` and nowhere else.** That file
is the single source: the flag constants, the unified defaults, the retired-name map and the
CLI-to-MCP derivation rule. `cmd/spotinfo/vocabulary_test.go` walks the command tree built by
`newSpotinfoApp()` and asserts each command declares **exactly** the entries marked for it, so
adding a flag anywhere else fails a test rather than shipping a second name for one idea.

**Derivation rule, MCP argument from CLI flag: strip the leading `--`, replace `-` with `_`.**
The one exception — a repeatable CLI flag becomes a plural JSON array (`--region` → `regions`)
— lives in `pluralMCPArgs` as data, not as a branch in `mcpArgName`, so a second repeatable
flag is one line instead of a second special case.
`cmd/spotinfo/mcp_vocabulary_test.go` asserts every declared MCP argument equals `mcpArgName`
of its CLI flag. It lives in `cmd/spotinfo` because `mcpArgName` is in package `main` and
`internal/mcp` cannot import it — do not move the file to "fix" that.

**Unified defaults. The same value on both surfaces**, because the same question asked either
way must return the same document:

| Flag         | Commands       | Default, everywhere                                              |
| ------------ | -------------- | ---------------------------------------------------------------- |
| `--workload` | recommend only | `cost` — the only value every cloud accepts, and it caps nothing |
| `--region`   | both           | `all` — cross-region comparison is the tool's value              |
| `--top`      | recommend only | 3                                                                |
| `--os`       | both           | `linux`                                                          |
| `--output`   | both           | `table`                                                          |

`--sort` deliberately has **no** default. The retired default was `interruption`, which under
the neutral vocabulary is `--sort risk`, and `Query.CapabilityNeeds()` demands
`CapabilityRisk` for that key — a defaulted risk sort would refuse `spotinfo list --cloud gcp`
before acquisition for a column the command renders as a status. An unset key leaves the order
to the provider. `--order` alone is accepted and means ascending.

**`--region all` on AWS is slow, and that is accepted deliberately.** It queries every region,
and on `list` the live-price fallback fires for unpriced instances in each one. The help text
says so and points at `--offline` or an explicit `--region`.

**Eight retired names, each replaced by exactly one survivor.** A retired name produces a
rename hint on stderr and exits 1, never `flag provided but not defined`:

| Retired                    | Replacement        |
| -------------------------- | ------------------ |
| `--type`, `--instance`     | `--machine`        |
| `--vcpu`, `--cpu`          | `--min-vcpu`       |
| `--memory`, `--memory-gib` | `--min-memory-gib` |
| `--price`, `--budget`      | `--max-price`      |

The names are gone from the command tree; `renameHint` in `cmd/spotinfo/main.go` answers them
from an `OnUsageError` handler, which is set on the app **and** on both commands because
`Command.Run` consults `c.OnUsageError` and never the app's. Without it urfave/cli prints
"Incorrect Usage" plus the whole help page **to stdout**.

**MCP tools. Three, each mirroring the CLI, none with a cloud in its name:**

| Tool                      | Mirrors              | Emits                   | Replaced                   |
| ------------------------- | -------------------- | ----------------------- | -------------------------- |
| `list_spot_machines`      | `spotinfo list`      | `spotinfo.list/v1`      | `find_spot_instances`      |
| `recommend_spot_machines` | `spotinfo recommend` | `spotinfo.recommend/v3` | `recommend_spot_instances` |
| `list_cloud_regions`      | —                    | `spotinfo.regions/v1`   | `list_spot_regions`        |

A retired tool name returns JSON-RPC `-32602`. A failing call returns `spotinfo.error/v1`.
`recommend_spot_machines` declares **no** `sort`, `order`, `with_score`, `min_score`, `az`,
`score_timeout` or `live_risk` argument — a ranked page's placement figures are reachable from
the CLI only.

**Schema family. Four versions, sharing the candidate, risk, price and source DTOs:**
`spotinfo.list/v1`, `spotinfo.recommend/v3`, `spotinfo.regions/v1` and `spotinfo.error/v1`,
declared in `internal/cloud/schema.go`. `spotinfo.recommend/v1` and `/v2` are **retired** and
nothing in the binary emits them. The normative JSON Schemas live in `docs/plans/contracts/`
(`list-v1`, `recommend-v3-{input,success,error}`); `spotinfo.regions/v1` has no contract file.
Error codes: `INVALID_ARGUMENT`, `UNSUPPORTED_CAPABILITY`, `DATA_UNAVAILABLE`, `NO_CANDIDATES`,
`INTERNAL` (`internal/cloud/errors.go`).

## Invariants

These are the reason the tool can be trusted across clouds. Do not change them.

1. **`interruptionCappableKinds` holds exactly `[RiskKindInterruptionFrequencyRange]`.** The
   `web`/`ci`/`batch` ceilings are AWS Spot Advisor bucket boundaries and transfer to no other
   vendor's measurement. `RiskKindPreemptionRate` is deliberately outside it, and so would an
   Azure eviction rate be. A placement kind measures provisioning success, not interruption at
   all, and belongs to a separate vocabulary.
2. **A cloud that does not publish a figure reports its absence.** Never a zero, never a low
   bucket, never a substituted value from another cloud.
3. **A capability check happens before acquisition.** An unanswerable request is refused
   without reading a price.
4. **A change to a parser bumps `parser_version` in both the parser and the source contract,
   in the same change, and ends with `make verify-data`.** Never lower a threshold.
5. **No source enters a snapshot unless the source contract names it.** Authenticated and
   unclear-licence sources stay on opt-in runtime paths — the pattern `--live-risk`,
   `--gcp-billing-key` and the Azure live price path all follow.
6. **Builds stay hermetic.** No target that builds or tests may fetch.
7. **`content_sha256` provenance stays verifiable.** Trimming the published source list may
   never drop the provenance for a value a candidate actually carries.
8. **No Azure credential dependency.** `azidentity` costs +4.83 MB (+11.7% of the binary) and
   unlocks only the two deferred Azure features — eviction rate and placement score — neither
   of which can be tested without a subscription. Never add `azidentity`,
   `armresourcegraph` or `armrecommender`.

## Development Commands

### Building

- `make build` - Build binary for current OS/arch. Hermetic: embeds the committed data, never downloads.
- `make all` - Alias for `make build` (nothing more, despite the name)
- `make release` - Build binaries for multiple platforms

### Testing

- `make test` - `go test ./...`
- `make test-verbose` - `go test -v ./...`
- `make test-race` - Run tests with race detector
- `make test-coverage` - Run tests with coverage reporting

### Code Quality

- `make lint` - Run golangci-lint with config from `.golangci.yaml`
- `make fmt` - `go fmt ./...`

### Data Updates

- `make update-data` - Update embedded Spot Advisor data from AWS
- `make update-price` - Update embedded spot pricing data from AWS
- `make update-gcp-data` - Rebuild the embedded GCP catalogue from its contracted pages
- `make update-azure-data` - Rebuild the embedded Azure catalogue from the Retail Prices API
  and the contracted Microsoft Learn size pages
- `make refresh-manifests` - Rewrite AWS sidecar manifest hashes (run by the update targets)
- `make verify-data` - Data gate: manifests, source contracts, parser contracts, coverage floors

### Architecture Gates

- `make verify-architecture-rules` - archfit package-boundary and layer gate
- `make verify-architecture` - the above, then `cmd/archfitcheck` fails on any open Critical or
  High archfit finding

### Dependencies

- `make check-deps` - Verify system has required dependencies (wget)
- `make setup-tools` - Install golangci-lint and mockery at their pinned versions. archfit
  is not installed here; `make verify-architecture-rules` installs it on demand.

## Architecture

### Core Components

- `internal/cloud/` - The provider-neutral domain. `Query`, `Result`, `Candidate`,
  `Capabilities`, fixed-point `Money`, risk/price/placement/source observations, the neutral
  recommender, the source-scope trimming, and the `spotinfo.list/v1`, `spotinfo.recommend/v3`,
  `spotinfo.regions/v1` and `spotinfo.error/v1` DTOs. It imports no provider SDK, no CLI and no
  MCP package; `dependencies_test.go` enforces that.
- `internal/snapshot/` - Manifest and source-contract contracts shared by every embedded
  snapshot, plus the fail-closed validators and the atomic writer the update commands use.
- `internal/feedcache/` - The provider-neutral on-disk cache for fetched documents. All three
  clouds fetch at runtime — the AWS advisor and price feeds, the Azure Retail Prices API, and
  the GCP Cloud Billing Catalog behind `--gcp-billing-key` — and all want one directory, one
  override variable and one off switch. What differs is the TTL and whether the origin publishes
  a validator, and both are the caller's to decide. Every operation is best-effort: a cache that
  cannot be read or written costs time, never answers. (The package doc still says "two clouds";
  the GCP path was added after it was written.)
- `internal/reproducible/` - The committed-payload gzip encoding the snapshot updaters share.
  gzip records a modification time and an OS byte, so without the zeroed header a no-op weekly
  refresh would open a pull request that changes no data.
- `internal/providers/` - `registry.go` compiles the recognised providers. A provider whose
  snapshot fails a gate is _disabled_, never substituted by another cloud.
  - `aws/` - Adapter over the legacy `internal/spot` client
  - `gcp/` - Offline catalogue parsed from Google's server-rendered pricing pages, plus
    `liveprice.go` (Cloud Billing Catalog, API key) and `placement.go` (obtainability, beta)
  - `azure/` - Offline catalogue from the Retail Prices API joined to Learn size pages, plus
    `liveprice.go` (anonymous live refresh of a named region) and `sources.go` (URL scope
    parsing for provenance trimming)
- `cmd/update-gcp-data/`, `cmd/update-azure-data/` - Anonymous snapshot updaters
- `cmd/archfitcheck/` - Fails the build on an open Critical or High archfit finding
- `cmd/spotinfo/` - CLI entry point using urfave/cli/v2
  - `main.go` - App construction, the root Action's `--mcp`/`--version` branch, the rename-hint
    `OnUsageError` handler, and the shared renderers
  - `vocabulary.go` - **The single source for every flag name, default and retired name**
  - `list.go`, `recommend.go` - The two commands
  - `provider_flags.go` - Query construction, capability refusals, provider resolution
  - `placement.go`, `liverisk.go`, `gcpprices.go` - The three opt-in enrichment flag groups
- `internal/spot/` - Legacy AWS business logic. Still the AWS acquisition path
  - `client.go` - Spot client orchestration and option handling
  - `data.go` - Both `//go:embed` directives, feed fetching, and static price parsing
  - `liveprice.go` - Live price fallback via EC2 DescribeSpotPriceHistory API
  - `types.go` - Core data types (Advice, TypeInfo, Range, etc.)
  - `score.go` - Spot placement scores via EC2 API
  - `architecture.go` - The embedded architecture snapshot and its lookup
  - `data/` - Embedded JSON data files from AWS feeds
- `internal/mcp/` - MCP server tools and handlers

There is no `price.go`; static pricing lives in `data.go` alongside the advisor parsing. There
is no `recommend.go` or `diagnose.go` in `internal/spot` either: the AWS-only recommender was
deleted with `spotinfo.recommend/v1`, and the architecture snapshot that shared the file moved
to `architecture.go`.

### Data Sources

AWS (three sources):

1. Spot Instance Advisor data: `https://spot-bid-advisor.s3.amazonaws.com/spot-advisor-data.json`
2. Spot pricing data: `https://website.spot.ec2.aws.a2z.com/spot.json` (the feed behind
   <https://aws.amazon.com/ec2/spot/pricing/> — replaces the legacy JSONP
   `spot-price.s3.amazonaws.com/spot.js`, frozen since 2024-05-13)
3. EC2 DescribeSpotPriceHistory API: Live fallback for newer instance types with $0 in the static feed

Sources 1-2 are embedded in the binary during build for offline capability.

`--offline` answers from those embedded snapshots and makes no **price or risk** request —
including no DescribeSpotPriceHistory, which is why the flag also clears the live-price
provider. Without that it was _slower_ than the live path, because every instance the
snapshot does not price fell through to an AWS API call that blocks for the live-price
timeout. It is worth having because the feeds dominate an invocation: the advisor
document alone takes over a second, against roughly 40 ms to answer from embedded data.

**`--offline` does not suppress placement enrichment, and the docs must not say it does.**
It governs price and risk acquisition; there is no snapshot a placement figure can be answered
from, so `--with-score` still calls the provider's placement API. Measured:

```console
$ spotinfo list --offline --region us-east-1 --machine '^m5\.large$' --with-score --score-timeout 3
spotinfo: failed to get spot savings: aws candidate acquisition: score enrichment failed: region us-east-1: spot placement scores unavailable: requires AWS credentials and the ec2:GetSpotPlacementScores permission: … dial tcp 169.254.169.254:80: connect: host is down
```

`--offline` with the score flags off — the default — is the invocation that reaches no network
at all.

The default is unchanged — live feeds, with the snapshot as fallback. `useEmbedded` now
governs both feeds; it used to apply to pricing only, so an "embedded" client still
downloaded the advisor document and no caller could actually avoid the network.

**Feed cache.** `internal/feedcache` stores fetched documents under `os.UserCacheDir()/spotinfo`
(`SPOTINFO_CACHE_DIR` overrides, `SPOTINFO_CACHE=off` disables) for every cloud that fetches:
the two AWS feeds, the Azure Retail Prices sweep and the GCP billing catalogue, the last two at
**24h** each. The two AWS time-to-live
values differ because the feeds do: the advisor document takes over a second to
transfer and its `Last-Modified` is months old, so it is cached for **24h**; prices are
rewritten through the day and transfer in a tenth of a second, so they are cached for
**1h**. Expiry revalidates with `If-None-Match`/`If-Modified-Since` rather than
re-downloading — both feeds serve ETags, and a 304 costs one round trip and no payload.
`--refresh` ignores any cached copy for the run.

Resolution order is: fresh cache, then the origin, then an _expired_ cache entry, then
the committed snapshot. The expired entry outranks the snapshot because it is AWS data
that is merely old, while the snapshot is AWS data that is old _and_ frozen at build
time. Every cache failure is non-fatal — a read-only filesystem costs time, not answers.

**`data_source.mode` has three states, and that is a deliberate contract decision.**
`live`, `cached` and `embedded-snapshot`. Reporting a cached answer as `live` would
have been a claim about recency that nothing verified, which is the same class of silent
substitution the provider seam exists to prevent. A copy the origin confirmed with a 304
_is_ `live`: it matches AWS right now.

**`live_price` is always emitted in `spotinfo.list/v1`, with no `omitempty`.** Price
provenance is carried by every other format — a `*` suffix in `text` and `table`, a
`Price Source` column in `csv` — and under `omitempty` the JSON form was the only one where
"this price came from the static feed" and "this build does not report provenance" were
indistinguishable. The optional fields beside it (`zone_prices`, `region_score`,
`zone_scores`, `score_fetched_at` and the obtainability fields) keep `omitempty`: each is
genuinely absent unless the matching flag asked for it.

**`--top` is bounded at `cloud.MaxTop` on the CLI, not only on the MCP surface.** The
bound is written into `request.top` in the published `spotinfo.recommend/v3` schema, which
`internal/mcp` validates against, so an unbounded CLI emitted documents that failed their
own contract — `--top 999 --output json` produced `"top": 999`. `execRecommendCmd` applies
it so the same flag on the same command cannot mean two things, and
`cmd/spotinfo/contract_v2_test.go` reads the maximum out of the schema file so raising one
without the other fails a test rather than a consumer.

**Provenance is trimmed per scope, and a source whose scope cannot be parsed is retained.**
Azure's manifest carries 81 sources — 55 region-scoped Retail Prices URLs and 26 Microsoft
Learn size pages — and untrimmed that is over nine tenths of a three-row payload. Each
provider derives a `cloud.SourceScope` (`{Region, Machines}`) from its own source URLs;
`internal/cloud` keeps only the sources some published candidate needs and reports the rest
as `sources_omitted`, resolvable through `list_cloud_regions`. An unparseable URL is
**retained**, which keeps Invariant 7 true by construction when a vendor changes a URL
shape, and an answer with no candidates publishes every source — both schemas declare
`data_source.sources` with `minItems: 1`. Measured on the committed Azure snapshot
(`recommend --cloud azure --architecture arm64 --min-vcpu 4 --min-memory-gib 16 --offline`):
4 sources published, 77 omitted, provenance 0.423 of the serialized payload. Trimming only one
dimension lands above half, which is what `TestProvenanceIsNotTheBulkOfAnAzureAnswer` in
`internal/providers/azure/sources_test.go` catches — it asserts a **ratio**, not a byte count,
so a refresh that changes the source count cannot silently disable it.

**Placement figures carry a kind and are never normalised onto a shared scale.**
`cloud.PlacementObservation` names its measurement: `placement_score` is the AWS integer
1-10 from `GetSpotPlacementScores`, `obtainability` is the GCP 0.0-1.0 probability from the
beta `compute.advice.capacity` API. `--min-score` states an integer 1-10 floor, so it is
**refused** on a cloud whose figure is not one:

```console
$ spotinfo list --cloud gcp --with-score --min-score 5
spotinfo: unsupported capability: --min-score is refused on gcp: gcp publishes obtainability, and an integer 1-10 floor states no reviewed mapping onto it
```

The table headers follow the kind too — `Placement Score (Regional)`/`(AZ)` against
`Obtainability (Regional)`/`(AZ)` — because a reader who saw "Placement Score" over a 0.0-1.0
value would read it as a very bad score. Azure publishes no placement figure and says so.

**Live GCP preemption risk is opt-in and never enters a snapshot.** `--live-risk`
calls `compute/beta advice.capacityHistory` with Application Default Credentials and
attaches a `preemption_rate` risk to the _ranked page_ — one request per
recommendation, never per catalogue entry. The figure is per-project advisory data,
so it is not redistributable and `internal/providers/gcp/data/source-contract.json`
does not name it; the contract governs the committed snapshot, and this never
touches it. The project comes from `--gcp-project` or `GOOGLE_CLOUD_PROJECT` and is
never read from gcloud's ambient `core/project`: the call is billed to whatever it
names.

`RiskKindPreemptionRate` is deliberately **absent** from `interruptionCappableKinds`.
Google measures (preempted Spots) / (Spots that stopped running); AWS measures the
fraction of _running_ instances interrupted. `acceptsRisk` rejects a kind that is not
listed, so `--workload web|ci|batch` keeps refusing GCP even when the number is
available. Live risk makes the figure visible, not filterable — that asymmetry is the
whole point of the kind vocabulary.

The credential resolution mirrors `awsConfigWithCredentials`: one lazy
`sync.OnceValues`, negative result cached. **Do not pass a cancellable context to
`google.DefaultClient`** — it is stored in the token source and reused on every
refresh, so a `defer cancel()` in the constructor makes the first real call fail with
`oauth2: ... context canceled` before any request is sent. Binary cost of the whole
slice: **+240,768 bytes** (0.59%).

GCP: Google's public server-rendered Spot and Compute pricing pages. The committed snapshot
covers **`us-central1` only** — 333 machines. A region outside it is an empty page and a `WARN`
at exit 0 on `list`, and `no candidates: gcp publishes no machines in <region>` at exit 1 on
`recommend`. `--gcp-billing-key` (or `SPOTINFO_GCP_BILLING_KEY`) prices further regions from the
Cloud Billing Catalog API for **one invocation** and never enters a snapshot: Google states no
redistribution terms for it, which is exactly why it stays on a runtime path.

Azure: the anonymous Azure Retail Prices API for amounts, joined to Microsoft Learn VM size
pages for vCPU, memory and processor architecture. 55 reviewed regions, 26 machine series,
**224 sizes, Linux and Windows**. Naming **one or two** explicit regions fetches their prices
live and anonymously (`data_source.mode: live`); three or more regions, `--region all` and
`--offline` answer from the snapshot instead, because a full sweep is megabytes per region. Adding Windows roughly doubled
the priced rows, so `max_compressed_bytes` in the source contract was raised — as a reviewed
change to a cap a larger catalogue legitimately outgrew, never as a lowered floor. The
committed `catalog.json.gz` is 209,979 bytes against a 262,144 cap.

Every non-AWS snapshot is governed by an approved
`internal/providers/<cloud>/data/source-contract.json` that enumerates its exact source URLs,
support matrix and thresholds. No provider may read a source the contract does not name, and
`make verify-data` fails when a snapshot contradicts its contract. See
`docs/research/multicloud-source-contracts.md` for the reasoning behind each approval.

### Key Libraries

- `github.com/urfave/cli/v2` - CLI framework. **Stay on v2.** v3 is stable but the
  migration rewrites the entire CLI surface (`cli.Command` replaces `cli.App`, changed
  context and flag access) for no user-visible gain. Do not "upgrade" it as part of a
  routine dependency bump; revisit only if v2 stops getting security fixes.
- `github.com/jedib0t/go-pretty/v6` - Table formatting
- `github.com/aws/aws-sdk-go-v2` - AWS SDK for live pricing and placement scores
- `golang.org/x/net/html` - HTML parsing for the GCP and Azure snapshot updaters (build-time
  data refresh only; the shipped binary makes no request)
- `github.com/mark3labs/mcp-go` - MCP server implementation (`internal/mcp/`)
- `github.com/bluele/gcache` - Placement score caching
- `github.com/spf13/cast` - Type coercion for MCP tool arguments
- `github.com/stretchr/testify` - Testing framework with assertions

## Build Requirements

- Go 1.26+
- wget (for data updates)
- golangci-lint and mockery (installed via `make setup-tools`, pinned in the Makefile)
- archfit (pinned by `ARCHFIT_VERSION`; installed on demand by
  `make verify-architecture-rules`, not by `make setup-tools`)

## Output Formats

`number|text|json|table|csv` on `spotinfo list`; `text|json|table|csv` on `spotinfo recommend`,
which **refuses `number`** — one savings percent has no meaning for a ranked page. `table` is
the default on both. MCP is always JSON.

## CI/CD Pipeline

### GitHub Actions Workflows

- **ci.yaml**: Modern CI with Go 1.26, tests, linting, the pinned archfit architecture gate
  (`make verify-architecture`), then matrix builds for all platforms
- **release.yaml**: Tag-triggered releases with binary uploads using standard Go toolchain
- **docker.yaml**: Multi-arch Docker images published to GitHub Container Registry (ghcr.io)
- **auto-release.yaml**: Quarterly automated releases with smart change detection and semantic versioning
- **update-data.yaml**: Weekly refresh of the embedded AWS feeds; warns on stale feeds and opens a PR. This is the only workflow that touches `internal/spot/data/`.
- **update-gcp-data.yaml**: Weekly refresh of `internal/providers/gcp/data/`; anonymous, opens one PR
- **update-azure-data.yaml**: Weekly refresh of `internal/providers/azure/data/`; anonymous, opens one PR

Each data workflow owns exactly one provider's directory and fails closed rather than committing
partial data.

### Docker

- **Build**: `docker build -t spotinfo .` (uses Go 1.26 and `make build`)
- **Multi-arch**: Supports linux/amd64 and linux/arm64 platforms
- **Registry**: Published to `ghcr.io/alexei-led/spotinfo`
- **Base**: Uses scratch image with ca-certificates for minimal attack surface

### Release Process

1. **Manual Release**: Create and push a tag starting with 'v' (e.g., `git tag v1.2.3 && git push origin v1.2.3`)
2. **Automated Release**: Runs quarterly (Jan/Apr/Jul/Oct) with automatic version bumping
3. **Artifacts**: Cross-platform binaries for Linux/macOS/Windows on AMD64/ARM64

The v2 surface is a **breaking** release: both commands, every flag name, all three MCP tool
names and both payload schemas changed. `docs/migration-v2.md` is the single upgrade table —
publish it in the release notes, and update any published MCP client configuration that names
a retired tool.

## Development Guidance

- Use `make` commands for all development tasks
- Run `make test-verbose` before committing changes
- Embedded data is refreshed by the weekly `update-data` workflow via PR — see Data Update
  Workflow below before refreshing it by hand
- Follow Go 1.26 best practices and modern testing patterns
- NEVER add Claude as co-author to git commit message

## Data Update Workflow

The embedded data files are critical — they provide offline capability:

- `internal/spot/data/spot-advisor-data.json` — Interruption rates, savings % (AWS advisor feed)
- `internal/spot/data/spot-price-data.json` — Static spot pricing (AWS pricing-page feed, plain JSON)
- `internal/spot/data/{spot-advisor,spot-price}-data.json.gz` — **what the binary embeds.**
  Each is its `.json` compressed, and is 3.4 MB smaller across the two
- `internal/spot/data/{spot-advisor,spot-price,architecture}-manifest.json` — the sidecar
  manifests that hash and describe those files

**Two files per feed, on purpose.** The readable `.json` is what a data-refresh pull
request is reviewed from — a reviewer can see which prices moved. The `.json.gz` is what
ships. They cannot drift: `TestEmbeddedArchivesMatchTheirJSON`, part of `verify-data`,
fails when an archive does not decompress to exactly the `.json` beside it. Never
hand-edit either; run `make refresh-manifests`, which rebuilds the archive from the
`.json` and rewrites the hashes.

The manifest's `payload.form` is `compressed-source`, not `raw-source`: `payload.sha256`
is the archive that ships, while `sources[0].sha256` stays the hash of the document the
URL serves. That distinction is what keeps the `content_sha256` published in every
answer verifiable by re-fetching the source — an archive's hash would not match it.
`architecture-snapshot.json` is left uncompressed; it is 3.8 KB.

**Update flow:**

1. `make update-data` — fetches fresh `spot-advisor-data.json`
2. `make update-price` — fetches fresh `spot-price-data.json`
3. `make verify-data` — parse gate on the embedded files
4. Commit the refreshed `.json`, its regenerated `.json.gz`, **and** the manifest
   sidecars — the update targets end by running `refresh-manifests`, which rebuilds the
   archive and rewrites the hashes, and `verify-data` fails when any of the three disagree

Both targets download to a `.tmp` file and only replace the tracked file on success,
so a failed download cannot clobber good data.

**When to update:** Normally never by hand — the `update-data` workflow runs weekly and
opens a PR. Build, test and release workflows deliberately do NOT refresh the feeds, so
every binary embeds exactly what is committed. Run the targets manually only when you need
data fresher than the last PR.

**If prices show $0:** the instance type is missing from the static price feed. That is
expected for very new families and for regions AWS omits (currently all `me-*`); those fall
through to the live `DescribeSpotPriceHistory` path, which needs AWS credentials.

### Updating a GCP or Azure snapshot

`make update-gcp-data` / `make update-azure-data`, then `make verify-data`, then commit the
whole `internal/providers/<cloud>/data/` directory. Both updaters validate against the source
contract, the manifest hash and the reviewed coverage floor **before** writing, so a failed run
leaves the reviewed snapshot untouched. Both are normally run by their weekly workflow.

A refresh failure is usually the source changing shape, not a flake. `docs/data-sources.md`
tables every expected error and what it means.

**The GCP updater reads every contracted page twice and refuses when the two copies
differ.** Google serves these pages from a CDN that can hold more than one generation at
once: on 2026-08-10 five consecutive requests to the Spot page — same URL, same
User-Agent, seconds apart — alternated between `n2-standard-4` at $0.101336 and
$0.111472, every response a different hash, while the general-purpose page was stable in
the same window. A single read cannot tell which generation it got, and the four pages
are read seconds apart, so one run could pair a Spot price from one generation with an
On-Demand price from another and publish a savings figure spanning two days. Nothing
downstream can catch that — both numbers are well-formed, in range, and from the
contracted URL. Two identical reads do not prove stability; two different ones disprove
it, and that is the case worth refusing (`ErrSourceUnstable`). It costs one extra
download per page on a weekly build-time job and nothing at runtime.

**The two copies are compared on `stabilityDigest`, not on the raw body, and that is
load-bearing.** Every response from `cloud.google.com` carries a fresh CSP `nonce`
attribute and a fresh `FdrFJe` request id inside a script body, so the raw SHA-256 differs
on every read whatever the prices do. Measured on 2026-08-12: nine reads with the
updater's own User-Agent gave nine different raw hashes, one digest and one price. A
raw-body comparison therefore refused **every** run — the gate landed on 2026-08-10, one
day after the last successful snapshot, and no refresh has passed since, which is how the
committed catalogue came to publish `n2-standard-4` at $0.101336 while the page served
$0.111472. `stabilityDigest` strips script and style bodies, comments and every tag,
collapses whitespace and hashes the remaining text: it moves when a price moves and not
when markup re-rolls. The manifest still records the raw body hash — that is provenance of
the bytes the run read, and for a nonce-bearing page it is a record, not a checksum a
re-fetch can reproduce.

**The per-page double read is not enough on its own, and `confirmWindowStable` is the
other half.** Comparing a page against itself cannot, by construction, see a rollover
that lands _between_ two pages — which is precisely the cross-page mixing described
above. `fetchSources` therefore re-reads the first downloaded page after the last one and
refuses when its digest moved, bracketing every interval between the pages for one extra
download per run. Both halves are load-bearing: the per-page read catches a flip inside
one page's window, the bracket catches a flip between windows. Do not remove either
because the other exists.

`update-gcp-data: gcp pricing page is not serving a stable document` therefore means
wait and retry, not investigate the parser — and the updater says so with **exit 75**
(`EX_TEMPFAIL`) against 1 for every other failure. The weekly workflow branches on that
code and reports a notice instead of a red run. Neither `go run` nor `make` preserves an
exit code, so the workflow builds the binary and calls it directly; `make update-gcp-data`
stays the human-facing command. A machine whose two pages disagree about its
_shape_ is a different failure — excluded and reported, not fatal — and that is a
defence against one stale cell, not against an unstable source.

Two rules matter most:

- The coverage floor is a **floor**, not a census, and for Azure it is applied **per region**.
  Never lower it to make a short refresh pass.
- Never widen a parser to make a changed source fit. Review the source, then bump
  `parser_version` in both the parser and the source contract.

## Provider Interfaces (Key Pattern)

Two layers of interface. The neutral one is what new code should use:

```go
// cloud.Provider — the neutral seam every cloud implements (internal/cloud/provider.go)
type Provider interface {
    ID() ProviderID
    Capabilities() Capabilities
    Query(ctx context.Context, query *Query) (Result, error)
}
```

A provider declares what it can answer through `Capabilities`, and the CLI and MCP layers reject
an unsupported request with `UNSUPPORTED_CAPABILITY` _before_ acquisition. Never let a provider
answer a question it cannot: a cloud with no risk data must report
`RiskStatusUnavailable`, never a zero or a low bucket, or a neutral consumer will rank its
silence against another cloud's measurement.

A published risk figure is not automatically comparable either. The `web`/`ci`/`batch`
ceilings are AWS Spot Advisor bucket boundaries, so `acceptsRisk` admits only a kind listed
in `interruptionCappableKinds` (`internal/cloud/recommend.go`). Adding a provider that
publishes a different measurement means giving it a reviewed wire name in
`riskKindWireNames` and deciding, deliberately, whether it belongs in that list — not
letting a preemption rate be filtered as if it were an interruption frequency.

The same applies to a placement figure through `cloud.PlacementKind`. A kind is a claim about
what was measured, and the refusals follow the kind rather than the cloud: `--min-score` states
an integer 1-10 floor and is refused where the figure is a probability, `--sort score` is
refused without `--with-score` because nothing would have been fetched to order by, and a cloud
that publishes no placement figure at all says so. Every refusal names the vendor limit that
caused it — a message that names a missing feature instead of the limit sends the reader
looking for a flag.

The legacy AWS interfaces below still back `internal/spot`. Never call AWS directly in tests.

```go
// advisorProvider — embedded/remote advisor data
type advisorProvider interface {
    getRegions() []string
    getRegionAdvice(region, os string) (map[string]spotAdvice, error)
    getInstanceType(instance string) (TypeInfo, error)
    getRange(index int) (Range, error)
}

// pricingProvider — static pricing data
type pricingProvider interface {
    getSpotPrice(instance, region, os string) (float64, error)
}

// livePriceProvider — EC2 API fallback for zero-price instances
type livePriceProvider interface {
    fetchLivePrices(ctx context.Context, region string, instanceTypes []string, os string) (map[string]float64, error)
}

// scoreProvider — EC2 placement scores
type scoreProvider interface {
    enrichWithScores(ctx context.Context, advices []Advice, singleAZ bool, timeout time.Duration) error
}
```

In tests: use `mocks_test.go` which implements all interfaces with controllable behavior.

> **`mocks_test.go` is generated** — it opens with `// Code generated by mockery; DO NOT EDIT.`
> Never hand-edit it. Regenerate with `make mocks` (mockery pinned via `MOCKERY_VERSION`
> in the Makefile, also installed by `make setup-tools`).
>
> **Every mocked interface must be listed in `.mockery.yaml`.** A mock that exists only by
> hand inside the generated file is silently deleted by the next run. `livePriceProvider`
> was in exactly that state and is now declared properly, alongside `advisorProvider`,
> `pricingProvider`, `scoreProvider`, `awsAPIProvider` and `spotClient`.
> In production: use `NewWithOptions()` which wires up real AWS providers.
> For injection: use `NewWithProviders(advisor, pricing)` + `SetLivePriceProvider()`.

## Functional Options Pattern

`GetSpotSavings` uses functional options — add new filters without breaking existing callers:

```go
// Adding a new filter option:
func WithFoo(foo string) GetSpotSavingsOption {
    return func(cfg *getSpotSavingsConfig) {
        cfg.foo = foo
    }
}
// Add `foo string` field to getSpotSavingsConfig
// Apply in GetSpotSavings() after the options loop
```

## Testing Approach

- **Framework**: testify for assertions; `make test-coverage` for the coverage report
- **Unit tests** use mock providers from `mocks_test.go` (in `internal/spot` and
  `cmd/spotinfo`) or hand-written `cloud.Provider` stubs — no AWS credentials needed
- **End-to-end tests**: `cmd/spotinfo/e2e_test.go` builds the binary once in `TestMain` and
  runs it as a subprocess. It is the only place that covers flag parsing, exit codes, the
  stdout/stderr split and the MCP stdio handshake as shipped. It needs no credentials and makes
  no request, through **two** arrangements that are both load-bearing. `e2eEnv` pins
  `HTTP_PROXY`/`HTTPS_PROXY` at a closed port for **every** subprocess, so the live-to-snapshot
  fallback is exercised rather than the network. And `e2eOfflineFor` adds `--offline` on AWS
  only: Task 8 refuses the flag on a cloud declaring no `CapabilityLiveEnrichment`, so passing
  it everywhere would have made the suite flip red and green across intermediate commits for a
  reason that says nothing about the surface. **Azure and GCP now have live paths**, and the
  suite stays request-free because every Azure invocation uses the default `--region all`,
  which never sweeps, and no GCP invocation passes a billing key. A new e2e test naming an
  explicit Azure region must extend `e2eOfflineFor` to Azure. `-short` skips these — they
  compile the binary — so `go test -short ./...` is the fast lane, not a credential guard.
  Any helper added to that file must be prefixed `e2e`: it shares a package with the unit tests.
- **Never let a test reach the network.** `--offline` is a **subcommand** flag now, not a root
  one, so `spotinfo --offline --mcp` is rejected: `flag provided but not defined: -offline`.
  The per-call replacement is the `offline` tool argument on `list_spot_machines` and
  `recommend_spot_machines`: measured on an AWS `list_spot_machines` call with the feed cache
  off, **0.13 s with `"offline": true` against 5.60 s without**. If you add a test that drives
  MCP, pass it — **and** pin `HTTP_PROXY`/`HTTPS_PROXY` at a closed port anyway, because
  `list_cloud_regions` declares only `cloud`, so no argument can stop it fetching (measured
  7.25 s, `data_source.mode: live`).
- If you add a test that genuinely needs real AWS, guard it with
  `if testing.Short() { t.Skip("requires AWS credentials") }` so `go test -short ./...`
  stays credential-free.
- **Parallel**: unit tests use `t.Parallel()` — keep it that way, with one
  measured exception. Tests in `cmd/spotinfo` that build or run a `cli.App` must
  stay serial: urfave/cli appends its **package-level** `HelpFlag` to every
  command it parses and writes to it in `Apply`, so two concurrent `app.Run`
  calls race inside the library. `go test -race` reports it as a data race in
  `urfave/cli`, not in this repository. A test in that package that touches no
  CLI app can still be parallel. Anything using `t.Setenv` must stay serial too —
  Go panics if a test calls both.
- **Table-driven**: use `tc := tc` (loop variable capture) or Go 1.22+ range semantics
- **Resilient**: assertions tolerate the embedded feeds changing under them
- **No network in unit tests**: build clients via the embedded path and a nil live-price
  provider. A real client stalls for `livePriceTimeout` (10s) per call on unpriced
  instances — that alone was 79s of the suite. See `newEmbeddedRegistry` and `stubProvider`
  in `internal/mcp/helpers_test.go`; the MCP tests answer from a `cloud.Provider` stub and
  never open an AWS client.
- **Published output is golden-pinned**, and **every golden is rendered from one fixed
  `cloud.Provider` stub**, never from the embedded feeds and never through the production AWS
  adapter. That adapter reads its provenance from the committed sidecar manifests, so a golden
  recorded through it would carry manifest hashes and timestamps and every weekly data-refresh
  pull request would rewrite a contract nobody meant to change. The stub is
  `contractCandidates()` in `cmd/spotinfo/contract_test.go`, and it deliberately covers every
  branch the renderers have: a static price and a live one, a regional placement score and a
  pair of zonal ones with their own zone prices, and a machine the cloud published no price for
  — an unknown price is the **absence** of an observation, never a zero, so JSON publishes
  `null` and the rendered formats print `-`.

  The current set is `cmd/spotinfo/testdata/{list-v1.*,recommend-v3.*}` and
  `internal/mcp/testdata/recommend-v3-*.json`. The retired `aws-*-v1.*` and
  `find-spot-instances-v1-*.json` goldens are **deleted** with the schemas they recorded — do
  not look for them or recreate them. A diff in the surviving set is a client-visible contract
  break, not a test to update. For those contract tests `UPDATE_GOLDEN=1` rewrites the golden
  **and fails the run**, so a regeneration can never be reported as a pass; review the diff,
  then re-run without it.

- **`REFRESH_MANIFESTS=1` is a separate switch** and drives `make refresh-manifests` only,
  where rewriting is the point and the run passes normally. Keep the two names apart. When
  they were one variable, regenerating a CLI golden — or any ambient `UPDATE_GOLDEN=1` —
  also re-blessed whatever data files were on disk and let `make verify-data` exit 0 on a
  snapshot no reviewer accepted. `verify-data` now clears both variables on every line: a
  gate that can rewrite what it checks is not a gate.

When adding a new feature:

1. Add the interface to `.mockery.yaml` and run `make mocks` if a new mock is needed
2. Write unit test with mock provider
3. Only if it genuinely needs live AWS, add an integration test guarded by
   `testing.Short()` — and expect to be the first, see above

## Common Mistakes to Avoid

- **Never** call `enrichMissingPrices` with a nil provider — it's a no-op but check the guard
- **Never** forget `Advice.LivePrice = true` when price comes from EC2 API (not static feed)
- **Never** bypass the `maxPrice` re-filter after live price enrichment
- `allRegionsKeyword = "all"` is the special value for `--region all`, not an actual region
- **Never** guard a float CLI flag with `value <= 0` alone. `NaN` fails every comparison, so
  it slips past both the rejection and the "is it set" branch and silently drops the filter
- **Never** lower a snapshot's coverage floor on an unreadable manifest. The updaters seed
  from the contract minimum only when the manifest is genuinely absent; anything else is an
  error, or a reviewer's raised floor is discarded by a green PR
- `defaultScoreTimeout` and `livePriceTimeout` are separate — don't confuse them
- **Never** make `build` depend on `update-data`/`update-price`. It used to, which made every
  build and Docker image download fresh feeds and overwrite tracked data. Builds must be
  hermetic; refreshing data is a separate, explicit step.
- **Never** add a provider without an approved `source-contract.json`. The contract is the
  normative list of sources; reading anything it does not name is out of contract.
- **Never** infer processor architecture from a machine name when the source publishes it. The
  Azure parser reads each page's `[x86-64]`/`[Arm64]` marker and fails when it is absent; an Arm
  size shipped as `x86_64` passes every other gate and silently recommends a machine that
  cannot run the caller's binaries.
- **Never** resolve Azure effective dates against `time.Now()` inside the parser. The instant is
  an argument so one run resolves every region consistently and a rebuild is reproducible.
- **Never** treat an Azure `Low Priority` meter as Spot, or a `Cloud Services` meter as a VM
  price. Both sit under `serviceName = "Virtual Machines"` beside the real meters.
- The static price feed is an **undocumented** endpoint (`website.spot.ec2.aws.a2z.com/spot.json`),
  not a published AWS API. Its predecessor froze silently for two years. If prices look wrong
  across the board, check the feed's `Last-Modified` before debugging the code.
- **Never** declare a flag outside `cmd/spotinfo/vocabulary.go`. The command-tree test asserts
  equality, not containment, so an extra flag fails as loudly as a missing one — which is the
  point.
- **Never** add a second name for a concept the vocabulary already names, and never reintroduce
  a retired one as an alias. `--cpu` derives the MCP argument `cpu`, which loses both the
  "minimum" and the unit; that is why it became `--min-vcpu`. An alias would put the exception
  back.
- **Never** normalise one vendor's placement figure onto another's scale. AWS publishes an
  integer 1-10, GCP a 0.0-1.0 probability, and no vendor published a mapping between them.
- **Never** reintroduce `info.emr`, or any other AWS Spot Advisor flag, into `cloud.Candidate`.
  EMR compatibility has no meaning on another cloud; the field was dropped with
  `spotinfo.recommend/v1`, not translated.
- **Never** assume `--offline` composes with `--mcp`. It is a subcommand flag; the MCP surface
  takes `"offline": true` per tool call instead.
- **Never** hand a `sort` key to a provider that cannot order by it. `Query.CapabilityNeeds()`
  turns the key into a capability, which is why `--sort` has no default: a defaulted risk sort
  would refuse GCP and Azure before acquisition.
