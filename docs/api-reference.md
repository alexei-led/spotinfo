# API Reference

This document specifies the JSON documents `spotinfo` publishes, and the MCP tools that
serve them.

It covers both surfaces at once, because both emit the same documents.
`spotinfo list --output json` and the MCP tool `list_spot_machines` return the same
`spotinfo.list/v1` payload for the same question — the same keys and the same values, not
merely a similar shape. Only the framing differs: the CLI pretty-prints to stdout, the tool
returns a compact document in one text content item.
`spotinfo recommend --output json` and `recommend_spot_machines` return the same
`spotinfo.recommend/v3`.

Two things are reachable from the CLI only: a ranked page's placement figures
(`--with-score`) and live GCP preemption risk (`--live-risk`). No MCP tool declares an
argument for either. There is no `gcp_project` or `gcp_billing_key` argument on any tool;
on that surface both are read from the environment.

## Published documents

| Schema                  | Emitted by                                                        | Normative contract                                                               |
| ----------------------- | ----------------------------------------------------------------- | -------------------------------------------------------------------------------- |
| `spotinfo.list/v1`      | `spotinfo list --output json`, MCP `list_spot_machines`           | `docs/plans/contracts/list-v1.schema.json`                                       |
| `spotinfo.recommend/v3` | `spotinfo recommend --output json`, MCP `recommend_spot_machines` | `docs/plans/contracts/recommend-v3-input.schema.json` and `-success.schema.json` |
| `spotinfo.regions/v1`   | MCP `list_cloud_regions` only — no CLI command emits it           | none                                                                             |
| `spotinfo.error/v1`     | every failing MCP tool call                                       | `docs/plans/contracts/recommend-v3-error.schema.json`                            |

`spotinfo.recommend/v1` and `spotinfo.recommend/v2` are retired. Nothing in this binary
emits either.

The schema files control types, required fields, defaults, nullability, formats and
`additionalProperties`; this page is prose around them. `internal/mcp/jsonschema_test.go`
validates real payloads against the contract directory, and `internal/cloud/schema_test.go`
reads `recommend-v3-success.schema.json` directly, so a document that drifts from its schema
fails a test rather than a consumer.

`spotinfo.regions/v1` has no contract file. It is validated by its Go tests only.

### A note on `<` and `>` in JSON

Go's `encoding/json` escapes both characters into their six-character Unicode escape form,
so an AWS risk label leaves the binary escaped and decodes to `<5%` or `>20%`. Every JSON
parser handles it. The examples below show the decoded form, which is what a client sees;
a raw byte-for-byte capture of stdout shows the escapes.

---

## `spotinfo.list/v1`

A browse answer. It states what is there; it does not rank.

An empty match is **not** an error: `status` stays `"ok"`, `candidates` is `[]`, and the CLI
exits 0.

```console
$ spotinfo list --offline --region us-east-1 --machine '^m5\.large$' --output json
{
  "schema_version": "spotinfo.list/v1",
  "status": "ok",
  "request": {
    "cloud": "aws",
    "regions": [
      "us-east-1"
    ],
    "machine": "^m5\\.large$",
    "architecture": "",
    "os": "linux",
    "min_vcpu": 0,
    "min_memory_gib": 0,
    "max_price": null,
    "sort": "",
    "order": "asc"
  },
  "data_source": {
    "provider": "aws",
    "mode": "embedded-snapshot",
    "sources": [
      {
        "url": "https://spot-bid-advisor.s3.amazonaws.com/spot-advisor-data.json",
        "fetched_at": "2026-08-10T06:54:02Z",
        "observed_at": null,
        "content_sha256": "158f404cca4a5167a2a035cbd952de8f1886bb14c8cdbc2a55f4871c33735f1a",
        "parser_version": "aws-spot-advisor-json/1",
        "schema_version": "aws.spot-advisor-feed/v1"
      },
      {
        "url": "https://website.spot.ec2.aws.a2z.com/spot.json",
        "fetched_at": "2026-08-10T06:54:02Z",
        "observed_at": null,
        "content_sha256": "cafdfff877df03265127b0b7615b92de79bc4e53b20eef3f2bb37181160fdbee",
        "parser_version": "aws-spot-price-json/1",
        "schema_version": "aws.spot-price-feed/v1"
      },
      {
        "url": "https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/instance-types.html",
        "fetched_at": "2026-08-06T00:00:00Z",
        "observed_at": null,
        "content_sha256": null,
        "parser_version": "aws-architecture-snapshot/1",
        "schema_version": "aws.architecture-snapshot/v1"
      }
    ],
    "sources_omitted": 0
  },
  "candidates": [
    {
      "cloud": "aws",
      "region": "us-east-1",
      "machine": "m5.large",
      "architecture": "x86_64",
      "os": "linux",
      "vcpu": 2,
      "memory_gib": 8,
      "spot_usd_per_hour": "0.039900000",
      "on_demand_usd_per_hour": null,
      "savings_percent": 59,
      "risk": {
        "status": "available",
        "kind": "interruption_bucket",
        "label": ">20%",
        "min_percent": 23,
        "max_percent": 100,
        "window_days": 30,
        "source_url": "https://spot-bid-advisor.s3.amazonaws.com/spot-advisor-data.json",
        "observed_at": null
      },
      "live_price": false
    }
  ],
  "warnings": []
}
```

