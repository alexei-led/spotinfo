# Multi-cloud source contracts

Record of the data sources spotinfo may embed, the decisions each one still
needs, and the conditions that stop a provider from shipping.

A provider does not get an implementation until its machine-readable contract
exists, is approved, and passes the gate. The contract is the normative
artifact; this page is the reasoning behind it.

- Schema: `docs/plans/contracts/provider-source-contract.schema.json`
- Loader and gate: `internal/snapshot/source_contract.go`
- Per-provider contract: `internal/providers/<provider>/data/source-contract.json`

## What a contract must state

| Field | Why it is required |
| --- | --- |
| `sources[].url`, `source_type` | Only a documented anonymous REST API or an official server-rendered page qualifies. Calculators, undocumented JavaScript endpoints, and aggregators do not. |
| `terms` | Redistribution has to be an explicit decision with a linked evidence page, not an assumption. |
| `expected_fields` | The exact fields the parser reads, so a source that renames one fails instead of parsing to zero. |
| `support` | The complete supported OS, architecture, price class, region, and machine-series lists. An unlisted value is out of contract, not a judgement call. |
| `thresholds` | The coverage floor, size limit, and maximum decimal precision the snapshot must satisfy. |
| `parser_version`, `update_cadence` | Ties a committed snapshot to the parser the review actually saw. |
| `no_go_conditions` | The observations that stop the provider rather than degrade it. |

`support.risk_status` is `unavailable` for every offline provider. GCP
preemption history and Azure eviction history both require authorization, so
neither ships in v2. A provider must never present silence as a low risk.

## Source candidates

### AWS — already embedded, not contract-governed

AWS predates this contract and embeds its upstream feeds verbatim under its own
parser contract in `internal/spot`. Its provenance lives in the sidecar
manifests next to the data:

| Snapshot | Source | Payload form |
| --- | --- | --- |
| `spot-price-data.json` | `https://website.spot.ec2.aws.a2z.com/spot.json` | raw-source |
| `spot-advisor-data.json` | `https://spot-bid-advisor.s3.amazonaws.com/spot-advisor-data.json` | raw-source |
| `architecture-snapshot.json` | AWS EC2 instance type documentation, reviewed by hand | parsed-catalog |

The price feed is undocumented. Its predecessor froze silently for two years, so
the freshness check in `.github/workflows/update-data.yaml` and the coverage
floors in the manifests exist to surface the next freeze in weeks rather than
years.

### GCP — approved, `us-central1` only

Contract: `internal/providers/gcp/data/source-contract.json`
(`review_status: approved`, parser `gcp-pricing-html/1`, reviewed 2026-08-09).

Sources, all official and server-rendered:

| URL | Supplies |
| --- | --- |
| `https://cloud.google.com/spot-vms/pricing` | Spot price, vCPU, memory |
| `https://cloud.google.com/products/compute/pricing/general-purpose` | On-Demand price |
| `https://cloud.google.com/products/compute/pricing/compute-optimized` | On-Demand price |
| `https://cloud.google.com/products/compute/pricing/memory-optimized` | On-Demand price |
| `https://docs.cloud.google.com/compute/docs/machine-resource` | Architecture, read by hand |

`https://cloud.google.com/compute/vm-instance-pricing` — the URL the plan named —
now redirects to a landing page with no price tables. The per-family category
pages above are where the On-Demand tables moved.

#### 1. Which headers are parsed, and what happens when one moves

A table is read only when its header row matches exactly:

- Spot: `Machine type` · `Virtual CPUs` · `Memory` · `Current Spot pricing (USD)`
- On-Demand: `Machine type` · `Virtual CPUs` · `Memory` · a fourth cell starting
  `Default* (USD)` — the committed-use columns beside it price a contract, not an
  hour.

Value cells are equally exact: `$<number> / 1 hour`, `<number> GiB`, and a machine
identifier matching `^[a-z][a-z0-9]*(?:-[a-z0-9]+)+$`.

A renamed header matches nothing, so the parse empties and the coverage floor
fails the run. Nothing is guessed and nothing partial is written: the updater
validates before it writes, so a failed refresh leaves the reviewed snapshot
untouched.

#### 2. The region rule — the one place this could silently go wrong

The pages carry no region column. Each price table is preceded by an ARIA
listbox whose selected option holds the region:
`role="option" aria-selected="true" data-value="Iowa (us-central1)"`. The parser
attributes every table to the selector rendered above it and reads only the
tables whose region matches the contract.

This is not defensive coding. The Spot page has 74 tables, and one of them —
custom machine pricing — renders for `africa-south1` because `us-central1` does
not offer it. A parser that assumed one region per page would publish those
prices under the wrong region, parse cleanly, and pass every other gate. A page
with no selector at all fails outright.

