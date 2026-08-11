# Cloud coverage

This page is the single source for what each cloud serves. Other documents state a count only
when they need it, and link here. The region list for Azure is enumerated in
[data-sources.md](data-sources.md). This page gives the count and the reasoning.

Counts come from the committed snapshots at commit `06220c6`. A weekly workflow refreshes
them through a reviewed pull request, so a count can move. The capability rules below do not
move without a code change.

## Matrix

|                          | AWS                       | GCP                              | Azure                    |
| ------------------------ | ------------------------- | -------------------------------- | ------------------------ |
| `spotinfo recommend`     | yes                       | yes                              | yes                      |
| `spotinfo` query command | yes                       | no                               | no                       |
| Regions in the snapshot  | 34 (Advisor), 40 (prices) | 1 — `us-central1`                | 55                       |
| Machine types            | 1,192                     | 333                              | 224                      |
| Machine series           | not enumerated            | 18                               | 26                       |
| Architectures            | x86_64, arm64             | x86_64 (275), arm64 (58)         | x86_64 (187), arm64 (37) |
| Operating systems        | linux, windows            | linux                            | linux, windows           |
| Interruption risk        | published buckets         | `unavailable`, or opt-in live    | `unavailable`            |
| Workloads accepted       | cost, web, ci, batch      | cost                             | cost                     |
| On-Demand price in v2    | no                        | yes                              | yes                      |
| Placement scores         | yes                       | no                               | no                       |
| Live price fallback      | yes                       | no                               | no                       |
| Credentials              | optional                  | optional, for `--live-risk` only | none                     |

Every limit on this page is either a vendor limit or a reviewed decision.
[reviews/multicloud-parity.md](reviews/multicloud-parity.md) says which is which, and what it
takes to remove each one.

## Why the risk column differs

`--workload web`, `ci` and `batch` cap the maximum of an interruption bucket at 5%, 16% and
22%. Those numbers are AWS Spot Advisor bucket boundaries. A ceiling is meaningful only
against the measurement it was drawn from.

GCP and Azure publish no interruption figure that can ship in a snapshot, so both report
`risk.status: "unavailable"` on every candidate. The value is never a zero and never a low
bucket. A neutral ranking that scores silence as safety prefers the cloud that measures
least.

`--workload cost` applies no interruption constraint. It is the only workload every cloud
accepts.

## AWS

**Entry points.** Both. `spotinfo recommend --cloud aws` ranks machines. Bare `spotinfo`
queries the Advisor, renders an interruption column, and reads placement scores.

**Data.** Three sources. The Spot Advisor document and the Spot pricing feed ship inside the
binary and are also fetched live. `DescribeSpotPriceHistory` fills prices the static feed
records as $0, which happens for very new families and for the regions AWS omits.

**Fetch order.** Fresh cache, then the origin, then an expired cache entry, then the
committed snapshot. `--offline` skips every request, including the live-price call.
`--refresh` ignores the cache for one run. See [data-sources.md](data-sources.md).

**Schema.** Every workload emits `spotinfo.recommend/v3`, on every cloud and on both
surfaces. The AWS-only `spotinfo.recommend/v1` document, and the workload that used to
select between it and the neutral schema, are retired. See
[reviews/cli-and-mcp-surface-review.md](reviews/cli-and-mcp-surface-review.md).

**Placement scores.** `--with-score` reads `GetSpotPlacementScores`. A score rates the whole
request, not one machine, so a wider `--type` pattern scores higher than a narrow one.
[aws-spot-placement-scores.md](aws-spot-placement-scores.md) explains what a score does and
does not tell you.

## GCP

**Entry point.** `spotinfo recommend --cloud gcp` only. The query command renders an
interruption column, which GCP cannot fill:

```console
$ spotinfo --cloud gcp --type "n2.*"
spotinfo: gcp: unsupported capability: risk; the query command renders interruption risk and is AWS-only; use "spotinfo recommend --cloud gcp" instead
```

**Regions.** `us-central1` only. Google publishes Spot prices per region through a page
selector, and the committed snapshot covers the one reviewed region. Any other region returns
`NO_CANDIDATES`. An unset `--region` expands to every published region, which is this one.

**Machines.** 333 types in 18 series: `c2`, `c3`, `c3d`, `c4`, `c4a`, `c4d`, `e2`, `m1`,
`m2`, `m3`, `n1`, `n2`, `n2d`, `n4`, `n4a`, `n4d`, `t2a`, `t2d`. The Arm series are `c4a`,
`n4a` and `t2a`.