### `request`

The echo of what was asked, after defaults. Always these ten keys.

| Key              | Type                | Notes                                          |
| ---------------- | ------------------- | ---------------------------------------------- |
| `cloud`          | `aws\|gcp\|azure`   | Defaults to `aws`                              |
| `regions`        | array of string     | Defaults to `["all"]` on both surfaces         |
| `machine`        | string              | RE2 pattern; `""` means no machine-name filter |
| `architecture`   | `""\|x86_64\|arm64` | `""` means no architecture filter              |
| `os`             | `linux\|windows`    | Defaults to `linux`                            |
| `min_vcpu`       | integer ≥ 0         | `0` means no floor                             |
| `min_memory_gib` | number ≥ 0          | `0` means no floor                             |
| `max_price`      | number or `null`    | `null` means no ceiling                        |
| `sort`           | enum                | `""` keeps the order the cloud publishes       |
| `order`          | `asc\|desc`         | Defaults to `asc`                              |

`sort` echoes the **neutral field name**, not the word a caller wrote. The vocabulary is
`price`, `savings`, `risk`, `machine`, `region` and `score`; the first five echo unchanged,
and `score` echoes as `placement_score` — the field's own name. The enum in
`list-v1.schema.json` is the normative list.

### `candidates[]`

Twelve keys are always present.

| Key                      | Type                     | Notes                                                                            |
| ------------------------ | ------------------------ | -------------------------------------------------------------------------------- |
| `cloud`                  | `aws\|gcp\|azure`        |                                                                                  |
| `region`                 | string                   | The provider's own region identifier                                             |
| `machine`                | string                   | The provider's own machine name — `m5.large`, `n2-standard-2`, `Standard_D2s_v5` |
| `architecture`           | `""\|x86_64\|arm64`      |                                                                                  |
| `os`                     | `linux\|windows`         |                                                                                  |
| `vcpu`                   | integer                  |                                                                                  |
| `memory_gib`             | number                   | GiB, not GB                                                                      |
| `spot_usd_per_hour`      | decimal string or `null` | Exactly nine fractional digits, e.g. `"0.039900000"`                             |
| `on_demand_usd_per_hour` | decimal string or `null` | `null` on AWS, which publishes no on-demand price in this feed                   |
| `savings_percent`        | number or `null`         | 0-100                                                                            |
| `risk`                   | object                   | See below; all eight keys always present                                         |
| `live_price`             | boolean                  | `true` when the price came from a live provider API rather than a feed           |

Prices are decimal **strings** so no consumer has to reconstruct an amount from a float.