Only `us-central1` is claimed because that is the only region the pages
server-render; the others are switched in by JavaScript. The 12 MB
`AF_initDataCallback` blob behind that switch is an undocumented positional
array, which the safety notes exclude. One region is a narrower claim than the
plan hoped for, not a broken one.

#### 3. Exact support matrix

| Dimension | Claimed |
| --- | --- |
| Region | `us-central1` |
| OS | `linux` |
| Architectures | `x86_64`, `arm64` (Arm series: `c4a`, `n4a`, `t2a`) |
| Price classes | `spot` and `on-demand`, paired per machine |
| Risk | `unavailable` |
| Machine series (18) | `c2` `c3` `c3d` `c4` `c4a` `c4d` `e2` `m1` `m2` `m3` `n1` `n2` `n2d` `n4` `n4a` `n4d` `t2a` `t2d` |
| Machines | 333 |

Deliberately excluded:

- **Shared-core machines** (`f1-micro`, `g1-small`, `e2-micro`): a fraction of a
  vCPU has no whole-core count, and rounding one up would invent a core.
- **Platform-annotated rows** such as `n1-standard-96 Skylake Platform only`:
  the cell names a platform variant, not a machine type a user can request.
- **Local-SSD shapes** (`-lssd`) and GPU/TPU tables: their header differs and
  their price includes storage.
- **Spot machines with no On-Demand pair**: a savings figure with no denominator.
  The updater prints each one it skipped.

Architecture is the one field the pricing tables do not publish. It comes from a
reviewed series list in the parser, with the machine-resource documentation page
recorded as its source — the same posture as the AWS architecture snapshot.

What protects that list is the contract's `machine_series` allow-list, not the
architecture check: an unlisted series defaults to `x86_64`, so a new Arm series
would be mislabelled if it could reach the catalogue at all. It cannot — the
gate rejects the whole refresh with `belongs to unapproved series` until the
series is added to both the parser's Arm list and the contract, in that order.
The architecture check itself only catches a hand-edited catalogue: a freshly
built one derives the field from the same list it is compared against.

#### 4. Redistribution

`terms.redistribution_decision: approved`, evidence
`https://developers.google.com/site-policies`.

What is redistributed is factual: machine type, vCPU count, memory in GiB, and
USD per instance-hour, with the exact source URLs and fetch times recorded in the
sidecar manifest. No page markup, styling, prose, or image is copied. The
machine-resource documentation page carries a Creative Commons Attribution 4.0
footer; the pricing pages do not, so their figures are used as facts rather than
as expression. **This decision needs the project owner's explicit confirmation
before release**, and again on any schema change.

#### 5. Thresholds and precision

| Threshold | Value | Why |
| --- | --- | --- |
| `min_regions` | 1 | One region is all the pages render |
| `min_machines` | 300 | 333 observed; the floor catches a partial render |
| `max_compressed_bytes` | 65536 | 5,815 committed — room to grow, not to run away |
| `max_fractional_digits` | 9 | Measured maximum, e.g. `$0.167920068` |

Nine digits is exactly `cloud.MoneyScale`, so there is no headroom. A GCP price
needing a tenth digit fails `ParseMoney` with `ErrPrecisionLoss` rather than
rounding, and raising the scale is then a deliberate decision.

The coverage floor is not theoretical. During the first refresh Google served a
partially rendered general-purpose page missing every `n4d` table; the run
stopped at 295 machines with `coverage below the manifest floor` and wrote
nothing. A retry produced the full 333.

Binary-size delta for the whole GCP slice: **+127,056 bytes** (parser, provider,
`golang.org/x/net/html`, and the 5,815-byte catalogue).

`advice.capacityHistory` is authenticated and beta. It is deferred to optional
live enrichment and is not part of the offline contract.

### Azure — approved, 55 regions

Two sources, because neither answers the whole question:

- **Prices**: `https://prices.azure.com/api/retail/prices`, the documented, anonymous
  Azure Retail Prices API. No key, subscription, or tenant.
- **Specifications and architecture**: one Microsoft Learn size page per approved
  machine series. The Retail API publishes no vCPU count, no memory figure and no
  processor architecture, so all three come from the pages.

Contract: `internal/providers/azure/data/source-contract.json`. Parser:
`azure-retail-prices/1`.

#### 1. The exact request, and what a Spot row is

Per region, one sweep:

```
GET https://prices.azure.com/api/retail/prices
    ?api-version=2023-01-01-preview
    &currencyCode=USD
    &$filter=serviceName eq 'Virtual Machines'
        and armRegionName eq '<region>'
        and priceType eq 'Consumption'
```

`NextPageLink` is followed until empty, bounded at 40 pages (a region returns 8-10
today) and checked to stay on the contracted scheme and host — the response chooses
the next request, and nothing else in this repository lets a server do that.

