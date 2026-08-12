# Quick start

Five minutes, from install to a ranked answer. For every install method, read
[installation.md](installation.md).

## 1. Install

```bash
brew install alexei-led/tap/spotinfo
```

## 2. Ask for a machine

Give `recommend` an architecture and a size floor. These three flags are required:

```console
$ spotinfo recommend --architecture x86_64 --min-vcpu 4 --min-memory-gib 16
RANK  CLOUD  REGION          MACHINE     ARCHITECTURE  vCPU  MEMORY GiB  USD/HOUR    SAVINGS  RISK          WHY
   1  aws    ap-south-1      t3a.xlarge  x86_64           4        16.0  0.0259          58%  10-15%        ARCHITECTURE_MATCH,COST_POLICY,KNOWN_POSITIVE_PRICE,RESOURCE_MINIMUMS_MET
   2  aws    ap-southeast-3  t3.xlarge   x86_64           4        16.0  0.0262          73%  <5%           ARCHITECTURE_MATCH,COST_POLICY,KNOWN_POSITIVE_PRICE,RESOURCE_MINIMUMS_MET
   3  aws    ca-west-1       r6i.xlarge  x86_64           4        32.0  0.0276          77%  >20%          ARCHITECTURE_MATCH,COST_POLICY,KNOWN_POSITIVE_PRICE,RESOURCE_MINIMUMS_MET
```

Every region is searched by default, which is why the answer is three regions you did not
name. Section 5 says what that costs and how to narrow it.

The `WHY` column lists the rule each candidate satisfied. It is deterministic, so two runs on
the same data give the same codes.

## 3. Change the cloud

```bash
spotinfo recommend --cloud gcp   --architecture x86_64 --min-vcpu 4 --min-memory-gib 16
spotinfo recommend --cloud azure --architecture arm64  --min-vcpu 4 --min-memory-gib 16
```

Neither needs credentials. Both report `RISK: unavailable`, because neither publishes an
interruption figure that can ship in the binary. GCP answers from that snapshot alone unless
you pass `--gcp-billing-key`; Azure refreshes prices from its anonymous Retail Prices API when
you name one or two regions. Read [clouds.md](clouds.md) for what each cloud serves.

## 4. Change the workload

`--workload` sets the ranking policy and the interruption ceiling.

| Workload | Interruption ceiling | Clouds          |
| -------- | -------------------- | --------------- |
| `cost`   | none                 | aws, gcp, azure |
| `web`    | 5%                   | aws             |
| `ci`     | 16%                  | aws             |
| `batch`  | 22%                  | aws             |

```console
$ spotinfo recommend --architecture x86_64 --min-vcpu 2 --min-memory-gib 8 --workload batch
RANK  CLOUD  REGION          MACHINE    ARCHITECTURE  vCPU  MEMORY GiB  USD/HOUR    SAVINGS  RISK          WHY
   1  aws    ap-south-1      t3a.large  x86_64           2         8.0  0.0125          61%  10-15%        ARCHITECTURE_MATCH,KNOWN_POSITIVE_PRICE,RESOURCE_MINIMUMS_MET,WORKLOAD_BATCH_CAP_MET
   2  aws    ap-southeast-3  t3.large   x86_64           2         8.0  0.0149          74%  <5%           ARCHITECTURE_MATCH,KNOWN_POSITIVE_PRICE,RESOURCE_MINIMUMS_MET,WORKLOAD_BATCH_CAP_MET
   3  aws    ca-west-1       t3.large   x86_64           2         8.0  0.0153          77%  5-10%         ARCHITECTURE_MATCH,KNOWN_POSITIVE_PRICE,RESOURCE_MINIMUMS_MET,WORKLOAD_BATCH_CAP_MET
```

The ceilings are AWS Spot Advisor bucket boundaries, so `web`, `ci` and `batch` are refused on
a cloud that measures something else. Every cloud accepts `cost`. The report schema is
`spotinfo.recommend/v3` for every workload and every cloud.

## 5. Widen or narrow the search

```bash
# Five results instead of three
spotinfo recommend --architecture x86_64 --min-vcpu 4 --min-memory-gib 16 --top 5

# Two named regions
spotinfo recommend --architecture arm64 --min-vcpu 2 --min-memory-gib 8 \
  --region us-east-1 --region eu-west-1

# A machine-name pattern, as an RE2 regexp
spotinfo recommend --architecture x86_64 --min-vcpu 4 --min-memory-gib 16 --machine "^c[0-9]"

# A price ceiling
spotinfo recommend --architecture x86_64 --min-vcpu 4 --min-memory-gib 16 --max-price 0.03
```