Optional keys appear only when the matching flag or argument asked for them:
`zone_prices`, `region_score`, `zone_scores`, `score_fetched_at`, `region_obtainability`,
`zone_obtainability`, `region_estimated_uptime_seconds` and `placement_status`. See
[Placement observations](#placement-observations).

### `risk`

```json
"risk": {
  "status": "unavailable",
  "kind": null,
  "label": null,
  "min_percent": null,
  "max_percent": null,
  "window_days": null,
  "source_url": null,
  "observed_at": null
}
```

That block is exactly what every GCP and Azure candidate carries. An absent measurement is
`null`, never `0` and never a low bucket. `status` is one of `available`, `unavailable`,
`unsupported` or `unknown`.

`kind` is `interruption_bucket` (AWS Spot Advisor), `preemption_rate` (GCP, `--live-risk`
only) or `null`. `eviction_rate` is in the frozen enum and has no producer in this build.

**Kinds from different clouds measure different things and must not be compared.** AWS
publishes the fraction of _running_ instances interrupted over a 30-day window. Google's
preemption rate is (preempted Spot VMs) / (Spot VMs that stopped running). That is why
`--workload web|ci|batch`, whose ceilings are AWS Advisor bucket boundaries, is refused on
a cloud that publishes anything else — even when the figure is present.

### `warnings`

Array of strings, never `null`. A degraded live path adds a warning here and still answers;
it does not fail the call.

---

## `spotinfo.recommend/v3`

A ranked page. Unlike `list`, an empty result is an **error** (`NO_CANDIDATES`), because a
recommendation with nothing in it is not an answer.

```console
$ spotinfo recommend --offline --region us-east-1 --architecture x86_64 \
    --min-vcpu 2 --min-memory-gib 4 --top 2 --output json
```

The `schema_version`, `data_source` and `warnings` blocks are identical in shape to
`spotinfo.list/v1`. What differs:

```json
{
  "schema_version": "spotinfo.recommend/v3",
  "status": "ok",
  "request": {
    "cloud": "aws",
    "regions": ["us-east-1"],
    "machine": "",
    "architecture": "x86_64",
    "os": "linux",
    "min_vcpu": 2,
    "min_memory_gib": 4,
    "max_price": null,
    "workload": "cost",
    "top": 2
  },
  "ranking_policy": [
    "spot_price_ascending",
    "excess_vcpu_ascending",
    "excess_memory_gib_ascending",
    "region_ascending",
    "machine_ascending"
  ],
  "recommendations": [
    {
      "rank": 1,
      "cloud": "aws",
      "region": "us-east-1",
      "machine": "t2.medium",
      "architecture": "x86_64",
      "os": "linux",
      "vcpu": 2,
      "memory_gib": 4,
      "spot_usd_per_hour": "0.016100000",
      "on_demand_usd_per_hour": null,
      "savings_percent": 62,
      "risk": {
        "status": "available",
        "kind": "interruption_bucket",
        "label": "<5%",
        "min_percent": 0,
        "max_percent": 5,
        "window_days": 30,
        "source_url": "https://spot-bid-advisor.s3.amazonaws.com/spot-advisor-data.json",
        "observed_at": null
      },
      "rationale_codes": [
        "ARCHITECTURE_MATCH",
        "COST_POLICY",
        "KNOWN_POSITIVE_PRICE",
        "RESOURCE_MINIMUMS_MET"
      ]
    }
  ]
}
```

Differences from a `list` candidate:

- `request` carries `workload` and `top` instead of `sort` and `order`.
- Each recommendation carries `rank` (1-based) and `rationale_codes`, and no `live_price`.
- `spot_usd_per_hour` is **never** `null` here. A machine with no known positive price is
  not rankable and does not reach the page.
- `architecture` is never `""`, and `min_vcpu`/`min_memory_gib` are never zero: the three
  are required.

### Input

`architecture`, `min_vcpu` and `min_memory_gib` are required on both surfaces. Omitting one
on the CLI is refused before anything is read:

```console
$ spotinfo recommend --offline
spotinfo: invalid argument: --architecture, --min-vcpu, --min-memory-gib are required; every recommendation needs an architecture and a size floor
```

`top` is bounded at 1..50 on **both** surfaces — the CLI applies the same bound the schema
declares, so a payload can never contradict its own contract.

### `ranking_policy`

The ordering actually applied, most significant first. It is published so a consumer never
has to infer why one row outranks another. The `cost` workload's policy is the one shown
above; `web`, `ci` and `batch` apply the same ordering after an interruption ceiling.

### `workload`

`cost` makes **no** interruption claim. `web`, `ci` and `batch` admit only a risk bucket
whose **maximum** is at most 5%, 16% and 22% respectively; on AWS the reachable Advisor
buckets make those effective ceilings 5%, 15% and 20%.

All three require a cloud that publishes an interruption frequency. Asking on one that does
not is refused before acquisition, and the message says why:

```console
$ spotinfo recommend --cloud gcp --architecture x86_64 --min-vcpu 2 --min-memory-gib 4 --workload web
spotinfo: gcp: unsupported capability: risk: the web workload caps interruption frequency at 5%, an AWS Spot Advisor bucket boundary, and gcp publishes no figure measured that way; workload cost applies no ceiling and answers on every cloud
```

### `rationale_codes`

Non-empty and unique. The codes this build emits:

| Code                     | Meaning                                                  |
| ------------------------ | -------------------------------------------------------- |
| `ARCHITECTURE_MATCH`     | The machine's architecture is the one that was required  |
| `RESOURCE_MINIMUMS_MET`  | vCPU and memory floors are satisfied                     |
| `KNOWN_POSITIVE_PRICE`   | A real Spot price was read, not a zero or an absence     |
| `COST_POLICY`            | Ranked under `--workload cost`, which applies no ceiling |
| `WORKLOAD_WEB_CAP_MET`   | Risk is within the `web` ceiling                         |
| `WORKLOAD_CI_CAP_MET`    | Risk is within the `ci` ceiling                          |
| `WORKLOAD_BATCH_CAP_MET` | Risk is within the `batch` ceiling                       |

`COST_POLICY` appears only under `--workload cost`; the three `WORKLOAD_*_CAP_MET` codes
replace it under the other three.

---

## `spotinfo.regions/v1`

Emitted by `list_cloud_regions` and by nothing else — there is no `spotinfo regions`
subcommand.

Five top-level keys: `schema_version`, `status`, `cloud`, `regions` and `data_source`.
`tools/call list_cloud_regions {"cloud": "gcp"}` answers with `"regions": ["us-central1"]`
and the `data_source` block described below.

Its `data_source` carries the **complete** source list of that cloud, which is what makes
the `sources_omitted` count of a trimmed `list` or `recommend` answer resolvable. Measured
on this build: AWS 34 regions and 3 sources, GCP 1 region and 5 sources, Azure 55 regions
and 81 sources — all three with `sources_omitted: 0`.

---

## `spotinfo.error/v1`

An MCP error result has `isError: true` and one text content item:

```json
{
  "schema_version": "spotinfo.error/v1",
  "code": "NO_CANDIDATES",
  "message": "no candidates: gcp publishes no machines in atlantis",
  "cloud": "gcp"
}
```

| Code                     | Meaning                                                                                    |
| ------------------------ | ------------------------------------------------------------------------------------------ |
| `INVALID_ARGUMENT`       | A value outside the documented vocabulary or bounds, or an argument of the wrong JSON type |
| `UNSUPPORTED_CAPABILITY` | A valid request the selected cloud cannot answer                                           |
| `DATA_UNAVAILABLE`       | A recognised cloud whose snapshot is missing or unusable                                   |
| `NO_CANDIDATES`          | A served request with no matching machine                                                  |
| `INTERNAL`               | An unclassified failure                                                                    |

`cloud` echoes the value the caller supplied, or `null` when it could not be read. The MCP
host rejects a `cloud` outside the input enum before the handler runs.

**The CLI does not emit this document.** A CLI failure prints a plain-text line to stderr,
prefixed with `spotinfo:`, and exits 1 — in every `--output` format, including `json`. The
wording is the same as the `message` field:

```console
$ spotinfo recommend --offline --region us-east-1 --architecture x86_64 \
    --min-vcpu 2 --min-memory-gib 4 --machine '^zzzz$' --output json
spotinfo: no candidates: no machine name matches "^zzzz$"; aws publishes c3.2xlarge, c3.xlarge, c5.9xlarge, c5.metal, c5a.2xlarge, c5a.xlarge, c5n.metal, c6a.metal, and more
```

A JSON-RPC `-32602` with `tool 'X' not found` means the tool name is one of the three
retired ones. See [Migration](#migration-from-the-retired-v1-surface).

---

## Placement observations

A placement figure answers "can I actually get this machine right now", which is a different
question from interruption risk. Two clouds publish one, and they publish two different
measurements:

| Kind              | Cloud | Scale         | Fields                                                                          | Notes                                                                             |
| ----------------- | ----- | ------------- | ------------------------------------------------------------------------------- | --------------------------------------------------------------------------------- |
| `placement_score` | AWS   | integer 1-10  | `region_score`, `zone_scores`, `score_fetched_at`                               | EC2 `GetSpotPlacementScores`; needs credentials and `ec2:GetSpotPlacementScores`  |
| `obtainability`   | GCP   | float 0.0-1.0 | `region_obtainability`, `zone_obtainability`, `region_estimated_uptime_seconds` | Beta `compute advice.capacity`; needs a project and ADC; recommendation-side only |

This build declares no placement kind for Azure, and `--with-score` on GCP is
recommendation-side only. Both refusals say so:

```console
$ spotinfo list --cloud azure --with-score
spotinfo: unsupported capability: --with-score is refused on azure: azure publishes a Spot Placement Score, but reading it needs an Azure subscription this build does not authenticate to

$ spotinfo list --cloud gcp --with-score
spotinfo: failed to get spot savings: unsupported capability: gcp obtainability is fetched for a ranked recommendation only, and needs a Google Cloud project
```

**The two kinds are deliberately not normalised onto a shared scale.** A common 1-10 would
invent precision no vendor published: an AWS score is a decile bucket whose boundaries AWS
does not document, and reading `--min-score 8` as "obtainability ≥ 0.8" would be this tool
inventing a correspondence between two vendors' measurements. So the integer filter is
refused rather than mapped, naming the cloud and the kind:

```console
$ spotinfo list --cloud gcp --with-score --min-score 5
spotinfo: unsupported capability: --min-score is refused on gcp: gcp publishes obtainability, and an integer 1-10 floor states no reviewed mapping onto it
```

`--with-score` still answers on such a cloud. Only the filter is refused. The same rule
governs `sort`: `--sort score` orders each kind on its own scale, and leaves a pair of
different kinds unordered rather than ranking one measurement against another.

Only the _regional_ figure orders a page. `--sort score` combined with `--az` is refused,
because asking for one figure per zone leaves no regional figure to order by, and inventing
a maximum or a mean across zones would publish a regional figure the provider declined to
give.

### `placement_status`

Three domain states, one published value.

- A figure present means the lookup ran and produced one.
- `"placement_status": "unavailable"` means the lookup ran and produced nothing. On AWS a
  per-region score failure is non-fatal, so one failed region among many really does yield
  rows in this state.
- Neither key present means nobody asked.

Emitting `"available"` beside a score would say the same thing twice, so the schema declares
the field as a single-valued enum. That is honest about what can appear there today.

---

## `data_source` and provenance

Every answer names where its values came from. The block has exactly four keys —
`provider`, `mode`, `sources` and `sources_omitted` — and a filled-in example is in the
`spotinfo.list/v1` payload above.

`mode` is one of:

| Mode                | Meaning                                                                            |
| ------------------- | ---------------------------------------------------------------------------------- |
| `embedded-snapshot` | Answered from the snapshot compiled into the binary. No request was made.          |
| `cached`            | Answered from a locally cached copy of a fetched document, not revalidated.        |
| `live`              | Answered from the origin this run, or from a copy the origin confirmed with a 304. |

`cached` exists because reporting a cached answer as `live` would be a claim about recency
that nothing verified.

Each `sources[]` entry has six keys, all required, two of them nullable:

| Key              | Notes                                                                                        |
| ---------------- | -------------------------------------------------------------------------------------------- |
| `url`            | The exact document read                                                                      |
| `fetched_at`     | RFC 3339                                                                                     |
| `observed_at`    | Nullable                                                                                     |
| `content_sha256` | 64 lowercase hex, or `null`                                                                  |
| `parser_version` | e.g. `aws-spot-price-json/1`, `gcp-pricing-html/1`, `azure-retail-prices/2`                  |
| `schema_version` | The **source** document's schema, e.g. `aws.spot-price-feed/v1`, `spotinfo.azure-catalog/v2` |

`content_sha256` is `null` when the reviewed document was not fetched as stable bytes — the
AWS architecture snapshot is one, its source being a documentation page rather than a
fetched payload. The field is never filled with the derived snapshot hash, because the
published hash has to stay verifiable by re-fetching the source. A consumer that verifies
provenance must test the value, not the key.

**Trimming.** `sources` lists the documents the published rows draw their values from, not
every document the snapshot was built from. Azure reads 81 — one Retail Prices query per
region and one Microsoft Learn page per machine series — so a three-row answer would
otherwise be mostly provenance for rows it does not contain. `sources_omitted` counts what
was read and not listed; a two-source Azure recommendation reports `79`. A source whose
scope a provider cannot derive from its URL is always listed, so trimming can never drop the
provenance of a value a published row carries. `list_cloud_regions` returns the untrimmed
list.

---

## Per-cloud constraints

[clouds.md](clouds.md) is the single source for coverage counts. What matters to an
integrator is which requests are refused.

**AWS** — the full surface. Linux and Windows, x86_64 and arm64, 34 regions, all four
workloads, `interruption_bucket` risk on every candidate, `placement_score` behind
credentials. `--live-risk`, `--gcp-project` and `--gcp-billing-key` are refused.

**GCP** — Linux only, one snapshot region, opt-in live risk.

- OS: `linux` only. `os: "windows"` returns `UNSUPPORTED_CAPABILITY`.
- Workload: `cost` only. `web`, `ci` and `batch` return `UNSUPPORTED_CAPABILITY`.
- Risk: `status: "unavailable"` on every candidate. `--live-risk` fetches a per-project
  preemption rate and makes it _visible_, never filterable — see below.
- Regions: `us-central1` in the committed snapshot. A valid `--gcp-billing-key` /
  `SPOTINFO_GCP_BILLING_KEY` prices regions beyond it, for that invocation only; the key
  never enters a snapshot.
- Placement: `obtainability`, on a ranked page only, needing `--gcp-project` /
  `GOOGLE_CLOUD_PROJECT`.

**Azure** — both operating systems, no risk figure, no placement figure.

- OS: `linux` **and** `windows`. Both are served, on both commands and both surfaces.
- Workload: `cost` only.
- Risk: `status: "unavailable"` on every candidate. Eviction rates need a subscription this
  build does not authenticate to, and it links no Azure credential library.
- Placement: none. `--with-score` is refused.
- Regions: 55. `machine` is the full Azure size name, e.g. `Standard_D2s_v5`.

**Unknown regions differ by command, not by cloud.** `spotinfo list` treats an unmatched
region as an empty result — exit 0, `candidates: []`, a `WARN` on stderr — while
`recommend` refuses:

```console
$ spotinfo recommend --cloud gcp --architecture x86_64 --min-vcpu 2 --min-memory-gib 4 --region atlantis
spotinfo: no candidates: gcp publishes no machines in atlantis
```

On AWS the region list is validated during acquisition instead, so an unknown region is
`region not found: atlantis` on both commands.

### Live GCP preemption risk

`--live-risk` calls `compute/beta advice.capacityHistory` with Application Default
Credentials and attaches a `preemption_rate` risk to the ranked page. It is GCP-only,
opt-in, and never enters a snapshot: the figure is per-project advisory data, so it is not
redistributable and the GCP source contract does not name it.

`preemption_rate` is deliberately **not** accepted by `--workload web|ci|batch`. Google
measures (preempted Spots) / (Spots that stopped running); AWS measures the fraction of
_running_ instances interrupted. Live risk makes the figure visible, not filterable, and
that asymmetry is the point of the kind vocabulary.

A failed lookup is a warning, not a refusal: the run exits 0, warns on stderr, and the page
reports the snapshot's `risk.status` unchanged.

---

## MCP server

- **Protocol version**: `2024-11-05`
- **Server name**: `spotinfo`
- **Transport**: stdio by default. `MCP_TRANSPORT=sse` with `MCP_PORT` (default `8080`)
  selects SSE.
- **Start**: `spotinfo --mcp`, or set `SPOTINFO_MODE=mcp` and run `spotinfo` with no
  arguments. Both serve the same three tools.
- **Capabilities**: `tools` (with `listChanged`) and `logging`.

All three tools declare `readOnlyHint: true`, `destructiveHint: false`,
`idempotentHint: true`, `openWorldHint: true`, and `additionalProperties: false` on their
input schema.

`--offline` is **not** a global flag and does not compose with `--mcp`. Pass the per-call
`offline: true` argument instead.

### `list_cloud_regions`

Required: none.

| Argument | Type   | Default | Notes                   |
| -------- | ------ | ------- | ----------------------- |
| `cloud`  | string | `aws`   | `aws`, `azure` or `gcp` |

Returns `spotinfo.regions/v1`.

### `list_spot_machines`

Required: none. Returns `spotinfo.list/v1`.

| Argument         | Type            | Default   | Bounds / enum                                            |
| ---------------- | --------------- | --------- | -------------------------------------------------------- |
| `cloud`          | string          | `aws`     | `aws`, `azure`, `gcp`                                    |
| `regions`        | array of string | `["all"]` | `minItems: 1`, unique, each non-empty                    |
| `machine`        | string          | `""`      | RE2 pattern                                              |
| `architecture`   | string          | —         | `x86_64`, `arm64`; omit for no filter                    |
| `os`             | string          | `linux`   | `linux`, `windows`                                       |
| `min_vcpu`       | integer         | —         | `minimum: 0`                                             |
| `min_memory_gib` | number          | —         | `minimum: 0`                                             |
| `max_price`      | number          | —         | `exclusiveMinimum: 0`                                    |
| `sort`           | string          | —         | `machine`, `price`, `region`, `risk`, `savings`, `score` |
| `order`          | string          | `asc`     | `asc`, `desc`                                            |
| `offline`        | boolean         | `false`   | Answer from the committed snapshots, no request          |
| `refresh`        | boolean         | `false`   | Ignore any locally cached document for this call         |
| `with_score`     | boolean         | `false`   | Include placement figures (experimental)                 |
| `min_score`      | integer         | —         | `0`-`10`; needs `with_score`                             |
| `az`             | boolean         | `false`   | Zone-level instead of region-level; needs `with_score`   |
| `score_timeout`  | integer         | `30`      | `1`-`300` seconds; needs `with_score`                    |

`min_score`, `az` and `score_timeout` are refused on **presence**, not on value — passing
`min_score: 0` without `with_score` is refused too. The wording matches the CLI's, without
the `--` prefix:

```json
{
  "schema_version": "spotinfo.error/v1",
  "code": "INVALID_ARGUMENT",
  "message": "invalid argument: az needs with_score, which is what fetches the placement scores it splits by zone",
  "cloud": "aws"
}
```

### `recommend_spot_machines`

Required: `architecture`, `min_vcpu`, `min_memory_gib`. Returns `spotinfo.recommend/v3`.

| Argument         | Type            | Default   | Bounds / enum                |
| ---------------- | --------------- | --------- | ---------------------------- |
| `cloud`          | string          | `aws`     | `aws`, `azure`, `gcp`        |
| `regions`        | array of string | `["all"]` | `minItems: 1`, unique        |
| `machine`        | string          | `""`      | RE2 pattern                  |
| `architecture`   | string          | required  | `x86_64`, `arm64`            |
| `os`             | string          | `linux`   | `linux`, `windows`           |
| `min_vcpu`       | integer         | required  | `minimum: 1`                 |
| `min_memory_gib` | number          | required  | `exclusiveMinimum: 0`        |
| `max_price`      | number          | —         | `exclusiveMinimum: 0`        |
| `workload`       | string          | `cost`    | `cost`, `web`, `ci`, `batch` |
| `top`            | integer         | `3`       | `1`-`50`                     |
| `offline`        | boolean         | `false`   |                              |
| `refresh`        | boolean         | `false`   |                              |

It declares **no** `sort`, `order`, `with_score`, `min_score`, `az`, `score_timeout` or
`live_risk` argument. A ranked page's placement figures and live risk are CLI-only.

### CLI flag to MCP argument

Strip the leading `--` and replace `-` with `_`. The one exception is the repeatable
`--region`, which becomes the array `regions`. `--output` has no argument (MCP is always
JSON), and `--gcp-project` and `--gcp-billing-key` have none by design — on that surface
both come from the environment.

### Driving it by hand

```bash
printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"probe","version":"1"}}}' \
  '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' \
  | spotinfo --mcp
```

---

## Migration from the retired v1 surface

### Tools

| Retired                    | Now                       | Note                         |
| -------------------------- | ------------------------- | ---------------------------- |
| `find_spot_instances`      | `list_spot_machines`      | Now serves three clouds      |
| `list_spot_regions`        | `list_cloud_regions`      | Now takes a `cloud` argument |
| `recommend_spot_instances` | `recommend_spot_machines` | `spotinfo.recommend/v3`      |

Calling a retired name is a JSON-RPC error, not a payload:

```json
{
  "code": -32602,
  "message": "tool 'find_spot_instances' not found: tool not found"
}
```

### Arguments

| Retired                                          | Now                                                                                               |
| ------------------------------------------------ | ------------------------------------------------------------------------------------------------- |
| `instance_types`                                 | `machine`                                                                                         |
| `min_memory_gb`                                  | `min_memory_gib`                                                                                  |
| `max_price_per_hour`                             | `max_price`                                                                                       |
| `max_interruption_rate`                          | No replacement. Use `workload` (`web`, `ci`, `batch`) on `recommend_spot_machines`                |
| `sort_by`                                        | `sort` plus `order`. `reliability` is now `risk`; `price`, `savings` and `score` keep their words |
| `limit`                                          | `top` on `recommend_spot_machines`. `list_spot_machines` returns every match                      |
| `min_vcpu`, `regions`                            | Unchanged                                                                                         |
| `with_score`, `min_score`, `az`, `score_timeout` | Unchanged on `list_spot_machines`; not declared on `recommend_spot_machines`                      |

### Response fields

| Retired                                                         | Now                                                       |
| --------------------------------------------------------------- | --------------------------------------------------------- |
| `instance_type`, `instance`                                     | `machine`                                                 |
| `spot_price_per_hour` (number)                                  | `spot_usd_per_hour` (decimal string, 9 fractional digits) |
| `price` (number, root v1 JSON)                                  | `spot_usd_per_hour`                                       |
| `savings_percentage` (tool), `savings` (root v1 JSON, integer) | `savings_percent`                                         |
| `interruption_frequency`, `interruption_range`, `range.label`   | `risk.label`                                              |
| `interruption_rate`, `range.min`, `range.max`                   | `risk.min_percent`, `risk.max_percent`                    |
| `memory_gb`, `info.ram_gb`                                      | `memory_gib` (renamed; the figure is GiB)                 |
| `vcpu`, `info.cores`                                            | `vcpu`                                                    |
| `zone_price`                                                    | `zone_prices` (decimal strings)                           |
| `metadata.regions_searched`                                     | `request.regions`                                         |
| `metadata.data_source`, `metadata.data_freshness`               | `data_source.provider`, `data_source.mode`                |
| `region_score`, `zone_scores`, `score_fetched_at`, `live_price` | Unchanged                                                 |

Gone with no replacement:

- `spot_price` (`"$0.0104/hour"`), the tool's string `savings`
  (`"68% cheaper than on-demand"`) and `specs` (`"1 vCPU, 1 GB RAM"`) — pre-formatted display
  strings. Format from the numbers.
- `reliability_score` — it was `100 - interruption_rate`, a derived figure no cloud
  publishes. Read `risk` instead, which states its own availability.
- `metadata.total_results` — count `candidates`. `metadata.query_time_ms` — measure it.
- **`info.emr`** — and the whole `info` block with it. EMR compatibility is an AWS Spot
  Advisor classification with no meaning on another cloud, so the provider-neutral candidate
  carries no equivalent and the field is retired with the schema rather than published as a
  false `false`. `vcpu` and `memory_gib` are now top-level candidate fields.

### Vocabulary

The neutral vocabulary is shared by both surfaces. An old CLI flag is refused with a rename
hint rather than "flag provided but not defined":

```console
$ spotinfo list --offline --type m5.large
spotinfo: invalid argument: --type was renamed to --machine
```

| Retired flag               | Now                |
| -------------------------- | ------------------ |
| `--type`, `--instance`     | `--machine`        |
| `--vcpu`, `--cpu`          | `--min-vcpu`       |
| `--memory`, `--memory-gib` | `--min-memory-gib` |
| `--price`, `--budget`      | `--max-price`      |

There is also no root query command any more. `spotinfo` with no subcommand prints help and
exits 1; use `spotinfo list`.

---

## Data sources and freshness

Every snapshot is compiled into the binary, so every cloud answers offline. See
[data-sources.md](data-sources.md) for the full treatment, including each source contract.

**AWS** — the Spot Instance Advisor feed and the Spot pricing feed, refreshed weekly by the
`update-data` workflow. `DescribeSpotPriceHistory` is a live fallback for instance types the
static feed prices at zero; a candidate priced that way carries `live_price: true`.

**GCP** — Google's server-rendered Spot and Compute pricing pages, refreshed weekly by
`update-gcp-data`. With a Cloud Billing Catalog API key (`--gcp-billing-key` or
`SPOTINFO_GCP_BILLING_KEY`) a run can also price regions the snapshot does not carry; that
answer reports `mode` `live` or `cached` and appends one more `sources[]` entry, under the
parser version `gcp-billing-catalog/1`, so a composed price is never published under the
scrape's provenance. A key that is rejected degrades to the snapshot and still answers:

```console
$ spotinfo --debug list --cloud gcp --region us-central1 --gcp-billing-key bogus-key-123 --machine '^n2-standard-2$' --output text
… level=DEBUG msg="gcp billing catalogue unavailable; answering from the committed snapshot" error="billing catalogue request was refused: 400 Bad Request: …"
machine=n2-standard-2, vCPU=2, memory=8GiB, saving=47%, risk='unavailable', price=0.0507
```

**Azure** — the anonymous Azure Retail Prices API for amounts, joined to Microsoft Learn VM
size pages for vCPU, memory and architecture; refreshed weekly by `update-azure-data`. The
Retail Prices API needs no credential, so an explicit region can be priced live at runtime.

`offline: true` answers from the embedded snapshots and makes no request at all.
`refresh: true` ignores any cached document for that call.

---

## Version compatibility

- **MCP protocol**: `2024-11-05`.
- **Payload documents**: `spotinfo.list/v1`, `spotinfo.recommend/v3`, `spotinfo.regions/v1`,
  `spotinfo.error/v1`. Each is versioned in its own `schema_version` field; a breaking change
  raises that number rather than mutating a published shape in place.
- **Retired**: `spotinfo.recommend/v1` and `spotinfo.recommend/v2`, and the three v1 tool
  names. This release breaks compatibility with all of them. See
  [Migration](#migration-from-the-retired-v1-surface).

For setup instructions, see the [Claude Desktop integration guide](claude-desktop-setup.md).