Four response values are contract, not data: `serviceName`, `type`, `unitOfMeasure`
and `currencyCode`. A row that disagrees means the filter stopped selecting what it
was reviewed to select, so the refresh fails rather than skipping the row.

Classification, in order:

| Rule | Outcome | Why |
| --- | --- | --- |
| `skuName` ends ` Spot` | Spot price | The only interruptible meter |
| `skuName` ends ` Low Priority` | **excluded** | Retired Batch product, different eviction model, priced alongside Spot |
| otherwise | On-Demand price | List rate |
| `productName` contains `Windows` | excluded | Bundles a licence; the catalogue is Linux-only |
| `productName` contains `Cloud Services`/`CloudServices` | excluded | Legacy PaaS, same `armSkuName`, different rate |
| `armSkuName` empty | skipped | Cannot be attributed to a machine (0 observed) |
| `retailPrice` zero | skipped | Promotional or placeholder, not a quotable rate |

The Cloud Services exclusion was not anticipated: it made about 40 sizes per region
ambiguous, and the second spelling (`Eadsv5 Series CloudServices`, no space) survived
a first fix that matched only `Cloud Services`. The marker is matched with spaces
removed.

#### 2. The effective-date rule — where this could silently go wrong

The API returns every interval it knows about. In `eastus`, 289 meters carry more
than one row, typically an interval that ended last month beside the one now in
force. A parser that took the first row, or the last, would publish an expired price
and pass every other gate.

The rule: a price is the interval where `effectiveStartDate <= at` and
`effectiveEndDate` is absent or not before `at`.

- `at` is an **argument**, set once per run, not `time.Now()` inside the parser. All
  all 55 regions resolve against the same instant, so a sweep cannot mix an expiring
  price with its replacement, and a rebuild is reproducible from its inputs.
- No interval in effect: the machine is dropped and reported. An expired price is
  worse than a missing one.
- Two intervals in effect at different amounts: the refresh fails. Nothing can choose
  between them. The same amount twice is redundancy and is accepted.
- Resolution runs **after** the reviewed matrix filter. The sweep prices every size
  Azure sells — around 1,700 per region against 224 contracted — and a conflict in
  `Standard_NCC40ads_H100_v5` is not a reason to fail a refresh for machines nobody
  can ask this binary about.

#### 3. Architecture — read, never inferred

Each size page's parts table carries a `Processor` row listing processor models with
a bracketed architecture marker. Three spellings are in use today: `[x86-64]`,
`[Arm64]` and `[ARM-64]`. They are normalised; a page with no marker, or two
different markers, fails the parse.

This is deliberately stricter than the GCP slice, which keeps a reviewed Arm series
list in code because Google publishes no marker. Here the source states it, so the
source is used. An Arm size shipped as `x86_64` would pass coverage, price sanity and
schema checks and silently recommend a machine that cannot run the caller's binaries.

#### 4. Memory is labelled two ways

18 pages write `Memory (GiB)`; the 8 memory-optimized pages write `Memory (GB)`. The
figures are identical for equivalent sizes — `Standard_E2_v5` reads 16 on a "GB" page,
`Standard_E2s_v5` reads 16 on a "GiB" one — so this is one quantity under two labels,
not two units. Both are accepted as gibibytes; a third label fails.

#### 5. Exact support matrix

- **Regions (8)**: `australiaeast`, `eastus`, `eastus2`, `northeurope`,
  `southeastasia`, `uksouth`, `westeurope`, `westus2`.

  A reviewed selection, not a limit of the source: the API serves every Azure region.
  Eight bounds the weekly refresh to about 100 requests and the payload to 16 KB.
  Widening it is a contract edit plus a refresh, with no code change.

- **Machine series (26)**, one Learn page each:

  | Architecture | Series |
  | --- | --- |
  | x86_64 (21) | `basv2` `bsv2` `dadsv5` `dalsv6` `dasv5` `dasv6` `ddsv5` `ddv5` `dsv5` `dsv6` `dv5` `easv5` `easv6` `edsv5` `edv5` `esv5` `esv6` `ev5` `falsv6` `fasv6` `fsv2` |
  | arm64 (5) | `bpsv2` `dpdsv5` `dpsv5` `dpsv6` `epsv5` |

- **Sizes**: 224 priced in all 55 regions (37 arm64). 225 sizes are documented by
  the contracted pages; one is not offered as Spot.
- **OS**: Linux only. **Classes**: Spot and On-Demand, always paired.
- **Risk**: `unavailable`, permanently for the offline path.

#### 5a. Widening from eight regions to 55 (2026-08-10)

The Retail Prices API is anonymous and publishes every region, so the original
eight were a review decision, not a limit of the source. Widening cost nothing
but a rebuild.

