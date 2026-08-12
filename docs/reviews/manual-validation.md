# Manual correctness and usability review

**Date:** 2026-08-12
**Binary:** `.bin/spotinfo`, `v2.5.0-95-gc293cc2`, built by `make build` from
`feat/spotinfo-multicloud-v2`
**Scope:** Task 18 of `docs/plans/completed/20260811-multicloud-parity.md` — the judgement no assertion
makes. Task 17 proved the binary answers on every command x cloud x format cell; this asks
whether the answer is _right_ and whether a first-time reader can act on what it prints.

Every number below was read from a vendor page or API on 2026-08-12 and compared against
output copied from the binary. Where a vendor page could not be read, the cell is recorded as
unverified and says why.

**Verdict: ship, with one open data defect.** Of the eleven checks the task names, seven passed
against the binary as shipped. Thirteen findings were raised: seven are fixed in this task, six
are recorded and left. The one that matters is the GCP catalogue — its prices are stale,
because its updater could not run, because the stability gate compared bytes that change on
every response. The gate is fixed here; the refresh it now reaches is blocked on a source-page
header rename that needs the contract's reviewer, not a review agent.

---

## Summary

| #   | Finding                                                                  | Severity | Verdict            |
| --- | ------------------------------------------------------------------------ | -------- | ------------------ |
| 1   | GCP snapshot updater could never write a snapshot                        | High     | Fixed              |
| 2   | GCP on-demand page renamed the N4D column header                         | High     | Open, needs review |
| 3   | GCP catalogue prices are stale and measurably wrong                      | Medium   | Open, blocked by 2 |
| 4   | Twelve identical credential warnings on a default `list`                 | High     | Fixed              |
| 5   | `unknown cloud provider` did not name the accepted set                   | Medium   | Fixed              |
| 6   | `list` prefixed provider errors with `failed to get spot savings`        | Medium   | Fixed              |
| 7   | Empty `list` answer named no next move                                   | Medium   | Fixed              |
| 8   | MCP tool descriptions did not say which clouds each supports             | Medium   | Fixed              |
| 9   | `list --help` did not state the half of the discriminator it owns        | Low      | Fixed              |
| 10  | Unknown region exits 0 on GCP and Azure, 1 on AWS                        | Medium   | Not fixed          |
| 11  | AWS savings percent and AWS spot price come from two feeds that disagree | Low      | Not fixed          |
| 12  | Savings percent is truncated, not rounded                                | Low      | Not fixed          |
| 13  | `warnings` is present in both schemas and never populated                | Low      | Not fixed          |

---

## Correctness

### AWS — passed

Three instance types in three regions, `--offline` against the committed snapshot and again
against the live feeds.

The **spot price** was compared against `https://website.spot.ec2.aws.a2z.com/spot.json`, the
feed behind <https://aws.amazon.com/ec2/spot/pricing/>. The human page is client-rendered and
carries no numbers in its HTML — WebFetch returned "the actual pricing table appears to be
client-rendered" — so the feed is the only readable form of that page. It is also the feed the
binary reads, so this comparison proves the region key, the OS column and the unit are read
correctly; it is **not** independent evidence of the price.

| Region         | Machine    | Feed, direct | `spotinfo list` (live) |
| -------------- | ---------- | ------------ | ---------------------- |
| us-east-1      | m5.large   | 0.0410       | 0.041000000            |
| us-east-1      | c5.xlarge  | 0.0646       | 0.064600000            |
| us-east-1      | r5.2xlarge | 0.1857       | 0.185700000            |
| eu-west-1      | m5.large   | 0.0525       | 0.052500000            |
| eu-west-1      | c5.xlarge  | 0.0850       | 0.085000000            |
| eu-west-1      | r5.2xlarge | 0.2651       | 0.265100000            |
| ap-southeast-1 | m5.large   | 0.0447       | 0.044700000            |
| ap-southeast-1 | c5.xlarge  | 0.0806       | 0.080600000            |
| ap-southeast-1 | r5.2xlarge | 0.2372       | 0.237200000            |

Nine of nine exact, to the last digit.

The **savings percent and interruption range** were compared against the live Spot Advisor
document, `https://spot-bid-advisor.s3.amazonaws.com/spot-advisor-data.json`. All nine cells
match on both fields: the printed range label is `ranges[r].label` and the printed savings is
`s`, unmodified.

