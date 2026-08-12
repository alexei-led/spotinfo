# Output schema

Both commands emit one JSON document when you pass `--output json`. This file is
the field-by-field contract. Read it when you are writing a parser or when a
field is not what you expected.

The published JSON Schema files live in the repository under
`docs/plans/contracts/`: `list-v1.schema.json`, `recommend-v3-success.schema.json`,
`recommend-v3-input.schema.json` and `recommend-v3-error.schema.json`.

## Envelope

| Field             | Type   | Notes                                                                                                |
| ----------------- | ------ | ---------------------------------------------------------------------------------------------------- |
| `schema_version`  | string | `spotinfo.list/v1` or `spotinfo.recommend/v3`. Check it before parsing.                              |
| `status`          | string | `ok` when the query answered.                                                                        |
| `request`         | object | Echo of the resolved request, after defaults are applied. Read it to see what the tool actually ran. |
| `data_source`     | object | Where the numbers came from. See below.                                                              |
| `warnings`        | array  | Non-fatal notes. Empty most of the time.                                                             |
| `candidates`      | array  | **`list` only.** The matching machines.                                                              |
| `recommendations` | array  | **`recommend` only.** The ranked page.                                                               |
| `ranking_policy`  | array  | `recommend` only. The ordered criteria used to rank.                                                 |

The row key differs by command, and the two documents otherwise look alike. A
parser written for one finds nothing in the other and reports zero results
rather than failing, which is the worse outcome.

## `data_source`

| Field             | Meaning                                                                    |
| ----------------- | -------------------------------------------------------------------------- |
| `provider`        | `aws`, `gcp` or `azure`.                                                   |
| `mode`            | `live`, `cached`, or `embedded-snapshot`.                                  |
| `sources`         | The URLs the answer is traceable to, trimmed to those a row actually uses. |
| `sources_omitted` | How many sources were read but not listed.                                 |

`mode` is a claim about recency, so treat it as one. `live` means the origin
answered this run. `cached` means an unexpired local copy answered. A copy the
origin confirmed with a 304 counts as `live`, because it matches the origin now.
`embedded-snapshot` means the binary answered from the data compiled into it,
which can be weeks old.

If a user asks "is this price current", the honest answer comes from this field.

## Row fields

Common to both commands:

| Field                    | Type                 | Notes                                   |
| ------------------------ | -------------------- | --------------------------------------- |
| `cloud`                  | string               | `aws`, `gcp`, `azure`.                  |
| `region`                 | string               | Provider region name.                   |
| `machine`                | string               | Instance type, VM size or machine type. |
| `architecture`           | string               | `x86_64` or `arm64`.                    |
| `os`                     | string               | `linux` or `windows`.                   |
| `vcpu`                   | number               | Cores.                                  |
| `memory_gib`             | number               | Memory in GiB.                          |
| `spot_usd_per_hour`      | **string, nullable** | Fixed-point decimal. See below.         |
| `on_demand_usd_per_hour` | string, nullable     | Often `null`.                           |
| `savings_percent`        | number, nullable     | Discount against on-demand.             |
| `risk`                   | object               | Always present. See below.              |

`list` rows add `live_price` (boolean). `recommend` rows add `rank` (1-based)
and `rationale_codes` (why the row ranked where it did).

### Prices are strings

`"0.047500000"` is a fixed-point decimal, not a float. The tool carries money
this way so the last digit cannot drift through a float round-trip. Convert
explicitly before you compare or total:

```bash
# jq: tonumber before arithmetic
spotinfo list --cloud aws --offline --output json --machine '^m5\.' \
  | jq '[.candidates[] | {machine, usd: (.spot_usd_per_hour | tonumber)}] | sort_by(.usd)[0]'
```

`spot_usd_per_hour` is nullable on `list`. AWS omits some machines from its
static price feed, and a machine with no price carries `null` rather than `0`.
A zero would read as free. On `recommend` the field is never null, because an
unpriced machine cannot be ranked and is dropped before ranking.

### `risk`

```json
"risk": {
  "status": "available",
  "kind": "interruption_bucket",
  "label": "<5%",
  "min_percent": 0,
  "max_percent": 5,
  "window_days": 30,
  "source_url": "https://spot-bid-advisor.s3.amazonaws.com/spot-advisor-data.json",
  "observed_at": "..."
}
```

When the cloud publishes nothing, every field except `status` is `null`:

```json
"risk": { "status": "unavailable", "kind": null, "label": null,
          "min_percent": null, "max_percent": null, "window_days": null,
          "source_url": null, "observed_at": null }
```

Read `status` first and only read `label` when it is `available`. Reading
`label` unconditionally yields `null`, which prints as an empty cell and reads
to a person as "no risk".

`kind` names the measurement, and different kinds are not comparable. AWS
publishes `interruption_bucket`, a range of the fraction of running instances
reclaimed over `window_days`. GCP can publish `preemption_rate` under
`--live-risk`, which divides preempted instances by instances that stopped for
any reason. These two numbers answer different questions, so do not rank one
against the other.

### `placements`

Present only when `--with-score` was passed. Each entry carries a `kind`:

| `kind`            | Cloud | Value                                                            |
| ----------------- | ----- | ---------------------------------------------------------------- |
| `placement_score` | AWS   | `score`, integer 1-10                                            |
| `obtainability`   | GCP   | `obtainability`, float 0.0-1.0, plus optional `estimated_uptime` |

These are deliberately not normalized onto one scale. An integer rank and a
probability measure different things, and averaging them invents a number no
vendor published.

An empty `placements` array is ambiguous on its own, so a candidate also carries
a placement status that separates "no score was requested" from "a score was
requested and the cloud could not produce one".

## Errors

A refused request exits non-zero, prints nothing on stdout, and writes one line
to stderr. Over MCP the same refusal arrives as a `spotinfo.error/v1` body with a
stable `code`, such as `UNSUPPORTED_CAPABILITY` or `INVALID_ARGUMENT`.

Branch on the exit code, then read stderr. The message names the flag, the cloud
and the vendor limit, and usually names the flag to use instead.
