# Cloud coverage

This page is the single source for what each cloud serves. Other documents state a count only
when they need it, and link here. The region list for Azure is enumerated in
[data-sources.md](data-sources.md). This page gives the count and the reasoning.

Counts come from the committed snapshots at commit `2f2b80b`, read out of the binary rather
than out of a data file. A weekly workflow refreshes them through a reviewed pull request, so
a count can move. The capability rules below do not move without a code change.

## Matrix

|                         | AWS                          | GCP                                                                 | Azure                               |
| ----------------------- | ---------------------------- | ------------------------------------------------------------------- | ----------------------------------- |
| `spotinfo list`         | yes                          | yes                                                                 | yes                                 |
| `spotinfo recommend`    | yes                          | yes                                                                 | yes                                 |
| Regions in the snapshot | 34                           | 1 — `us-central1`                                                   | 55                                  |
| Machine types           | 1,155                        | 333                                                                 | 224                                 |
| Machine series          | not enumerated               | 18                                                                  | 26                                  |
| Architectures           | x86_64 (807), arm64 (348)    | x86_64 (275), arm64 (58)                                            | x86_64 (187), arm64 (37)            |
| Operating systems       | linux, windows               | linux                                                               | linux, windows                      |
| Interruption risk       | published buckets            | `unavailable`, or opt-in live                                       | `unavailable`                       |
| Workloads accepted      | cost, web, ci, batch         | cost                                                                | cost                                |
| On-Demand price in JSON | `null`                       | yes                                                                 | yes                                 |
| Placement figures       | score, 1-10                  | obtainability, beta, `recommend` only                               | published, but needs a subscription |
| Live price path         | yes                          | with an API key                                                     | yes, 1-2 named regions              |
| Credentials             | optional, for `--with-score` | optional, for `--live-risk`, `--with-score` and `--gcp-billing-key` | none                                |

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

```console
$ spotinfo list --cloud azure --region westeurope --machine '^Standard_D2as_v5$' --output text
machine=Standard_D2as_v5, vCPU=2, memory=8GiB, saving=81%, risk='unavailable', price=0.0192
```

`--workload cost` applies no interruption constraint. It is the only workload every cloud
accepts.

## AWS

**Entry points.** Both. `spotinfo list --cloud aws` browses the catalogue and
`spotinfo recommend --cloud aws` ranks it. `aws` is the default `--cloud`, so both work with
the flag omitted. There is no root query command: bare `spotinfo` prints its help and exits 1.

**Data.** Three sources. The Spot Advisor document and the Spot pricing feed ship inside the
binary and are also fetched live. `DescribeSpotPriceHistory` fills prices the static feed
records as $0, which happens for very new families and for the regions AWS omits.

**Fetch order.** Fresh cache, then the origin, then an expired cache entry, then the
committed snapshot. `--offline` skips every feed request. `--refresh` ignores the cache for
one run. See [data-sources.md](data-sources.md).

**Risk.** The only cloud with a published interruption figure in its snapshot. The column
prints one of five Advisor buckets — `<5%`, `5-10%`, `10-15%`, `15-20%`, `>20%` — and JSON
carries the bucket edges beside the label:

```console
$ spotinfo list --offline --region us-east-1 --machine '^m5\.large$' --output text
machine=m5.large, vCPU=2, memory=8GiB, saving=59%, risk='>20%', price=0.0399
```

**On-Demand price.** Absent. The Advisor feed publishes a savings percent, not the list price
it was derived from, so `on_demand_usd_per_hour` is `null` on every AWS candidate while
`savings_percent` is populated. GCP and Azure publish both halves and carry both.

**Schema.** `spotinfo list` emits `spotinfo.list/v1` and `spotinfo recommend` emits
`spotinfo.recommend/v3`, on every cloud and on both surfaces. The AWS-only
`spotinfo.recommend/v1` document, and the workload that used to select between it and the
neutral schema, are retired. See
[reviews/cli-and-mcp-surface-review.md](reviews/cli-and-mcp-surface-review.md).