| Region         | Machine    | Advisor `s` / `r` | Printed savings / risk |
| -------------- | ---------- | ----------------- | ---------------------- |
| us-east-1      | m5.large   | 59 / 4            | 59% / `>20%`           |
| us-east-1      | c5.xlarge  | 58 / 2            | 58% / `10-15%`         |
| us-east-1      | r5.2xlarge | 60 / 4            | 60% / `>20%`           |
| eu-west-1      | m5.large   | 40 / 4            | 40% / `>20%`           |
| eu-west-1      | c5.xlarge  | 39 / 0            | 39% / `<5%`            |
| eu-west-1      | r5.2xlarge | 52 / 4            | 52% / `>20%`           |
| ap-southeast-1 | m5.large   | 63 / 4            | 63% / `>20%`           |
| ap-southeast-1 | c5.xlarge  | 52 / 1            | 52% / `5-10%`          |
| ap-southeast-1 | r5.2xlarge | 56 / 3            | 56% / `15-20%`         |

An **independent** cross-check of the savings figure used AWS's published on-demand price
index (`b0.p.awsstatic.com/pricing/2.0/…/ec2-ondemand-without-sec-sel/<region>/Linux/index.json`),
which the binary never reads. See finding 11: the two AWS feeds do not agree with each other.

### Azure — passed, including Windows

Three sizes in three regions, Linux and Windows, against the anonymous Retail Prices API
(`https://prices.azure.com/api/retail/prices`) filtered to `priceType eq 'Consumption'`. The
Azure pricing calculator is client-rendered and publishes no numbers in its HTML; the Retail
Prices API is the documented source behind it and is what the source contract names.

| Region        | Size            | OS      | Retail spot | Printed spot | Retail on-demand | Printed on-demand |
| ------------- | --------------- | ------- | ----------- | ------------ | ---------------- | ----------------- |
| eastus        | Standard_D2s_v5 | linux   | 0.020266    | 0.020266000  | 0.096            | 0.096000000       |
| eastus        | Standard_D2s_v5 | windows | 0.039687    | 0.039687000  | 0.188            | 0.188000000       |
| westeurope    | Standard_D4s_v5 | linux   | 0.042504    | 0.042504000  | 0.230            | 0.230000000       |
| westeurope    | Standard_D4s_v5 | windows | 0.076507    | 0.076507000  | 0.414            | 0.414000000       |
| southeastasia | Standard_E4s_v5 | linux   | 0.056362    | 0.056362000  | 0.304            | 0.304000000       |
| southeastasia | Standard_E4s_v5 | windows | 0.090475    | 0.090475000  | 0.488            | 0.488000000       |

Twelve of twelve exact.

**A Windows price is never presented as a saving against a Linux price.** Each row's savings is
its own OS's spot over its own OS's on-demand: `1 − 0.039687/0.188 = 78.9%` for Windows and
`1 − 0.020266/0.096 = 78.9%` for Linux in eastus. The two agree because Azure discounts the
whole Windows meter proportionally, not because the tool crossed them — the on-demand
divisors differ (0.188 against 0.096) and both are printed.

The Retail API is a trap-rich source and the parser handles all three traps observed in the
`Standard_D2s_v5`/eastus response: a `DevTestConsumption` row carrying the Linux price under a
`… Series Windows` product name, two `Low Priority` meters, and two `Reservation` rows priced
per year under a `1 Hour` unit. Filtering on `priceType eq 'Consumption'` and re-checking
`type` excludes all of them.

### GCP — mismatch, see findings 1 to 3

Three machine types against `https://cloud.google.com/spot-vms/pricing` and the two on-demand
category pages. The page is 18 MB and exceeds WebFetch's limit, so it was downloaded directly
and the rows read out of the HTML.

| Machine       | Vendor spot, 2026-08-12 | Printed spot | Vendor on-demand | Printed on-demand |
| ------------- | ----------------------- | ------------ | ---------------- | ----------------- |
| e2-standard-4 | 0.080424                | 0.080424000  | 0.13402284       | 0.134022840       |
| n2-standard-4 | **0.111472**            | 0.101336000  | 0.194236         | 0.194236000       |
| c2-standard-8 | **0.219776**            | 0.209328000  | 0.417616         | 0.417616000       |

