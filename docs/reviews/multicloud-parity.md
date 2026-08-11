# Multi-cloud parity: what is missing, and what can be added

Date: 2026-08-11.

[cli-and-mcp-surface-review.md](cli-and-mcp-surface-review.md) lists what is inconsistent
today. This page answers the next question: for every capability AWS has and the other clouds
do not, can it be added, and exactly how?

Each verdict is one of four:

- **Ready** — the data is already downloaded or already reachable. Parser or flag work only.
- **Runtime only** — the source exists but cannot ship in a snapshot. It needs the caller's
  own credentials, as an opt-in flag.
- **Blocked** — the vendor publishes nothing that can serve it. Nothing to build.
- **Consistency** — nothing to fetch. The gap is in how the CLI behaves.

## The rule that decides every verdict

`internal/providers/<cloud>/data/source-contract.json` governs the **committed snapshot**.
The excluded sources in [../data-sources.md](../data-sources.md) were excluded for one of two
reasons: they need credentials, or their redistribution terms are unclear. Both reasons are
about _shipping the data to other people_.

Neither reason forbids reading a source **at run time, with the caller's own credentials, for
one invocation**. GCP `--live-risk` already does exactly this: it calls
`compute.advice.capacityHistory`, which the GCP source contract excludes, and never writes a
byte into the snapshot. `docs/research/multicloud-source-contracts.md` says the same thing
about Azure in its own words — Resource Graph "stays deferred to optional live enrichment".

So the answer to "can it be added" is usually not yes or no. It is **which of the two paths**:
snapshot, or opt-in runtime.

## Verdict table

| Capability                             | GCP                     | Azure                        |
| -------------------------------------- | ----------------------- | ---------------------------- |
| `--os windows`                         | Blocked                 | **Ready**                    |
| Interruption risk in the snapshot      | Blocked                 | Blocked                      |
| Risk at run time (`--live-risk`)       | Done                    | Buildable — **deferred** |
| `--workload web` / `ci` / `batch`      | Blocked by measurement  | Blocked by measurement       |
| `--with-score` (placement score)       | **Runtime only** — beta | GA — **deferred**        |
| More regions                           | Runtime only            | Not needed — 55 already      |
| Live prices                            | Runtime only            | **Runtime only** — anonymous |
| Zone-level prices (`--az`)             | Blocked for prices      | Blocked for prices           |
| Root query command                     | Follows risk            | Follows risk                 |
| `--offline` / `--refresh`              | Consistency             | Consistency                  |
| `--sort` / `--order` on `recommend`    | Consistency             | Consistency                  |
| `text`, `csv`, `number` on `recommend` | Consistency             | Consistency                  |

---

## 1. Windows on Azure — Ready

**Status: the data is already downloaded and then thrown away.**

The Azure updater fetches the Retail Prices API per region. That response already contains
Windows Spot rows, keyed by the same `armSkuName` as the Linux row. The parser drops them on
one line:

```go
// internal/providers/azure/prices.go:248
if i.ArmSkuName == "" || strings.Contains(i.ProductName, windowsMarker) || i.isCloudServices() {
```

A row pair from the committed fixture, `internal/providers/azure/testdata/retail-page-1.json`:

```
productName "Virtual Machines Dsv5 Series"          skuName "D2s v5 Spot"  armSkuName Standard_D2s_v5
productName "Virtual Machines Dsv5 Series Windows"  skuName "D2s v5 Spot"  armSkuName Standard_D2s_v5
```

**This is a catalogue-key change, not a one-line change.** Deleting the filter alone breaks the
build: `verifyPrices` in `internal/providers/azure/catalog.go:383` keys on `price.Machine`
alone and fails with "is priced twice in <region>" the moment one size carries two OS rates.

**How to add it:**

1. Read the OS from `productName`. There are **three** states, not two: a `" Windows"` suffix
   marks Windows, a `" Linux"` suffix marks Linux on newer families, and no suffix also means
   Linux on older families. Never infer OS from the size name, for the same reason
   architecture is never inferred.