**Placement scores.** `--with-score` reads `GetSpotPlacementScores`, which is an AWS API and
an AWS-only path — the other two clouds answer a different question, or none. A score rates
the whole request, not one machine, so a wider `--machine` pattern scores higher than a narrow
one. `--min-score` is an integer 1-10 floor and is accepted here because AWS is the cloud that
publishes an integer 1-10 score. Credentials and `ec2:GetSpotPlacementScores` are required;
`--offline` does not suppress the call.
[aws-spot-placement-scores.md](aws-spot-placement-scores.md) explains what a score does and
does not tell you.

## GCP

**Entry points.** Both. `spotinfo list --cloud gcp` browses the catalogue and
`spotinfo recommend --cloud gcp` ranks it. The risk column prints its status, never a number
GCP did not publish:

```console
$ spotinfo list --cloud gcp --machine "^n2-standard-4$" --output text
region=us-central1, machine=n2-standard-4, vCPU=4, memory=16GiB, saving=47%, risk='unavailable', price=0.1013
```

**Regions.** The committed snapshot carries `us-central1` only. Google publishes Spot prices
per region through a page selector, and one region is what it server-renders. An unset
`--region` expands to every region the snapshot has, which is this one — **unless
`--gcp-billing-key`, or `SPOTINFO_GCP_BILLING_KEY`, prices another one from the Cloud Billing
Catalog API for that one invocation.** A key that the API refuses is not an error: the run
logs the refusal at `--debug` and answers from the snapshot. Nothing fetched this way enters
the snapshot; Google does not state redistribution terms for the catalogue, which is why it
stays a runtime path.

Naming an uncovered region is not an error on `list` — it is an empty page and a warning —
while `recommend` refuses, because a ranked page with nothing in it is not an answer:

```console
$ spotinfo list --cloud gcp --region europe-west1 --output text
time=2026-08-12T01:10:51.653+03:00 level=WARN msg="no machines matched the query" filters="[region=europe-west1]"

$ spotinfo recommend --cloud gcp --architecture x86_64 --min-vcpu 2 --min-memory-gib 4 --region europe-west1
spotinfo: no candidates: gcp publishes no machines in europe-west1
```

**Machines.** 333 types in 18 series: `c2`, `c3`, `c3d`, `c4`, `c4a`, `c4d`, `e2`, `m1`,
`m2`, `m3`, `n1`, `n2`, `n2d`, `n4`, `n4a`, `n4d`, `t2a`, `t2d`. The Arm series are `c4a`,
`n4a` and `t2a`.