On-demand matches on all three. Spot is wrong on two of three: the printed n2-standard-4 price
is **9.1% below** what Google publishes today and c2-standard-8 is **4.8% below**. The savings
percent inherits the error — 47% printed against the 42.6% today's two prices imply for
n2-standard-4. Five consecutive reads of the Spot page returned 0.111472 every
time, so this is not the CDN alternation the source contract warns about; the committed
snapshot is simply from 2026-08-09 and has not been refreshable since.

### Risk column — passed

A cloud that publishes no interruption figure never renders a number.

```
$ spotinfo list --cloud gcp --machine '^n2-standard-4$' --offline
│ us-central1 │ n2-standard-4 │    4 │         16 │                    47% │ unavailable │ 0.1013   │

$ spotinfo list --cloud azure --machine '^Standard_D2s_v5$' --region eastus --offline
│ Standard_D2s_v5 │    2 │          8 │                    78% │ unavailable │ 0.0203   │

$ spotinfo list --cloud aws --machine '^m5\.large$' --region us-east-1 --offline
│ m5.large │    2 │          8 │                    59% │ >20% │ 0.0399   │
```

`csv`, `text` and `json` agree: `risk=unavailable`, `"status": "unavailable"` with every other
risk field `null`. No zero, no blank, no AWS-shaped bucket.

### Capped workloads — passed, and named the vendor limit

```
$ spotinfo recommend --cloud gcp --architecture x86_64 --min-vcpu 2 --min-memory-gib 4 --workload web --offline
spotinfo: gcp: unsupported capability: risk: the web workload caps interruption frequency at 5%,
an AWS Spot Advisor bucket boundary, and gcp publishes no figure measured that way; workload cost
applies no ceiling and answers on every cloud

$ spotinfo recommend --cloud azure --architecture x86_64 --min-vcpu 2 --min-memory-gib 4 --workload batch --offline
spotinfo: azure: unsupported capability: risk: the batch workload caps interruption frequency at 22%,
an AWS Spot Advisor bucket boundary, and azure publishes no figure measured that way; workload cost
applies no ceiling and answers on every cloud
```

Both name the limit, the vendor, and the workload that does answer. Exit 1, empty stdout.

**Invariant 1 observed from the outside.** `--live-risk` does not make the figure filterable:

```
$ GOOGLE_CLOUD_PROJECT=fake-project spotinfo recommend --cloud gcp --architecture x86_64 \
    --min-vcpu 2 --min-memory-gib 4 --workload web --live-risk --offline
spotinfo: gcp: unsupported capability: risk: the web workload caps interruption frequency at 5%, …
```

The refusal happens before acquisition, so no request is made and no credential is read. The
other half — that `--live-risk` makes the preemption rate **visible** — needs Google
Application Default Credentials and a project this repository does not hold, and is
**unverified**. It belongs to the plan's Post-Completion list.

---

## Findings

### 1. The GCP snapshot updater could never write a snapshot — High, fixed

`make update-gcp-data` refused with `ErrSourceUnstable` (exit 75, `EX_TEMPFAIL`), whose message
says to wait and retry. Waiting would never have helped.

`fetch` read each contracted page twice and compared `snapshot.SHA256Hex` of the raw bodies.
Every response from `cloud.google.com` carries a fresh CSP `nonce="…"` attribute and a fresh
`FdrFJe` request id inside a script body, so the raw hash differs on every read whatever the
prices do. Measured with the updater's own User-Agent and `Accept-Language`:

| Read | Raw SHA-256 (first 16) | Text digest (first 20) | n2-standard-4 |
| ---- | ---------------------- | ---------------------- | ------------- |
| 1    | `5c76db2c98cf4bf1`     | `68435cfcd3368e33d49e` | 0.111472      |
| 2    | `ab14b91bd6585000`     | `68435cfcd3368e33d49e` | 0.111472      |
| 3    | `4cdcc81bdf1ea56a`     | `68435cfcd3368e33d49e` | 0.111472      |
| 4    | `1af02822fd0dd927`     | `68435cfcd3368e33d49e` | 0.111472      |

Four reads spread over 3 minutes 46 seconds, plus five more in a single window: nine different
raw hashes, one digest, one price.

