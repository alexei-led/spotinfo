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

### GCP — decisions open

Candidate sources, all official and server-rendered:

- `https://cloud.google.com/spot-vms/pricing`
- `https://cloud.google.com/compute/vm-instance-pricing`
- The linked Compute pricing category pages.

Open decisions before any GCP code:

1. Which page headers are stable enough to parse, and what the parser does when
   one moves.
2. The exact Linux machine-series, region, and architecture matrix to claim.
3. Redistribution terms, with the evidence URL.
4. Coverage thresholds and the compressed-size budget for the committed
   catalogue.
5. Whether any published price needs more than `cloud.MoneyScale` fractional
   digits. If it does, the scale is raised deliberately; the parser never rounds.

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