2. Key the catalogue and the price row by (machine, region, **os**).
3. Add `linux` and `windows` to `support.operating_systems` in the Azure source contract, and
   raise `parser_version`.
4. Add `windows` to the Azure `Capabilities()`.
5. Raise the price coverage floor, because the row count grows.

**Risk to watch:** the Windows meter bundles a licence, so its price is not comparable to the
Linux price of the same size. Report it as a Windows price, never as a discount.

**Why this is worth doing first:** it is the only capability gap on any cloud where the data
already sits in a response the project downloads every week, and it closes the OS column of
the matrix without asking anyone for credentials.

## 1a. Two defects in the Azure meter filter, found while checking item 1

Neither breaks a build today. Both are latent, and the first one fails in the dangerous
direction.

**`Contains` where the rule is a suffix.** `internal/providers/azure/prices.go:248` tests
`strings.Contains(i.ProductName, "Windows")`, while `classOf` in the same file tests its two
sibling markers with `strings.HasSuffix`. The OS marker is a suffix. The failure modes are not
symmetric: `Contains` silently drops a **Linux** row whose product name mentions Windows
elsewhere, and no gate notices a row that was never observed. `HasSuffix` degrades instead to
an over-broad parse that the coverage floor and the duplicate check catch. Use `HasSuffix`.

**Dedicated Host meters are a third contaminant the contract does not name.**
`CLAUDE.md` names two rows to exclude under `serviceName = "Virtual Machines"`: Low Priority
meters and Cloud Services meters. There is a third. Measured against the live anonymous API on
2026-08-11, `eastus`, 9,389 rows over 10 pages:

```
49 rows have "Dedicated Host" in productName
 1 of them has a skuName ending in " Spot":
   productName "FX Series Dedicated Host"  skuName "FXmds Type1 Spot"
   armSkuName  "FXmds Type1"               retailPrice 0.982
```

That row survives every check in `observe()` — its `armSkuName` is non-empty, its
`productName` holds neither `Windows` nor `Cloud Services` — and `classOf` reads its
`" Spot"` suffix and classifies it as a **Spot price**. The other 48 fall to `classOf`'s
default and become **On-Demand prices**.

They are discarded, but by accident. `internal/providers/azure/catalog.go:104` skips any
machine with no specification:

```go
if _, known := specs[row.Machine]; !known {
    continue
}
```

No Microsoft Learn size page documents a host type, so `FXmds Type1` has no spec and vanishes
in a silent `continue`. Nothing reports that a priced row was thrown away. The catalogue is
correct today because a name did not collide, not because a filter rejected it.

**Fix:** exclude Dedicated Host rows in `observe()` alongside Low Priority and Cloud Services,
name them in the Azure source contract, and raise `parser_version`. A host row is not a VM
price, and the parser should say so rather than rely on a downstream join to lose it.

## 2. Windows on GCP — Blocked

Google's Spot pricing pages do not publish a Windows Spot line. Spot discounts are described
against the machine; the Windows licence is priced on a different page that the contract does
not name, and pairing them would mean joining two documents the parser cannot verify against
each other.

**Verdict: do not build.** `--os windows` on GCP must keep returning
`UNSUPPORTED_CAPABILITY`. That is the correct answer, not a missing feature.

## 3. Azure eviction rate — Runtime only, and a trap

**Status: buildable, and the obvious implementation is wrong.**

Azure publishes an eviction rate per SKU per region through Azure Resource Graph:

```kusto
SpotResources
| where type =~ 'microsoft.compute/skuspotevictionrate/location'
| project skuName = tostring(sku.name), location = location,
          spotEvictionRate = tostring(properties.evictionRate)
```

It needs an Azure subscription and authentication, so it cannot enter the snapshot. It is
exactly the shape `--live-risk` was built for.

**How to add it:**

1. Add `--live-risk` support for Azure, reusing the GCP structure: one lazy credential
   resolution, negative result cached, one query per recommendation against the **ranked page
   only**, never the catalogue.
2. Take the subscription from a flag or `AZURE_SUBSCRIPTION_ID`, and never from the ambient
   `az` CLI default — the same rule `--gcp-project` follows, and for the same reason.