The gate landed on 2026-08-10 (`34fb6bf`), one day **after** the last successful snapshot
(`60ae46e`, 2026-08-09). It has therefore never let a refresh through, and because exit 75 is
reported by the weekly workflow as a notice rather than a red run, nothing surfaced it.

**Fixed.** `cmd/update-gcp-data/stability.go` adds `stabilityDigest`, which strips script and
style bodies, comments and every tag, collapses whitespace and hashes the remaining text. Both
comparison sites — the per-page double read and `confirmWindowStable`'s bracket — now compare
digests. `page.sha256` is untouched, so the manifest still records the raw bytes this run read.
Two tests pin both directions: a page whose only change is a re-rolled nonce and request id is
accepted, and a page whose price cell moved is refused.

After the fix, `make update-gcp-data` gets past the stability gate on the first try — and fails
on finding 2 instead.

### 2. Google renamed the N4D on-demand column header — High, open

With the gate fixed, the refresh reaches the parser and fails:

```
skipped n4d-standard-4: spot price with no on-demand pair
… 27 n4d machines skipped …
update-gcp-data: invalid gcp catalogue: catalogue has no machine in approved series "n4d"
```

The N4D rows are on the general-purpose page — `n4d-standard-4 | 4 | 16 GiB | $0.1694 / 1 hour
| $0.121968 / 1 hour | …` — but that table's fourth header cell reads **`Price (USD)`**, while
`ParseOnDemandPage` requires the prefix **`Default* (USD)`**. Read out of the same download,
three other tables on that page still use the old spelling and only N4D does not:

| Table containing | Header cell 3    | Table offset |
| ---------------- | ---------------- | ------------ |
| `n4d-standard-4` | `Price (USD)`    | 3,164,704    |
| `c3d-standard-4` | `Default* (USD)` | 3,832,515    |
| `e2-standard-4`  | `Default* (USD)` | 4,055,513    |
| `n2-standard-4`  | `Default* (USD)` | 4,337,613    |

which is also why only the 27 N4D machines were skipped. The N4D table carries its own
consumption-model id (`7754-699E-0EBF`), so this is a per-series layout, not a page-wide
rename.

**Not fixed here, deliberately.** Accepting a second header spelling is exactly the move the
plan's safety notes forbid without review: "Never widen a parser to make a changed source fit.
Review the source, then bump `parser_version` in both the parser and the source contract." The
contract carries `review_status: approved` and `reviewer: alexei-led`, and its `expected_fields`
list names `on-demand table header cell 3 prefix: Default* (USD)` as a term of that approval. A
review agent may not re-approve it.

**Remedy, for the reviewer:** confirm from the page that `Price (USD)` on the N4D table is the
on-demand column and not a new consumption model, then accept both header spellings in
`cmd/update-gcp-data/parser.go`, update `expected_fields` and bump `parser_version` in
`internal/providers/gcp` and `internal/providers/gcp/data/source-contract.json` together, and
run `make update-gcp-data && make verify-data`.

The failure is now loud: exit 1, not the exit 75 the weekly workflow treats as a notice.

### 3. The committed GCP catalogue is stale and measurably wrong — Medium, blocked by 2

n2-standard-4 is 9.1% below the price Google publishes today, c2-standard-8 is 4.8% below, and
both savings figures inherit the error. The catalogue is honest about its provenance —
`data_source.mode` is `embedded-snapshot` and every source carries `fetched_at:
2026-08-09T05:26:02Z` — but GCP has no live price path without `--gcp-billing-key`, so the
snapshot _is_ the answer.

Findings 1 and 2 are the cause. Once finding 2 is reviewed, the weekly workflow lands the
refresh with no further code change.

### 4. Twelve identical credential warnings on a default `list` — High, fixed

`spotinfo list` with no AWS credentials answered correctly from the static feed and printed
this to stderr, twelve times, once per region, each about 450 characters:

```
level=WARN msg="failed to fetch live prices" region=us-west-2 error="live pricing unavailable:
no AWS credentials found: failed to refresh cached credentials, no EC2 IMDS role found,
operation error ec2imds: GetMetadata, exceeded maximum number of attempts, 3, request send
failed, Get \"http://169.254.169.254/latest/meta-data/iam/security-credentials/\": dial tcp
169.254.169.254:80: connect: host is down"
```

