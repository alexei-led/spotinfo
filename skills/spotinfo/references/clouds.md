# What each cloud publishes

The three clouds do not publish the same things. spotinfo refuses a question a
cloud cannot answer rather than inventing a value, so most surprises come from
this table.

## Capability matrix

|                                 | AWS                      | GCP                           | Azure                     |
| ------------------------------- | ------------------------ | ----------------------------- | ------------------------- |
| Spot price                      | yes                      | yes                           | yes                       |
| On-demand price and savings     | yes                      | yes                           | yes                       |
| Interruption figure             | yes, Spot Advisor bucket | no                            | no                        |
| Linux                           | yes                      | yes                           | yes                       |
| Windows                         | yes                      | **no**                        | yes                       |
| Regions offline                 | all                      | `us-central1` only            | 55 reviewed               |
| Live prices without credentials | yes                      | no                            | yes, anonymous Retail API |
| Placement figure                | `placement_score` 1-10   | `obtainability` 0.0-1.0, beta | not built                 |
| Zone-level figures              | yes, with `--az`         | no                            | no                        |

## Why Windows is refused on GCP

Google publishes Spot prices for Linux only. `--os windows --cloud gcp` is
refused with that reason. This is a vendor limit, not a missing feature, and it
cannot be worked around by another flag.

## Why risk-capped workloads are AWS-only

`--workload web`, `ci` and `batch` each cap interruption frequency at a
threshold: 5%, 16% and 20%. Those thresholds are AWS Spot Advisor bucket
boundaries. They are meaningful only against a figure measured the same way.

GCP and Azure publish no comparable figure, so a ceiling expressed in AWS
buckets cannot be applied to them. Asking for one is refused with that
explanation.

`--workload cost` is the default and makes no interruption claim, so it answers
on every cloud. Use it unless the user explicitly wants a risk ceiling and is
asking about AWS.

Even where a figure exists, it only counts if it is measured the same way. GCP's
`preemption_rate` under `--live-risk` is visible in the output but is never
accepted as a filter, because it divides preempted instances by instances that
stopped for any reason, while AWS measures the fraction of running instances
reclaimed. Making one filter the other would compare two different quantities.

## Azure specifics

Azure serves Windows and Linux from a catalogue of 21,656 priced rows across 55
regions and 26 machine series.

Live prices come from the anonymous Azure Retail Prices API, so `--offline` and
`--refresh` both act on Azure and neither needs a credential. A live refresh
covers at most two explicitly named regions per run, because one region sweep is
about 9,000 meters. `--region all`, the default, answers from the snapshot.

Azure eviction rate and Azure Spot Placement Score both exist as vendor APIs but
need an Azure subscription. They are not built, and the shipped binary links no
Azure credential library. The refusal message says so, which is how you tell
"not published" from "not built".

## GCP specifics

The committed snapshot covers `us-central1` only. Ask for another region without
a key and you get no candidates, which is honest rather than an error.

`--gcp-billing-key` reads the Cloud Billing Catalog API and widens the regions
for that one invocation. Nothing fetched this way is ever written into the
snapshot, because Google states no redistribution terms for that API.

`--with-score` on GCP calls a beta API and needs Application Default Credentials
plus `--gcp-project`. The project comes from the flag or `GOOGLE_CLOUD_PROJECT`
and never from an ambient `gcloud` setting, because the call is billed to
whatever it names. Google scores a whole configuration rather than one machine
type, so spotinfo asks about each machine on its own instead of batching, and
refuses a batch larger than the documented limit of five rather than truncating
it.

## AWS specifics

AWS is the only cloud with an interruption figure, and the only one where
`--workload web|ci|batch` works.

Prices come from the Spot Advisor and pricing feeds, with a live
`DescribeSpotPriceHistory` fallback for instance types the static feed omits.
That fallback needs AWS credentials. `--offline` skips it, which is why an
offline query is both faster and credential-free.

`--with-score` adds placement scores and needs credentials plus the
`ec2:GetSpotPlacementScores` permission. It cannot be combined with `--offline`,
because no snapshot carries a placement figure.

## Reading a refusal

Every refusal names the flag, the cloud and the limit. Read stderr rather than
guessing:

```
spotinfo: gcp: unsupported capability: os windows: this cloud publishes spot prices for linux only
spotinfo: gcp: unsupported capability: risk: the web workload caps interruption frequency at 5%, an AWS Spot Advisor bucket boundary, and gcp publishes no figure measured that way; workload cost applies no ceiling and answers on every cloud
spotinfo: invalid argument: --with-score cannot be combined with --offline: no snapshot carries a placement figure, so --with-score can only come from a live vendor API
```