If a ceiling admits nothing, the command names the screen that emptied the set:

```console
$ spotinfo recommend --cloud gcp --architecture x86_64 --min-vcpu 4 --min-memory-gib 16 --max-price 0.001
spotinfo: no candidates: no machine costs 0.001000000 USD per hour or less; gcp publishes nothing below 0.042496000 USD per hour, the price of c3d-standard-4 in us-central1
```

`--region` defaults to `all` on every cloud. Comparing regions is the point of the tool, so
that default stays — but on AWS it has a real cost. Every region is queried, and every machine
the static price feed does not price falls through to a live EC2 call that the command waits
for. Seconds, not milliseconds.

**When speed matters, reach for `--offline` first.** It is the lever that always collapses the
wait, because it makes no price or risk request. Naming a region helps only when that region's
machines are all priced in the static feed. Measured on one machine, with no AWS credentials
to answer the fallback:

| Command                                      | All regions | `--region us-east-1` | `--offline` |
| -------------------------------------------- | ----------- | -------------------- | ----------- |
| `spotinfo list --machine '^m5\.'`            | 4.19 s      | 0.11 s               | 0.12 s      |
| `spotinfo recommend --architecture x86_64 …` | 4.4 s       | 4.2 s                | 0.12 s      |

The `recommend` row is the one to remember: naming a single region bought nothing there,
because that region also holds machines the static feed does not price.

## 6. Get JSON

```bash
spotinfo recommend --cloud azure --architecture arm64 --min-vcpu 4 --min-memory-gib 16 \
  --output json
```

The report carries the request that produced it, the ranking policy, every source URL with
its SHA-256, and the ranked machines. Pipe it into `jq`:

```console
$ spotinfo recommend --cloud gcp --architecture x86_64 --min-vcpu 8 --min-memory-gib 32 --output json \
  | jq -r '.recommendations[] | "\(.machine) \(.spot_usd_per_hour)"'
c3d-standard-8 0.084992000
n2d-standard-8 0.107648000
c3-standard-8 0.108704000
```

## 7. Browse instead of ranking

`spotinfo list` answers a different question: not "what should I run" but "what is there". It
requires no flags, prints every match with its price and its risk, and works on all three
clouds.

```console
$ spotinfo list --machine "^m5\." --region us-east-1 --sort price --output text
machine=m5.large, vCPU=2, memory=8GiB, saving=59%, risk='>20%', price=0.0410
machine=m5.xlarge, vCPU=4, memory=16GiB, saving=68%, risk='>20%', price=0.0621
machine=m5.2xlarge, vCPU=8, memory=32GiB, saving=64%, risk='>20%', price=0.1518
machine=m5.4xlarge, vCPU=16, memory=64GiB, saving=67%, risk='>20%', price=0.2967
```

```bash
# With AWS placement scores, cheapest first. This one needs AWS credentials
spotinfo list --machine "^m5\." --region us-east-1 --with-score --sort price

# CSV for a spreadsheet
spotinfo list --min-vcpu 4 --min-memory-gib 16 --region us-east-1 --output csv
```

`list` also has an `--output number` that `recommend` refuses: it prints one savings percent,
which cannot describe a ranked page.

One of the two commands is always needed. `spotinfo` on its own prints the help and exits
non-zero:

```console
$ spotinfo
spotinfo: invalid argument: no command given; run "spotinfo list" to browse or "spotinfo recommend" to rank
```

## 8. Work offline

```bash
spotinfo recommend --architecture x86_64 --min-vcpu 2 --min-memory-gib 8 --offline
```

`--offline` answers from the snapshot inside the binary and makes no price or risk request. It
is accepted on every cloud, and it changes what happens on each of them:

- **AWS** stops fetching the advisor and price feeds, and stops the live EC2 price fallback.
  That is the whole four-second wait from section 5.
- **Azure** stops the Retail Prices refresh that a one- or two-region query would make.
- **GCP** stops the Cloud Billing Catalog lookup that `--gcp-billing-key` enables. With no key
  there is nothing to stop, so the flag changes nothing you can see.

It does **not** suppress `--with-score`. A placement figure has no snapshot to come from, so
that one flag still calls the cloud even under `--offline`.

`--offline` is a flag on `list` and on `recommend`, not on the root command. Over MCP, pass
the `offline: true` tool argument instead.

## Next

- [Usage guide](usage.md) — every flag and every output format
- [Cloud coverage](clouds.md) — what each cloud serves and refuses
- [Examples](examples.md) — Terraform, CI pipelines, cost monitors
- [MCP server](mcp-server.md) — let an assistant ask these questions