**Operating systems.** Linux only. `--os windows` returns `UNSUPPORTED_CAPABILITY`.

**Prices.** Every machine carries a Spot price, a paired On-Demand price and a derived
savings percent. The catalogue ships in the binary and makes no request at run time.

### Live preemption risk

Google publishes its preemption rate only through the authenticated, per-project
`compute.advice.capacityHistory` API. The answer differs per caller, so it cannot ship in a
snapshot. `--live-risk` fetches it for one invocation:

```bash
spotinfo recommend --cloud gcp --architecture x86_64 --cpu 4 --memory 16 \
  --live-risk --gcp-project my-project
```

```
RANK  CLOUD  REGION       MACHINE         ... USD/HOUR  SAVINGS  RISK
   1  gcp    us-central1  c3d-standard-4  ... 0.042496      76%  6.3% avg
   2  gcp    us-central1  n2d-standard-4  ... 0.053824      68%  17.5% avg
```

Rules:

- **The default path stays offline.** It answers in about a tenth of a second.
- **The project is never guessed.** Pass `--gcp-project`, or set `GOOGLE_CLOUD_PROJECT`. The
  call is billed to whichever project it names, so gcloud's ambient `core/project` is
  deliberately not read.
- **Credentials** come from Application Default Credentials: `gcloud auth
application-default login`, a service account, or the GCE metadata server. Without them the
  command still answers and reports `unavailable`.
- **One call per recommendation**, against the ranked page only, never the catalogue.
- **A page that fetched nothing says so.** One warning goes to stderr, naming the first
  cause. `unavailable` alone cannot be told apart from "Google has no history for these
  machines". [troubleshooting.md](troubleshooting.md) tables the cases.
- **The figure is visible, not filterable.** Its kind is `preemption_rate`, not
  `interruption_bucket`. Google divides preempted Spots by Spots that stopped running. AWS
  publishes the fraction of _running_ instances interrupted. The two are not comparable, so
  `--workload web|ci|batch` still refuses on GCP.

## Azure

**Entry point.** `spotinfo recommend --cloud azure` only, for the same reason as GCP.

**Regions.** 55. They are enumerated in
[data-sources.md](data-sources.md#7-azure-retail-prices-api-and-microsoft-learn-vm-size-pages)
and in `support.regions` of `internal/providers/azure/data/source-contract.json`. Any other
region returns `NO_CANDIDATES`. An unset `--region` searches all 55.

**Machines.** 224 sizes in 26 series. 37 sizes are arm64, in `bpsv2`, `dpdsv5`, `dpsv5`,
`dpsv6` and `epsv5`. `--instance` accepts the full Azure size name, for example
`Standard_D2s_v5`.

**Operating systems.** Linux and Windows. Azure prices most sizes twice — once bare and once
with a Windows licence — and each is a separate row keyed by machine, region and OS, so a
Windows Spot price is only ever compared against the Windows list price. 196 of the 224 sizes
carry a Windows meter; the Arm ones do not.

**Prices.** The anonymous Azure Retail Prices API supplies the amounts. Microsoft Learn size
pages supply vCPU, memory and processor architecture. Architecture is read from each page's
`[x86-64]` or `[Arm64]` marker. It is never inferred from a size name. An Arm size shipped as
`x86_64` passes every other gate, and then recommends a machine that cannot run the caller's
binaries.

**Risk.** Azure publishes eviction rates only through Resource Graph and Resource SKUs, and
both need a subscription. Every candidate reports `risk.status: "unavailable"`.

## Refreshing a snapshot

A weekly workflow owns each provider directory and opens one pull request. To refresh by
hand:

```bash
make update-data update-price   # AWS
make update-gcp-data            # GCP
make update-azure-data          # Azure
make verify-data                # all of them
```

Each updater validates against the source contract, the manifest hash and the reviewed
coverage floor **before** it writes, so a failed run leaves the reviewed snapshot untouched.

Two rules matter most:

1. The coverage floor is a floor, not a census. For Azure it applies per region. Never lower
   it to make a short refresh pass.
2. Never widen a parser to make a changed source fit. Review the source, then raise
   `parser_version` in both the parser and the source contract.

`make update-gcp-data` can stop with `gcp pricing page is not serving a stable document` and
exit code 75. That means wait and retry. Google serves those pages from a CDN that can hold
more than one generation at once, and the updater refuses a snapshot that mixes two.
[data-sources.md](data-sources.md) records the incident that made the check necessary.