The region list is **derived from the coverage floor, not chosen**. 67 regions
publish Spot meters for the 26 contracted series; the join against the Learn size
pages — which is what actually decides whether a machine can be published, since
vCPU, memory and architecture come from there — leaves 59 candidates, of which 55
carry at least `min_machines`. The four that fall short are excluded rather than
accommodated:

| region | machines after the join | |
| --- | --- | --- |
| `chilecentral` | 177 | below the floor |
| `malaysiawest` | 166 | below the floor |
| `indiasouthcentral` | 157 | below the floor |
| `denmarkeast` | 157 | below the floor |

`min_machines` stays **180**. Lowering it to 157 would have admitted all four and
is exactly the move the per-region floor exists to prevent: a floor is a floor,
and a short region is a signal, not a rounding error.

Also excluded, deliberately:

- **`usgov*`** (`usgovvirginia` 194, `usgovarizona` 194, `usgovtexas` 185) clear
  the floor. Azure Government is a separate sovereign cloud with its own endpoints
  and access rules, and quoting its rates beside commercial ones invites a
  comparison that does not hold. A reviewer can add them as a deliberate act.
- **AT&T edge zones** (`attatlanta1`, `attdallas1`, 60 each), `southwestus` (54),
  `eastus3` (8), `portland` (1) — all far below the floor.
- **`Global`**, which carries no machine of a contracted series.

Two thresholds moved with the surface:

| threshold | before | after | basis |
| --- | --- | --- | --- |
| `min_regions` | 8 | 55 | every approved region must be present |
| `max_compressed_bytes` | 65536 | 131072 | 88,272 observed; ~1.5x headroom |
| `min_machines` | 180 | **180** | unchanged |

The manifest's reviewed coverage floor was raised from 8 to 55 regions in the same
act. The updater refuses a manifest floor below the contract minimum, which is why
this cannot happen by accident — and raising a floor is safe in a way lowering one
is not.

Snapshot cost: 16,515 → **88,272 compressed bytes** (+71,757, 0.17% of the binary)
for 6.9x the regions. The immediate payoff is that the cheapest 4 vCPU / 16 GiB
x86_64 machine is `centralindia` at 0.020513 USD/hour, against `uksouth` at
0.027894 — 26% cheaper, and invisible before this change.

`parser_version` is **not** bumped: no parser was widened, and no source contract
gained a URL. Only the region list the same parser is asked for changed.

#### 6. Redistribution

`terms.redistribution_decision = approved`, evidence
`https://learn.microsoft.com/en-us/legal/termsofuse`.

The Retail Prices API is documented for anonymous use and returns factual figures.
Microsoft Learn documentation content is published under CC BY 4.0. The committed
catalogue republishes size name, vCPU count, memory in GiB, architecture and USD per
instance-hour, attributed to the exact source URLs in the manifest. No markup,
styling, prose, or image is copied.

**This decision was recorded by an agent, not by a human reviewer.** The project
owner must confirm it before release, exactly as for the GCP contract.

#### 7. Thresholds and precision

| Threshold | Value | Basis |
| --- | --- | --- |
| `min_regions` | 8 | Every approved region must be present |
| `min_machines` | 180 | ~80% of the 224 observed. Applied **per region**, so one region returning short fails instead of being absorbed by seven healthy ones |
| `max_compressed_bytes` | 65536 | 16,515 observed; ~4x headroom |
| `max_fractional_digits` | 6 | Maximum observed across all 55 regions. `cloud.MoneyScale` is 9, so there are three digits of real headroom before `ErrPrecisionLoss` |

Binary-size delta: **+119,536 bytes** (0.20%). `golang.org/x/net/html` was already a
dependency from the GCP slice.

Cross-checked against the live source at commit time: `uksouth/Standard_D2as_v6`
Spot `0.014013` and On-Demand `0.106`; `westeurope/Standard_D2ps_v5` Spot `0.017002`.
Both match exactly.

#### 8. Deferred

Resource Graph `SpotResources` and the Resource SKUs API require a subscription. Both
stay deferred to optional live enrichment and are named in the excluded list.

### Excluded

Vantage and other aggregators are cross-check references only. Nothing from them
is embedded without explicit redistribution permission.

## No-go conditions

Stop the provider task and update its GitHub issue rather than committing data
when any of these hold:

- Redistribution cannot be approved, or the terms are unclear.
- The source stops rendering its data without JavaScript, or moves to an
  authenticated endpoint.
- The source stops serving a stable document. Two reads moments apart returning
  different bytes means a rollout is in flight, and the contracted pages are
  fetched seconds apart, so a snapshot taken then can pair a Spot price from one
  generation with an On-Demand price from another. Observed on the GCP Spot page
  on 2026-08-10; the updater now reads every page twice and refuses on a
  mismatch.
- Coverage, size, or precision falls outside the contracted thresholds.
- A price row cannot be resolved to exactly one machine, region, OS, and class.
- Risk data would have to be inferred rather than published.