3. Add a new risk kind, `RiskKindEvictionRate`, with the reviewed wire name `eviction_rate`.
4. When every lookup fails, emit one warning to stderr naming the first cause, the way the
   GCP path does.

**The trap.** Azure publishes the bands `0%-5%`, `5%-10%`, `10%-15%`, `15%-20%`, `20+%`.
Those look identical to AWS Spot Advisor buckets, and they are not the same statistic:

|                  | AWS interruption frequency                  | Azure eviction rate                                   |
| ---------------- | ------------------------------------------- | ----------------------------------------------------- |
| What it measures | fraction of _running_ instances interrupted | probability that a VM is evicted in the **next hour** |
| History window   | trailing 30 days                            | trailing 7 days                                       |
| Normalisation    | over the period                             | per hour                                              |

An implementer who sees `0%-5%` next to AWS `<5%` will be tempted to add
`RiskKindEvictionRate` to `interruptionCappableKinds` and let `--workload web` work on Azure.
**Do not.** A per-hour probability compared against a 30-day frequency ceiling is a wrong
answer that passes every test. Azure eviction rate belongs in the same category as GCP
`preemption_rate`: **visible, never filterable.**

This is the exact case the kind vocabulary in `internal/cloud/recommend.go` was designed to
catch. Adding the kind and leaving it out of the cappable list is the design working, not a
shortcoming.

## 4. `--workload web` / `ci` / `batch` on GCP and Azure — Blocked by measurement

Even after item 3 ships, both clouds keep refusing risk-capped workloads. The ceilings 5%,
16% and 22% are AWS Advisor bucket boundaries. Neither Google's preemption rate nor Azure's
eviction rate is drawn from that measurement, so the ceiling has no meaning against them.

**What would unblock it:** a vendor publishing a figure with the same denominator and window
as AWS, or a reviewed conversion between them. Neither exists. Comparing a per-hour
probability with a 30-day frequency needs an assumed session length, which is a modelling
choice this tool must not make on the caller's behalf.

**Verdict: `cost` stays the only cross-cloud workload.** This is not a gap to close. It is the
honest answer, and it is why `RISK: unavailable` is printed instead of a zero.

## 5. Placement scores (`--with-score`) — Runtime only, on both clouds

**Correction.** An earlier draft of this page said both clouds were blocked. That was wrong.
Both publish an equivalent, and Azure's has been GA since 2025-06-05.

|           | AWS                      | Azure                           | GCP                       |
| --------- | ------------------------ | ------------------------------- | ------------------------- |
| Interface | `GetSpotPlacementScores` | `placementScores/spot/generate` | `compute.advice.capacity` |
| Score     | integer 1-10             | `High` / `Medium` / `Low`       | `obtainability`, 0.0-1.0  |
| Extra     | —                        | `isQuotaAvailable`              | `estimatedUptime`         |
| Stage     | GA                       | GA (`2025-06-05`)               | beta / preview            |
| Auth      | SigV4                    | Entra OAuth + subscription      | OAuth or ADC + project    |
| Limits    | —                        | 8 regions x 5 sizes             | 5 machine types           |

Azure's own words: the score "evaluates the likelihood of success for individual Spot
deployments", and "a score of High indicates that the deployment is highly likely to succeed".
That is the same role AWS's score plays. Microsoft charges nothing for it. It is also reachable
as `az compute-recommender spot-placement-recommender` and through the `armrecommender` Go SDK.

GCP's `compute.advice.capacity` is described as "advice on making real-time decisions (such as
choosing zone or machine types) during deployment to maximize your chances of obtaining
capacity", and its request schema names "scores [that] determine VM obtainability and
preemption likelihood". It sits on the same beta path and uses the same scopes as
`capacityHistory`, so it reuses the `--live-risk` credential machinery unchanged.

**How to add it:**

1. Extend `--with-score` to `recommend`, applied to the **ranked page only**, never the
   catalogue — the same rule `--live-risk` follows.
2. Give each cloud its own score observation. The three are not one number: AWS is an integer
   rank, Azure is one of three labels, GCP is a probability with an uptime estimate.
   Normalising them into a shared 1-10 would invent precision no vendor published.
