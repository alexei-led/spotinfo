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

### Azure — decisions open

Candidate sources:

- Azure Retail Prices API: `https://prices.azure.com/api/retail/prices`
  (anonymous and documented).
- Official Azure VM-size documentation for vCPU, memory, and architecture.

Open decisions before any Azure code:

1. The exact filter and `priceType=Consumption` selection, plus how a Spot row is
   recognised.
2. Pagination via `NextPageLink`, and what happens when a page fails midway.
3. Effective-date rules, and which row wins when several are current.
4. The exact Linux VM-size, region, and architecture matrix to claim.
5. Redistribution terms, with the evidence URL.
6. Canonical USD-per-hour units and the decimal precision observed.

Resource Graph `SpotResources` and Resource SKUs require a subscription. Both
are deferred to optional live enrichment.

### Excluded

Vantage and other aggregators are cross-check references only. Nothing from them
is embedded without explicit redistribution permission.

## No-go conditions

Stop the provider task and update its GitHub issue rather than committing data
when any of these hold:

- Redistribution cannot be approved, or the terms are unclear.
- The source stops rendering its data without JavaScript, or moves to an
  authenticated endpoint.
- Coverage, size, or precision falls outside the contracted thresholds.
- A price row cannot be resolved to exactly one machine, region, OS, and class.
- Risk data would have to be inferred rather than published.
