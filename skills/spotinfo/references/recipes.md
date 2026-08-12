# Recipes

Task-shaped examples. Each one is a question a user actually asks, the command
that answers it, and what to do with the result.

All of these were run against the shipped binary. Add `--offline` when the user
does not need today's price: it answers from the embedded snapshot in about
40 ms and makes no network call.

## Cheapest machine meeting a requirement

> "What's the cheapest 8-core machine with 32 GB I can get?"

```bash
spotinfo recommend --cloud aws --offline --output json \
  --architecture x86_64 --min-vcpu 8 --min-memory-gib 32 --top 5
```

Rows arrive ranked under `recommendations`. Report price, savings and risk
together — rank 1 is cheapest, not safest.

## Compare architectures

> "Would arm64 save us money on CI runners?"

```bash
for arch in arm64 x86_64; do
  spotinfo recommend --cloud aws --offline --output json \
    --architecture "$arch" --min-vcpu 4 --min-memory-gib 8 --top 3
done
```

Compare `spot_usd_per_hour` after `tonumber`. Mention the architecture change
cost: arm64 needs images built for it.

## Cheapest region for a family

> "Where are g5 GPU instances cheapest?"

```bash
spotinfo list --cloud aws --offline --output json \
  --machine '^g5\.' --sort price --order asc
```

`--machine` takes an RE2 regexp. Anchor it: `'^g5\.'` matches `g5.xlarge` but
not `g5g.xlarge`.

## Stay under a budget

> "What can I run for under 5 cents an hour?"

```bash
spotinfo list --cloud azure --output json \
  --max-price 0.05 --min-vcpu 4 --sort price
```

`--max-price` is USD per machine-hour.

## Compare one workload across clouds

> "Is AWS or Azure cheaper for this service?"

Machine naming differs per cloud, so there is no single cross-cloud query. Run
one per cloud with the same requirement and compare:

```bash
spotinfo recommend --cloud aws   --offline --output json --architecture x86_64 --min-vcpu 4 --min-memory-gib 16 --top 3
spotinfo recommend --cloud azure          --output json --architecture x86_64 --min-vcpu 4 --min-memory-gib 16 --top 3
spotinfo recommend --cloud gcp            --output json --architecture x86_64 --min-vcpu 4 --min-memory-gib 16 --top 3
```

`scripts/compare-clouds.sh` does this and prints one merged table.

State plainly that only the AWS rows carry an interruption figure. A reader
comparing three prices will assume the risk is comparable too.

## Windows pricing

> "What does this cost on Windows?"

```bash
spotinfo list --cloud azure --output json --os windows --machine '^Standard_D4s_v5$'
```

Works on AWS and Azure. GCP refuses it — Google publishes Linux Spot prices
only.

## Check a specific machine in a specific region

> "What's an m5.xlarge going for in eu-west-1?"

```bash
spotinfo list --cloud aws --offline --output json \
  --machine '^m5\.xlarge$' --region eu-west-1
```

Anchor both ends of the pattern for an exact match.

## Is this price current

```bash
spotinfo list --cloud azure --output json --machine '^Standard_D2s_v5$' --region westeurope \
  | jq '.data_source | {mode, provider}'
```

`mode` is `live`, `cached` or `embedded-snapshot`. Answer the user's freshness
question from this field rather than assuming.

## Placement scores

> "Which region is most likely to actually give me capacity?"

```bash
# AWS: integer 1-10, needs credentials and ec2:GetSpotPlacementScores
spotinfo recommend --cloud aws --with-score --output json \
  --architecture x86_64 --min-vcpu 8 --min-memory-gib 32 --top 5
```

Cannot be combined with `--offline`, because no snapshot carries a placement
figure. On GCP the equivalent figure is `obtainability`, a 0.0-1.0 probability
from a beta API needing `--gcp-project`. Do not compare the two numbers.

## Parsing with jq

Cheapest row as a single line:

```bash
spotinfo list --cloud aws --offline --output json --machine '^c7g\.' \
  | jq -r '[.candidates[] | select(.spot_usd_per_hour != null)]
           | sort_by(.spot_usd_per_hour | tonumber)[0]
           | "\(.machine) \(.region) $\(.spot_usd_per_hour)/h \(.savings_percent)% " +
             (if .risk.status == "available" then .risk.label else "risk not published" end)'
```

Prints `c7g.medium us-east-1 $0.006500000/h 71% <5%`.

Two guards in there are load-bearing. `select(.spot_usd_per_hour != null)` drops
machines the price feed omits, which would otherwise sort as if free. And the
risk test must be `if/then/else` — jq's `and` and `or` return booleans, so the
shorter `.risk.status == "available" and .risk.label or "..."` prints `true`
instead of the label.