3. Honour the per-request limits. Azure caps at 8 regions x 5 sizes, GCP at 5 machine types.
   A `--region all` recommendation must batch or refuse, not truncate silently.
4. `--min-score` only has meaning against a numeric score. On Azure it must either map to the
   three labels explicitly, or be refused.

**Two things not to do.** First, a placement score measures **provisioning success**, not
interruption of a running instance. It is a different question from `--workload`, so it needs
its own kind and must stay out of `interruptionCappableKinds`, exactly like
`RiskKindPreemptionRate`. Second, Azure documents that a score is "only valid at the time when
it's requested" and does not guarantee fulfilment — so it must never be cached into a snapshot
and never presented as a promise.

**Zones.** Both APIs accept a zone, so `--az` has meaning for scores on both clouds even though
neither publishes zone-level _prices_. That is why the zone row in the verdict table says
"blocked for prices" rather than "blocked".

## 6. More GCP regions — Runtime only

Today the snapshot serves `us-central1`, because that is the only region Google's pricing
pages **server-render**. Other regions are switched in by JavaScript from a 12 MB
`AF_initDataCallback` positional array, which the contract excludes as an undocumented
interface. That exclusion is correct and should stand.

Two paths exist, in order of preference:

**a. Cloud Billing Catalog API, opt-in at run time.** It covers every published region and
needs an API key, with no special IAM permission. The key is the caller's, the answer is not
redistributed, and nothing enters the snapshot — the same shape as `--live-risk`. Add a flag
such as `--gcp-billing-key` (with an environment variable), and let it widen `--region` beyond
`us-central1` for that invocation only. Google's terms for redistributing catalogue prices are
not stated, which is precisely why this must stay a runtime path and never a committed one.

**b. Watch for more server-rendered regions.** If Google ever server-renders a second region,
the existing parser already attributes each table to the selector above it, so widening is a
contract change and a coverage-floor change, with no new parser.

**Verdict: buildable as opt-in, never as a snapshot.**

## 7. Live prices on GCP and Azure — Runtime only

AWS falls back to `DescribeSpotPriceHistory` when the static feed prices something at $0.
Neither other cloud has a live path at all, so both are exactly as fresh as the last weekly
refresh.

- **Azure**: the Retail Prices API is **anonymous**. A `--refresh`-style live path needs no
  credentials at all. This is the cheapest freshness win available on any non-AWS cloud.
- **GCP**: the Cloud Billing Catalog API needs an API key — same mechanism as item 6.

**Verdict: buildable.** It also gives `--offline` and `--refresh` a real meaning on both
clouds, which removes the silent no-op in item 9 by making the flags do something rather than
by refusing them.

## 8. Zone-level prices (`--az`) — Blocked for prices, available for scores

The Azure Retail Prices API publishes region-level prices only. Google publishes Spot prices
per region, not per zone. Neither vendor exposes zone **pricing** through a contracted
interface.

Zone-level **placement scores** are a different matter: both APIs in item 5 accept a zone. So
`--az` can mean something on GCP and Azure, but only alongside `--with-score`, never for a
price column.

**Verdict: do not build zone pricing. Zone scores follow item 5.**

## 9. Consistency work — no data needed

None of these needs a vendor API. Each is a case where the same flag means different things,
or silently means nothing.

| Gap                                                                      | Fix                                                                                                                                |
| ------------------------------------------------------------------------ | ---------------------------------------------------------------------------------------------------------------------------------- |
| `--offline` / `--refresh` accepted and ignored on GCP and Azure          | Either implement item 7 so they act, or refuse them with a message naming the cloud                                                |
| `--az`, `--min-score`, `--score-timeout` accepted without `--with-score` | Refuse, the way `--live-risk` is refused off GCP                                                                                   |
| `--with-score` is on the AWS-only query command                          | Until item 5 ships, refuse it with `UNSUPPORTED_CAPABILITY` on a cloud with no score, instead of silently ignoring `--cloud`       |
| `--sort` and `--order` missing on `recommend`                            | Add them. Ranking is cloud-neutral; nothing blocks this                                                                            |
| `text`, `csv`, `number` missing on `recommend`                           | `csv` and `text` port cleanly. `number` does not — it prints one savings percent, which has no meaning for a ranked list           |
| Root query command is AWS-only                                           | It renders an interruption column. It stays AWS-only until a cloud publishes a comparable figure, which per item 4 is not in sight |
| `--gcp-project` ignored on AWS and Azure                                 | Refuse it, matching `--live-risk`                                                                                                  |