Thirteen stderr lines on a command that exited 0 with 38,709 rows of correct output. The first
thing a new reader sees is a wall of IMDS failures, and none of it says what to do.

The twelve are provably identical: `awsConfigWithCredentials` is a cached
`sync.OnceValues`, so every region gets the same error value.

**Fixed.** `enrichMissingPrices` now collects per-region failures and `reportLivePriceFailures`
reports them after the wait. Anything matching `errNoAWSCredentials` collapses into one line
that names the next move; every other error stays per region, so a throttled region is still
distinguishable from a healthy one. Observed after the fix:

```
level=WARN msg="no AWS credentials, so machines missing from the static price feed keep no
price; configure AWS credentials to price them, or pass --offline to skip the lookup"
regions="[me-central-1 me-south-1]"
```

### 5. `unknown cloud provider` did not name the accepted set — Medium, fixed

`spotinfo list --cloud oracle` said only `unknown cloud provider "oracle"`, while every sibling
message already lists its alternatives (`unknown output format "yaml", want one of
number|text|json|table|csv`; `unknown sort "bogus", want one of machine|price|region|risk|savings|score`).
A misspelled cloud is the one argument error whose reader may genuinely not know the options.

**Fixed.** `ParseProviderID` builds the list from `ProviderIDs()`, so it cannot drift:

```
spotinfo: invalid argument: unknown cloud provider "oracle", want one of aws|azure|gcp
```

### 6. `list` prefixed provider errors with `failed to get spot savings` — Medium, fixed

```
spotinfo: failed to get spot savings: aws candidate acquisition: region not found: nowhere-1
```

Three layers of prefix, and the outermost is the retired query command's wording — it claims a
savings lookup failed when what failed was a region name, on a command whose job is browsing.

**Fixed.** `answerList` returns the provider's error unwrapped; it already names the cloud and
the stage:

```
spotinfo: aws candidate acquisition: region not found: nowhere-1
```

### 7. An empty `list` answer named no next move — Medium, fixed

The empty-match warning echoed the filters but suggested nothing:

```
level=WARN msg="no machines matched the query" filters="[machine=zzz region=us-east-1]"
```

**Fixed.** It now names the two things that produce an empty page, one of which is finding 10:

```
level=WARN msg="no machines matched the query; relax a filter, or check the region name is one
this cloud publishes" filters="[machine=zzz region=us-east-1]"
```

`recommend` already did this well and was left alone — `no candidates: no machine has at least
9999 vCPU and 4 GiB of memory` names the binding constraint, and
`no candidates: azure publishes no machines in nowhere` names the region.

### 8. MCP tool descriptions did not say which clouds each supports — Medium, fixed

`tools/list` over stdio returned three tools with the right names, but no description mentioned
a cloud. The `cloud` argument's enum did (`["aws","azure","gcp"]`), which is not the same
thing: a client that reads descriptions to choose a tool learned nothing about coverage.

**Fixed.** Each description now states its coverage and its one asymmetry:

- `list_spot_machines` — "Answers on AWS, GCP and Azure; AWS is the only one publishing an
  interruption figure, and GCP prices Linux only."
- `recommend_spot_machines` — "Answers on AWS, GCP and Azure with workload 'cost'; the
  interruption-capped workloads 'web', 'ci' and 'batch' are AWS-only, because no other cloud
  publishes an interruption frequency."
- `list_cloud_regions` — "Answers on AWS, GCP and Azure; GCP publishes one region."

Each claim was checked against the binary: `list --cloud gcp --os windows` is refused, the GCP
catalogue publishes exactly `us-central1` and Azure 55 regions, and the two workload refusals
are quoted above. `internal/mcp/testdata/recommend-v3-input-schema.json` was hand-edited on the
one line that changed; no golden was regenerated.

### 9. `list --help` did not state the half of the discriminator it owns — Low, fixed

`recommend`'s one-liner already said "(requires --architecture, --min-vcpu and
--min-memory-gib)". `list`'s said nothing about requiring nothing, so on the command list a
reader saw one constrained command and one unexplained one.

**Fixed:**

```
list       list every matching machine with its price and risk (no flag is required)
recommend  rank the best Spot machines for a stated requirement (requires --architecture, --min-vcpu and --min-memory-gib)
```