**Operating systems.** Linux only, and that is Google's limit rather than a missing feature —
see [What stays refused](#what-stays-refused-and-why).

```console
$ spotinfo list --cloud gcp --os windows
spotinfo: gcp: unsupported capability: os windows: this cloud publishes spot prices for linux only
```

**Prices.** Every machine carries a Spot price, a paired On-Demand price and a derived
savings percent. The catalogue ships in the binary and, without a billing key, makes no
request at run time.

### Live preemption risk

Google publishes its preemption rate only through the authenticated, per-project
`compute.advice.capacityHistory` API. The answer differs per caller, so it cannot ship in a
snapshot. `--live-risk` fetches it for one invocation:

```bash
spotinfo recommend --cloud gcp --architecture x86_64 --min-vcpu 4 --min-memory-gib 16 \
  --live-risk --gcp-project my-project
```

Rules:

- **The default path stays offline.** It answers in about a twentieth of a second.
- **The project is never guessed.** Pass `--gcp-project`, or set `GOOGLE_CLOUD_PROJECT`. The
  call is billed to whichever project it names, so gcloud's ambient `core/project` is
  deliberately not read. Without either, the flag is refused before anything is fetched:

  ```console
  $ spotinfo recommend --cloud gcp --architecture x86_64 --min-vcpu 2 --min-memory-gib 4 --live-risk
  spotinfo: invalid argument: --live-risk needs a project; pass --gcp-project or set GOOGLE_CLOUD_PROJECT
  ```

- **Credentials** come from Application Default Credentials: `gcloud auth
application-default login`, a service account, or the GCE metadata server.
- **One call per recommendation**, against the ranked page only, never the catalogue.
- **A page that fetched nothing still answers.** A failed lookup is a warning on stderr and
  `risk=unavailable` on the page, at exit 0 — never a failed run. `unavailable` alone cannot
  be told apart from "Google has no history for these machines".
  [troubleshooting.md](troubleshooting.md) tables the cases.
- **The figure is visible, not filterable.** Its kind is `preemption_rate`, not
  `interruption_bucket`. Google divides preempted Spots by Spots that stopped running. AWS
  publishes the fraction of _running_ instances interrupted. The two are not comparable, so
  `--workload web|ci|batch` still refuses on GCP.

### Obtainability

`--with-score` on GCP does not read a placement score. Google's beta
`compute.advice.capacity` API returns an **obtainability** probability between 0.0 and 1.0,
which is a different measurement with a different scale. Three consequences, all of them
visible at the command line:

- It is fetched for a **ranked page** only, so `--with-score` is refused on `list`, and
  `--sort score` with it:

  ```console
  $ spotinfo list --cloud gcp --sort score
  spotinfo: failed to get spot savings: unsupported capability: gcp cannot order a catalogue by obtainability, which it fetches for a ranked page instead
  ```

- It needs a project, for the same billing reason `--live-risk` does:

  ```console
  $ spotinfo recommend --cloud gcp --architecture x86_64 --min-vcpu 2 --min-memory-gib 4 --with-score
  spotinfo: invalid argument: --with-score needs a project; pass --gcp-project or set GOOGLE_CLOUD_PROJECT
  ```

- `--min-score` is refused. An integer 1-10 floor states no reviewed mapping onto a
  probability, and inventing one would filter on a number nobody agreed the meaning of.

A run that has the project but cannot reach the API degrades the way `--live-risk` does: a
warning on stderr, `unavailable` in the `SCORE` column, exit 0.

## Azure

**Entry points.** Both, as on GCP. The risk column prints `unavailable`.

**Regions.** 55. They are enumerated in
[data-sources.md](data-sources.md#7-azure-retail-prices-api-and-microsoft-learn-vm-size-pages)
and in `support.regions` of `internal/providers/azure/data/source-contract.json`. An unset
`--region` searches all 55. As on GCP, an uncovered region is an empty page on `list` and
`spotinfo: no candidates: azure publishes no machines in atlantis` on `recommend`.

**Machines.** 224 sizes in 26 series. 37 sizes are arm64, in `bpsv2`, `dpdsv5`, `dpsv5`,
`dpsv6` and `epsv5`. `--machine` accepts the full Azure size name, for example
`^Standard_D2s_v5$`.

**Operating systems.** Linux and Windows. Azure prices most sizes twice — once bare and once
with a Windows licence — and each is a separate row keyed by machine, region and OS, so a
Windows Spot price is only ever compared against the Windows list price. 196 of the 224 sizes
carry a Windows meter; the Arm ones do not.

```console
$ spotinfo list --cloud azure --region westeurope --machine '^Standard_D2as_v5$' --os windows --output text
machine=Standard_D2as_v5, vCPU=2, memory=8GiB, saving=81%, risk='unavailable', price=0.0362
```

**Prices.** The anonymous Azure Retail Prices API supplies the amounts. Microsoft Learn size
pages supply vCPU, memory and processor architecture. Architecture is read from each page's
`[x86-64]` or `[Arm64]` marker. It is never inferred from a size name. An Arm size shipped as
`x86_64` passes every other gate, and then recommends a machine that cannot run the caller's
binaries.

**Risk.** Azure publishes eviction rates only through Resource Graph and Resource SKUs, and
both need a subscription. Every candidate reports `risk.status: "unavailable"`.

**Placement.** Azure does publish a Spot Placement Score, and reading it needs a subscription
this build does not authenticate to. The refusal says exactly that, because "Azure has no
placement figure" would be false:

```console
$ spotinfo list --cloud azure --with-score
spotinfo: unsupported capability: --with-score is refused on azure: azure publishes a Spot Placement Score, but reading it needs an Azure subscription this build does not authenticate to
```

**Live prices.** Naming one or two regions refreshes their prices from the same anonymous
Retail Prices API before answering — no credential, and no source the contract does not
already name. `data_source.mode` then reads `live`, or `cached` when a copy inside the
24-hour window answered. Everything else is the committed catalogue: `--offline`,
`--region all` (the default), three or more regions, and any region outside the 55. A sweep
that fails for any reason is discarded whole and the snapshot answers, never an error. The
cost is what bounds it — one region is 10 pages and 5.5 MB. See
[data-sources.md](data-sources.md#7-azure-retail-prices-api-and-microsoft-learn-vm-size-pages).

```console
$ spotinfo list --cloud azure --region westeurope --refresh --machine '^Standard_D2as_v5$' --output json | jq -r .data_source.mode
live
```

## What stays refused, and why

These questions are refused on a cloud that cannot answer them, and none of them is a feature
waiting to be built. Each message names the limit, so a reader can tell "no vendor publishes
this" from "this build does not serve it".
[reviews/multicloud-parity.md](reviews/multicloud-parity.md) carries the verdicts and the
vendor citations.

**Windows on GCP.** Google's Spot pricing pages publish no Windows Spot line. The licence is
priced on a page the source contract does not name, and pairing the two would join documents
the parser cannot check against each other.

```console
$ spotinfo recommend --cloud gcp --os windows --architecture x86_64 --min-vcpu 2 --min-memory-gib 4
spotinfo: gcp: unsupported capability: os windows: this cloud publishes spot prices for linux only
```

**Zone-level figures on GCP and Azure.** The Azure Retail Prices API publishes region-level
amounts, and Google publishes Spot prices per region. Both vendors' placement APIs do accept a
zone, which is why the message is about prices and not about zones in general.

```console
$ spotinfo list --cloud gcp --with-score --az
spotinfo: gcp: unsupported capability: zone_detail: this cloud publishes prices per region, not per zone, and only its region-level figures are served here
```

**`--workload web`, `ci` and `batch` on GCP and Azure.** The 5%, 16% and 22% ceilings are AWS
Spot Advisor bucket boundaries. Google's preemption rate divides preempted Spots by Spots that
stopped running; Azure's eviction rate is a per-hour probability over 7 days. Neither is the
fraction of running machines interrupted over 30 days that the ceilings were drawn from, so
the ceiling has no meaning against them. This survives both deferred Azure features: shipping
the eviction rate would make the figure visible, never filterable.

```console
$ spotinfo recommend --cloud azure --workload web --architecture x86_64 --min-vcpu 2 --min-memory-gib 4
spotinfo: azure: unsupported capability: risk: the web workload caps interruption frequency at 5%, an AWS Spot Advisor bucket boundary, and azure publishes no figure measured that way; workload cost applies no ceiling and answers on every cloud
```

`--workload cost` is the answer in every case. It applies no interruption constraint, so it
claims nothing a cloud did not measure.

**`--sort risk` on GCP and Azure `list`.** There is no risk figure in either catalogue to
order rows by.

```console
$ spotinfo list --cloud gcp --sort risk
spotinfo: gcp: unsupported capability: risk: this cloud's catalogue carries no risk figure
```

`recommend --sort risk` on the same cloud is **not** refused: it exits 0 and prints the ranked
page. `list --sort risk` orders a whole catalogue by a column that is `unavailable` in every
row, which is a request with no answer; `recommend --sort` re-orders a page the ranking policy
already chose, so an absent key leaves that order in place rather than producing nothing.

**`--with-score` and `--min-score` on a cloud without an integer score.** Three different
refusals, because the three clouds are in three different positions — AWS publishes a 1-10
score, GCP publishes a probability for a ranked page, Azure publishes a score this build
cannot read. The Azure and GCP wordings are quoted in the sections above.
`--min-score` on GCP:

```console
$ spotinfo list --cloud gcp --with-score --min-score 5
spotinfo: unsupported capability: --min-score is refused on gcp: gcp publishes obtainability, and an integer 1-10 floor states no reviewed mapping onto it
```

**`--live-risk` on AWS and Azure.** It fetches a GCP preemption rate and is implemented for no
other cloud. On Azure the message names the vendor limit behind that:

```console
$ spotinfo recommend --cloud azure --architecture x86_64 --min-vcpu 2 --min-memory-gib 4 --live-risk
spotinfo: unsupported capability: --live-risk is refused on azure: azure publishes an eviction rate through Azure Resource Graph, but reading it needs an Azure subscription this build does not authenticate to
```

**`--gcp-project` and `--gcp-billing-key` on AWS and Azure.** Each names a credential for an
authenticated GCP call, and neither cloud makes one.

```console
$ spotinfo list --offline --gcp-project foo
spotinfo: unsupported capability: --gcp-project is refused on aws: the flag names the project an authenticated gcp call is billed to, and aws makes none
```

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