## Decision, 2026-08-11: no Azure credentials

The maintainer has no Azure subscription, so items 3 and the Azure half of item 5 are
**deferred**, not rejected. Both remain accurate and buildable; what changed is that neither
can be exercised or tested today, and reaching them costs `azidentity` at **+4.83 MB**, or
+11.7% of the shipped binary. The ARM SDK on top of that is only +136 KB, so the credential
chain is the whole price.

`docs/plans/20260811-multicloud-parity.md` carries the rest and states as an invariant that
nothing in it authenticates to Azure. Revisit when a subscription exists.

Everything else on this page stands: Windows on Azure, live Azure prices, GCP obtainability
and wider GCP regions all need no Azure credentials at all.

## Suggested order

Ordered by value delivered against effort, and by how much each one reduces the surprise a
person meets when they change `--cloud`.

1. **Consistency fixes** (item 9). No external dependency, no contract change. Removes every
   silent no-op.
2. **The two Azure meter defects** (item 1a). `HasSuffix` for the OS marker, and an explicit
   Dedicated Host exclusion. Both are small, and the second closes a filter that is correct
   today only because two names did not collide.
3. **Windows on Azure** (item 1). The data is already downloaded. Closes the OS column.
4. **Live Azure prices** (item 7). Anonymous, so no credential design is needed, and it gives
   `--offline` and `--refresh` real meaning on one more cloud.
5. **Azure eviction rate behind `--live-risk`** (item 3), with `eviction_rate` deliberately
   left out of `interruptionCappableKinds`.
6. **Placement scores on Azure, then GCP** (item 5). Azure first: it is GA and free. GCP's is
   still beta.
7. **GCP regions and live prices behind an API key** (items 6 and 7). Largest design surface,
   because it introduces a second credential type.

Only three verdicts are genuinely blocked by the vendors: Windows on GCP, zone-level _prices_
on both, and risk-capped workloads on both. Record those as answered, so the question is not
re-opened every time someone reads the matrix.

## Sources

Local claims are cited to `file:line` and were read from this repository. External claims were
checked against vendor documentation, and the Azure meter measurements against the live
anonymous Retail Prices API on 2026-08-11.

- Azure eviction rate and the Resource Graph query:
  [Spot eviction](https://learn.microsoft.com/en-us/azure/architecture/guide/spot/spot-eviction),
  [Spot VMs](https://learn.microsoft.com/en-us/azure/virtual-machines/spot-vms)
- Azure Spot Placement Score:
  [feature page](https://learn.microsoft.com/en-us/azure/virtual-machine-scale-sets/spot-placement-score),
  [REST reference](https://learn.microsoft.com/en-us/rest/api/recommenderrp/spot-placement-scores/post?view=rest-recommenderrp-2025-06-05)
- Azure Retail Prices API:
  [overview](https://learn.microsoft.com/en-us/rest/api/cost-management/retail-prices/azure-retail-prices)
- GCP `compute.advice` methods: the Compute Engine Discovery document, revision 20260729, and
  [View the availability of Spot VMs](https://docs.cloud.google.com/compute/docs/instances/view-vm-availability)
- GCP Cloud Billing Catalog API:
  [catalog how-to](https://docs.cloud.google.com/billing/v1/how-tos/catalog-api)

The Azure `productName` OS-suffix rule is **not documented** by Microsoft. It is demonstrated
in their own samples and held over roughly 30,000 live rows, but treat it as a high-confidence
convention rather than a specified contract — which is an argument for `HasSuffix` and a
coverage floor, not against reading it.