Read side by side, that is the whole distinction. Both `--help` pages were re-read after the
change; nothing else needed rewording.

---

## Usability checks that passed as shipped

### `--region all` is discoverable and its cost is stated

`spotinfo list --help` and `spotinfo recommend --help` both carry it on the flag itself:

> one or more provider regions, or "all" for every published region; "all" on AWS queries every
> region, so pass --offline or an explicit --region when speed matters (default: "all")

`docs/quick-start.md` repeats it with a measured table pointing at `--offline` first. Measured
here, on a cold cache (`SPOTINFO_CACHE=off`):

| Invocation                                         | Wall time |
| -------------------------------------------------- | --------- |
| `list --machine '^m5\.large$'` (all regions, live) | 7.9 s     |
| `list --machine '^m5\.large$' --region us-east-1`  | 1.4 s     |
| `list --machine '^m5\.large$' --offline`           | 0.15 s    |
| `list` (no filter, all regions, live)              | 5.9 s     |

Eight seconds of silence is the slowest case, and it is what the help text and the quick-start
both warn about. Not a finding on its own; the wall of credential warnings that used to
accompany it was (finding 4).

### Rename hints name the replacement

All eight removed names, on `list`, exit 1 with empty stdout:

```
--type        →  spotinfo: invalid argument: --type was renamed to --machine
--instance    →  spotinfo: invalid argument: --instance was renamed to --machine
--vcpu        →  spotinfo: invalid argument: --vcpu was renamed to --min-vcpu
--cpu         →  spotinfo: invalid argument: --cpu was renamed to --min-vcpu
--memory      →  spotinfo: invalid argument: --memory was renamed to --min-memory-gib
--memory-gib  →  spotinfo: invalid argument: --memory-gib was renamed to --min-memory-gib
--price       →  spotinfo: invalid argument: --price was renamed to --max-price
--budget      →  spotinfo: invalid argument: --budget was renamed to --max-price
```

Every one names the replacement, not just the removal.

### The remaining error paths

Each was triggered by hand and judged on whether it says what to do next.

| Invocation                                    | Message                                                                                                                                                    | Verdict                                                      |
| --------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------ |
| `--output yaml`                               | `unknown output format "yaml", want one of number\|text\|json\|table\|csv`                                                                                 | good                                                         |
| `--sort bogus`                                | `unknown sort "bogus", want one of machine\|price\|region\|risk\|savings\|score`                                                                           | good                                                         |
| `--order sideways`                            | `unknown order "sideways", want asc or desc`                                                                                                               | good                                                         |
| `--architecture sparc`                        | `architecture must be x86_64 or arm64`                                                                                                                     | good                                                         |
| `--max-price -1`                              | `--max-price must be a positive USD machine-hour price`                                                                                                    | good                                                         |
| `--top 0`                                     | `top must be at least 1`                                                                                                                                   | good                                                         |
| `recommend` missing `--architecture`          | `--architecture is required; every recommendation needs an architecture and a size floor`                                                                  | good                                                         |
| `list --cloud gcp --os windows`               | `gcp: unsupported capability: os windows: this cloud publishes spot prices for linux only`                                                                 | good                                                         |
| `list --cloud gcp --az`                       | `gcp: unsupported capability: zone_detail: this cloud publishes prices per region, not per zone, …`                                                        | good                                                         |
| `list --cloud azure --with-score --min-score` | `--with-score is refused on azure: azure publishes a Spot Placement Score, but reading it needs an Azure subscription this build does not authenticate to` | good                                                         |
| `--machine '['`                               | `aws candidate acquisition: failed to match instance type: error parsing regexp: missing closing ]`                                                        | acceptable — Go's own regexp error is the clearest available |
| bare `spotinfo`                               | `no command given; run "spotinfo list" to browse or "spotinfo recommend" to rank`, exit 1                                                                  | good                                                         |

`spotinfo --mcp` starts and completes a handshake, `spotinfo --version` prints
`v2.5.0-95-gc293cc2`.

---

## Deliberately not fixed

### 10. Unknown region exits 0 on GCP and Azure, 1 on AWS — Medium

`list --cloud azure --region nowhere --offline` exits **0** with an empty table and the
empty-match warning; the same typo on AWS exits **1** with `region not found: nowhere-1`,
because the legacy AWS client validates against its region list. On `recommend`, Azure's
diagnosis does better still: `no candidates: azure publishes no machines in nowhere`.

