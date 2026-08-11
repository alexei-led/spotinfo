# Migrating to spotinfo v2

This release breaks compatibility on purpose. Every concept now has exactly one name, both
commands answer on all three clouds, and one schema family serves both surfaces. Nothing below
is deprecated-but-working: a retired name is refused.

Everything an upgrade needs is on this page. The per-field detail for the JSON payloads is in
[API reference](api-reference.md#migration-from-the-retired-v1-surface).

## The one-minute version

| Before                                                     | After                                                                                        |
| ---------------------------------------------------------- | -------------------------------------------------------------------------------------------- |
| `spotinfo --type m5.large`                                 | `spotinfo list --machine m5.large`                                                           |
| `spotinfo recommend --cpu 4 --memory 16 --instance 'm5\.'` | `spotinfo recommend --architecture x86_64 --min-vcpu 4 --min-memory-gib 16 --machine 'm5\.'` |
| MCP tool `find_spot_instances`                             | MCP tool `list_spot_machines`                                                                |
| `spotinfo.recommend/v1`, `/v2`                             | `spotinfo.list/v1`, `spotinfo.recommend/v3`                                                  |

## Commands

There is no root query command. `spotinfo` with no subcommand prints help and exits 1:

```console
$ spotinfo
spotinfo: invalid argument: no command given; run "spotinfo list" to browse or "spotinfo recommend" to rank
```

| Retired                         | Now                  | Note                                                                        |
| ------------------------------- | -------------------- | --------------------------------------------------------------------------- |
| `spotinfo <flags>` (root query) | `spotinfo list`      | Answers on AWS, GCP and Azure. Renders a `RISK` column, not an AWS-only one |
| `spotinfo recommend`            | `spotinfo recommend` | Same name, cloud-neutral, and `--architecture` is now required              |

`spotinfo --mcp` and `spotinfo --version` are unchanged.

**Flags are scoped to the subcommand now.** Only `--mcp`, `--debug`, `--quiet`, `--json-log`,
`--help` and `--version` are global; everything else must follow the command. Two invocations
that used to parse no longer do:

```console
$ spotinfo --cloud gcp recommend --architecture arm64 --min-vcpu 4 --min-memory-gib 16
spotinfo: flag provided but not defined: -cloud

$ spotinfo --offline --mcp
spotinfo: flag provided but not defined: -offline
```

Move the flag after the command (`spotinfo recommend --cloud gcp …`). On the MCP surface,
`--offline` is replaced by the per-call `offline` argument.

## Flags

Eight names are retired. Each is refused with a rename hint naming its replacement, exits 1,
and prints nothing to stdout — never `flag provided but not defined`.

| Retired flag   | Now                | Why                                          | Measured refusal                                                           |
| -------------- | ------------------ | -------------------------------------------- | -------------------------------------------------------------------------- |
| `--type`       | `--machine`        | one word for a machine on three clouds       | `spotinfo: invalid argument: --type was renamed to --machine`              |
| `--instance`   | `--machine`        | the `recommend` spelling of the same concept | `spotinfo: invalid argument: --instance was renamed to --machine`          |
| `--vcpu`       | `--min-vcpu`       | it is a floor, not an exact count            | `spotinfo: invalid argument: --vcpu was renamed to --min-vcpu`             |
| `--cpu`        | `--min-vcpu`       | `cpu` loses both the "minimum" and the "v"   | `spotinfo: invalid argument: --cpu was renamed to --min-vcpu`              |
| `--memory`     | `--min-memory-gib` | a floor, and the unit belongs in the name    | `spotinfo: invalid argument: --memory was renamed to --min-memory-gib`     |
| `--memory-gib` | `--min-memory-gib` | carried the unit but not the floor           | `spotinfo: invalid argument: --memory-gib was renamed to --min-memory-gib` |
| `--price`      | `--max-price`      | it is a ceiling                              | `spotinfo: invalid argument: --price was renamed to --max-price`           |
| `--budget`     | `--max-price`      | the `recommend` spelling of the same ceiling | `spotinfo: invalid argument: --budget was renamed to --max-price`          |

Every surviving name is carried by **both** commands unless the concept belongs to only one:
`--workload`, `--top` and `--live-risk` are `recommend`-only, and `number` output is
`list`-only.

## Defaults

The CLI and the MCP surface now use the same value for every default, so the same question
asked either way returns the same document.

| Flag         | Was                                                         | Now                    |
| ------------ | ----------------------------------------------------------- | ---------------------- |
| `--region`   | `us-east-1` on AWS                                          | `all` on every cloud   |
| `--workload` | unset, resolving to `web` on a cloud with interruption data | `cost` on every cloud  |
| `--sort`     | `interruption` on the root query command                    | **no default**         |
| `--top`      | 3                                                           | 3; above 50 is refused |
| `--os`       | `linux`                                                     | `linux`                |
| `--output`   | `table`                                                     | `table`                |

Three consequences worth planning for:

- **`--region all` is the default on AWS now**, and on `list` it queries every region. That is
  slower, and the live-price fallback fires per region. Pass `--offline` or an explicit
  `--region` when speed matters.
- **`--workload cost` makes no interruption claim.** A script that relied on the old implicit
  `web` ceiling must now pass `--workload web` explicitly — and only AWS accepts it.
- **`--sort` has no default.** An unset key leaves the order to the provider. The old
  `--sort interruption` is now `--sort risk`, and `--sort type` is now `--sort machine`; both
  are refused on GCP and Azure by `list`, which has no risk column to order by.

## MCP tools

All three tools were renamed. None carries a cloud in its name any more, and all three serve
AWS, GCP and Azure.

| Retired                    | Now                       | Emits                   | Note                                        |
| -------------------------- | ------------------------- | ----------------------- | ------------------------------------------- |
| `find_spot_instances`      | `list_spot_machines`      | `spotinfo.list/v1`      | Now serves three clouds                     |
| `recommend_spot_instances` | `recommend_spot_machines` | `spotinfo.recommend/v3` | `architecture` is now required              |
| `list_spot_regions`        | `list_cloud_regions`      | `spotinfo.regions/v1`   | Takes a `cloud` argument; lists all sources |

A retired name is a JSON-RPC error, not a payload:

```json
{
  "code": -32602,
  "message": "tool 'find_spot_instances' not found: tool not found"
}
```

**Update any published client configuration that names an old tool.** See
[Claude Desktop setup](claude-desktop-setup.md).

### Tool arguments

Every MCP argument is now derived from its CLI flag: strip the leading `--`, replace `-` with
`_`. The one exception is the repeatable `--region`, which becomes the array `regions`.

| Retired argument        | Now                                                                                 |
| ----------------------- | ----------------------------------------------------------------------------------- |
| `instance_types`        | `machine`                                                                           |
| `min_memory_gb`         | `min_memory_gib`                                                                    |
| `max_price_per_hour`    | `max_price`                                                                         |
| `sort_by`               | `sort` plus `order`; `reliability` is now `risk`                                    |
| `limit`                 | `top`, on `recommend_spot_machines` only — `list_spot_machines` returns every match |
| `max_interruption_rate` | No replacement. Use `workload` (`web`, `ci`, `batch`) on `recommend_spot_machines`  |

`recommend_spot_machines` declares no `sort`, `order`, `with_score`, `min_score`, `az`,
`score_timeout` or `live_risk` argument. A ranked page's placement figures are reachable from
the CLI only.

## Schemas

| Retired                                                   | Now                                             |
| --------------------------------------------------------- | ----------------------------------------------- |
| `spotinfo.recommend/v1`                                   | `spotinfo.recommend/v3`                         |
| `spotinfo.recommend/v2`                                   | `spotinfo.recommend/v3`                         |
| the bare JSON array the root query command printed        | `spotinfo.list/v1`, an object with `candidates` |
| the unversioned `{regions, total}` of `list_spot_regions` | `spotinfo.regions/v1`                           |

`spotinfo.error/v1` is unchanged.

The workload no longer selects a schema. `spotinfo.recommend/v3` answers for every cloud and
every workload, and it shares its candidate, risk, price and source shapes with
`spotinfo.list/v1`. The normative JSON Schemas are in
[`docs/plans/contracts/`](plans/contracts/).

The `list` document is an object, not an array. A `jq` expression that read the old root
output needs one more step:

```bash
# before
spotinfo --type 'm5\.' --output json | jq '.[0].price'

# now
spotinfo list --machine 'm5\.' --output json | jq '.candidates[0].spot_usd_per_hour'
```

### Renamed response fields

| Retired                                                       | Now                                                       |
| ------------------------------------------------------------- | --------------------------------------------------------- |
| `instance_type`, `instance`                                   | `machine`                                                 |
| `price` (number), `spot_price_per_hour` (number)              | `spot_usd_per_hour` (decimal string, 9 fractional digits) |
| `savings` (integer), `savings_percentage`                     | `savings_percent`                                         |
| `interruption_frequency`, `interruption_range`, `range.label` | `risk.label`                                              |
| `interruption_rate`, `range.min`, `range.max`                 | `risk.min_percent`, `risk.max_percent`                    |
| `memory_gb`, `info.ram_gb`                                    | `memory_gib` (the figure is GiB)                          |
| `info.cores`                                                  | `vcpu`                                                    |
| `zone_price`                                                  | `zone_prices` (decimal strings)                           |
| `metadata.regions_searched`                                   | `request.regions`                                         |
| `metadata.data_source`, `metadata.data_freshness`             | `data_source.provider`, `data_source.mode`                |

`region_score`, `zone_scores`, `score_fetched_at` and `live_price` keep their names.

### Fields removed with no replacement

- **`info.emr`, and the whole `info` block with it.** This is the one **capability** the
  release drops rather than renames. EMR compatibility is an AWS Spot Advisor classification
  with no meaning on GCP or Azure, so the provider-neutral candidate carries no equivalent.
  It was not cheap to drop: **731 of the 1,192 instance types** in the advisor snapshot
  published it as `true`. Publishing them all as a silent `false` would have been a wrong
  value rather than an absent one, so the field is retired with the schema. If you filter on
  EMR compatibility, read the AWS Spot Advisor feed directly — spotinfo no longer carries it.
  `vcpu` and `memory_gib` are now top-level candidate fields.
- `spot_price` (`"$0.0104/hour"`), the string `savings` (`"68% cheaper than on-demand"`) and
  `specs` (`"1 vCPU, 1 GB RAM"`) — pre-formatted display strings. Format from the numbers.
- `reliability_score` — it was `100 - interruption_rate`, a derived figure no cloud publishes.
  Read `risk`, which states its own availability.
- `metadata.total_results` — count `candidates`. `metadata.query_time_ms` — measure it.

## What did not change

- `spotinfo --mcp`, `SPOTINFO_MODE=mcp` and the MCP protocol version `2024-11-05`
- `spotinfo --version`
- `SPOTINFO_CACHE`, `SPOTINFO_CACHE_DIR` and the feed cache behaviour
- The AWS data sources, their weekly refresh, and the `live_price` fallback
- No credentials are required for any cloud. AWS credentials still unlock live prices and
  placement scores; GCP credentials still unlock `--live-risk` and `--with-score`; Azure still
  needs none