Not fixed because the principled version — validating `--region` against each cloud's published
set before acquisition — costs a full catalogue query on every invocation. `cloud.RegionsOf`
derives the region list by querying every region, and `internal/cloud/provider.go` records that
this is deliberate: "a query over every region already yields the answer, so a method would
oblige all three providers and every test stub to implement what one query derives". Adding
that in the last task of the plan is not a change this review should make.

Finding 7's fix is the cheap half: the warning now tells the reader to check the region name.
The exit-code difference between `list` (0) and `recommend` (1) on an empty answer is by
design — browsing finds nothing, recommending fails to recommend — and is not part of this.

### 11. The two AWS feeds disagree with each other — Low

The savings percent comes from the Spot Advisor and the price comes from the pricing feed. They
are refreshed on different cadences, so the printed savings does not equal
`1 − spot/on-demand` computed from AWS's own published on-demand index:

| Region         | Machine    | Printed savings | Implied by AWS's own prices | Gap     |
| -------------- | ---------- | --------------- | --------------------------- | ------- |
| us-east-1      | c5.xlarge  | 58%             | 62.0%                       | 4.0 pp  |
| us-east-1      | m5.large   | 59%             | 57.3%                       | 1.7 pp  |
| us-east-1      | r5.2xlarge | 60%             | 63.2%                       | 3.2 pp  |
| eu-west-1      | c5.xlarge  | 39%             | 55.7%                       | 16.7 pp |
| eu-west-1      | m5.large   | 40%             | 50.9%                       | 10.9 pp |
| eu-west-1      | r5.2xlarge | 52%             | 53.0%                       | 1.0 pp  |
| ap-southeast-1 | c5.xlarge  | 52%             | 58.9%                       | 6.9 pp  |
| ap-southeast-1 | m5.large   | 63%             | 62.8%                       | 0.2 pp  |
| ap-southeast-1 | r5.2xlarge | 56%             | 61.0%                       | 5.0 pp  |

(on-demand from `b0.p.awsstatic.com/pricing/2.0/…`, spot from the live feed, both read
2026-08-12)

spotinfo reports each AWS number exactly as AWS publishes it, and `data_source.sources` names
both documents with their SHA-256, so the disagreement is attributable. Computing the savings
locally instead would replace AWS's published figure with a derived one and would need a fourth
data source — an on-demand price feed the contract does not name. Recorded, not changed.

### 12. Savings percent is truncated, not rounded — Low

Azure eastus `Standard_D2s_v5`: `1 − 0.020266/0.096 = 78.89%`, printed as `78`. Truncation
understates the saving, which is the conservative direction for a cost tool, and the underlying
prices are printed beside it. Not worth a contract change in the last task.

### 13. `warnings` is present in both schemas and never populated — Low

`spotinfo.list/v1` and `spotinfo.recommend/v3` both declare `warnings`, and
`internal/cloud/schema.go` initialises it to `[]` in both builders. Nothing anywhere appends to
it; every answer inspected carried `"warnings": []`. Removing a published field in the final
task of a plan is worse than leaving a documented empty one, and the field is the obvious home
for the snapshot-age note finding 3 would want.

### Unverified, needing a credential this repository does not hold

- **`--live-risk` makes the GCP preemption rate visible.** Needs Google Application Default
  Credentials and a billable project. The refusal half — that it stays unfilterable — was
  observed and is recorded above. Already on the plan's Post-Completion list.
- **`--gcp-billing-key` prices regions beyond the snapshot.** No key available;
  `make validate-clouds` skips that cell with its reason, as designed.
- **`--with-score` on GCP.** Same credential.

---

## Gates re-run after the fixes

```
make test                 pass
make lint                 pass, 0 issues
make verify-data          pass
make verify-architecture   pass, no open critical or high findings (32 reviewed)
make validate-clouds      pass
```

No golden was regenerated; `UPDATE_GOLDEN` and `REFRESH_MANIFESTS` were never set. The one
recorded contract that moved — the `recommend_spot_machines` description in
`internal/mcp/testdata/recommend-v3-input-schema.json` — was hand-edited on the single line
whose change was intended.
