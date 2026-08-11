# Troubleshooting

Every message quoted here was produced by the binary. Refusals go to **stderr** and exit 1;
answers go to stdout. If you are piping to `jq` and getting parse errors, that split is
usually the reason — `2>/dev/null` keeps the pipe clean.

## Start here

```console
$ spotinfo --version
spotinfo v2.5.0-86-g2f2b80b
  Build date: 2026-08-12T00:54:59+0300
  Git commit: 2f2b80b
  Git branch: feat/spotinfo-multicloud-v2
```

A smoke test that needs no network and no credentials:

```console
$ spotinfo list --offline --region us-east-1 --machine '^m5\.large$' --output text
machine=m5.large, vCPU=2, memory=8GiB, saving=59%, risk='>20%', price=0.0399
```

If that prints a row, the binary and its embedded data are fine and the problem is in the
query. Read on.

## Migrating from the pre-2.x command line

Four changes account for nearly every "it used to work" report.

### There is no root query command

```console
$ spotinfo
spotinfo: invalid argument: no command given; run "spotinfo list" to browse or "spotinfo recommend" to rank
```

The help page is printed to stdout alongside it, and the exit code is 1. Pick a command:
`list` browses a catalogue, `recommend` ranks one.

### A flag before the subcommand is not defined

Only four flags are global — `--mcp`, `--debug`, `--quiet`, `--json-log`, plus `--help` and
`--version`. Every flag that describes a _query_ belongs to the subcommand:

```console
$ spotinfo --offline list --region us-east-1
spotinfo: flag provided but not defined: -offline

$ spotinfo list --offline --region us-east-1     # correct
```

The same rule explains a confusing one: **`--offline` cannot be combined with `--mcp`.**

```console
$ spotinfo --offline --mcp
spotinfo: flag provided but not defined: -offline
```

Over MCP, pass the per-call tool argument `"offline": true` instead.

`--workload`, `--top` and `--live-risk` belong to `recommend` only, so they are undefined on
`list`:

```console
$ spotinfo list --offline --top 3
spotinfo: flag provided but not defined: -top
```

### Eight flags were renamed

A retired name produces a rename hint rather than a parse error, on both commands:

```console
$ spotinfo list --offline --type 'm5.*'
spotinfo: invalid argument: --type was renamed to --machine

$ spotinfo list --offline --cpu 4
spotinfo: invalid argument: --cpu was renamed to --min-vcpu
```

| Old                        | New                | Why                                    |
| -------------------------- | ------------------ | -------------------------------------- |
| `--type`, `--instance`     | `--machine`        | one word for a machine on three clouds |
| `--vcpu`, `--cpu`          | `--min-vcpu`       | it is a floor, not an exact count      |
| `--memory`, `--memory-gib` | `--min-memory-gib` | a floor, and the unit is in the name   |
| `--price`, `--budget`      | `--max-price`      | it is a ceiling                        |

A name that was never a spotinfo flag gets the plain parser error instead, so the two cases
are distinguishable:

```console
$ spotinfo list --offline --limit 1
spotinfo: flag provided but not defined: -limit
```

`--limit` has no replacement. `list` has no row cap; use `--machine`, `--min-vcpu`,
`--min-memory-gib` and `--max-price` to narrow the query, or `recommend --top N` to rank.

### The three MCP tools were renamed

`find_spot_instances`, `recommend_spot_instances` and `list_spot_regions` are gone. A call to
one is a JSON-RPC error, not a payload:

```json
{
  "code": -32602,
  "message": "tool 'find_spot_instances' not found: tool not found"
}
```

The current names are `list_cloud_regions`, `list_spot_machines` and
`recommend_spot_machines`. Their arguments are the CLI flag names with `--` stripped and `-`
replaced by `_`; the repeatable `--region` becomes the array `regions`.

## Refusals from `--sort`, `--with-score` and their companions

### `--sort score needs --with-score`

```console
$ spotinfo list --offline --sort score
spotinfo: invalid argument: --sort score needs --with-score, which is what fetches the placement figures it orders by
```

Ordering by a figure nothing fetched would silently order by nothing at all, so the flag pair
is required rather than assumed. Add `--with-score` — which on AWS needs credentials and the
`ec2:GetSpotPlacementScores` permission — or drop `--sort score`.

The same rule covers the other three companions of `--with-score`. It is presence-based, so
`--min-score 0` is refused too:

| Flag              | Message                                                                                       |
| ----------------- | --------------------------------------------------------------------------------------------- |
| `--az`            | `--az needs --with-score, which is what fetches the placement scores it splits by zone`       |
| `--min-score`     | `--min-score needs --with-score, which is what fetches the placement scores it filters on`    |
| `--score-timeout` | `--score-timeout needs --with-score, which is what fetches the placement scores it waits for` |

### `--sort score cannot be combined with --az`

```console
$ spotinfo recommend --offline --architecture x86_64 --min-vcpu 2 --min-memory-gib 4 --sort score --with-score --az
spotinfo: invalid argument: --sort score cannot be combined with --az: only a regional placement figure orders a page, and --az asks for one figure per zone instead
```

A row has one position in a sorted page, and `--az` gives it several figures. Pick one: sort
by the regional score, or ask for the zone breakdown and read it.

### `gcp cannot order a catalogue by obtainability`

```console
$ spotinfo list --cloud gcp --sort score
spotinfo: unsupported capability: gcp cannot order a catalogue by obtainability, which it fetches for a ranked page instead
```

GCP's placement figure is **obtainability** — a 0.0-1.0 probability from a beta Google API,
not an integer score — and it is fetched one ranked page at a time, never for a whole
catalogue. Two consequences:

```console
$ spotinfo list --cloud gcp --with-score
spotinfo: unsupported capability: gcp obtainability is fetched for a ranked recommendation only, and needs a Google Cloud project

$ spotinfo list --cloud gcp --with-score --min-score 5
spotinfo: unsupported capability: --min-score is refused on gcp: gcp publishes obtainability, and an integer 1-10 floor states no reviewed mapping onto it
```

**Solution:** ask GCP for a ranked page instead, with a project to bill the call to:

```bash
spotinfo recommend --cloud gcp --architecture x86_64 --min-vcpu 2 --min-memory-gib 4 \
  --with-score --gcp-project my-project
```

Without a project it is refused before anything is fetched
(`--with-score needs a project; pass --gcp-project or set GOOGLE_CLOUD_PROJECT`). With a
project it cannot reach, the `SCORE` column reads `unavailable`, a warning goes to stderr and
the run exits 0.

### `azure: unsupported capability: placement_score`

```console
$ spotinfo list --cloud azure --sort score
spotinfo: azure: unsupported capability: placement_score: this cloud publishes no placement figure

$ spotinfo list --cloud azure --with-score
spotinfo: unsupported capability: --with-score is refused on azure: azure publishes a Spot Placement Score, but reading it needs an Azure subscription this build does not authenticate to
```

The two messages are not in conflict: Azure publishes a Spot Placement Score, this build does
not authenticate to a subscription, so no figure reaches the catalogue that could order it.
Note that `--min-score` on Azure hits the capability refusal, not the `--with-score` companion
rule — the cloud check runs first.

## Refusals from `--sort risk`, `--workload` and `--os`

### `unsupported capability: risk`

Two distinct triggers, and the tail of the message says which:

| Ends with                                       | Trigger                                          |
| ----------------------------------------------- | ------------------------------------------------ |
| `publishes no figure measured that way`         | `--workload web`, `ci` or `batch` on `recommend` |
| `this cloud's catalogue carries no risk figure` | `--sort risk` on `list`                          |

```console
$ spotinfo recommend --cloud gcp --architecture x86_64 --min-vcpu 2 --min-memory-gib 4 --workload web
spotinfo: gcp: unsupported capability: risk: the web workload caps interruption frequency at 5%, an AWS Spot Advisor bucket boundary, and gcp publishes no figure measured that way; workload cost applies no ceiling and answers on every cloud

$ spotinfo list --cloud gcp --sort risk
spotinfo: gcp: unsupported capability: risk: this cloud's catalogue carries no risk figure
```

Both refuse before any data is read, and both apply equally to Azure with its own name in the
message. Neither is a missing feature — see
[clouds.md](clouds.md#what-stays-refused-and-why).

**Solution:** drop the flag. `--workload cost` is the default and applies no interruption
constraint; leaving `--sort` unset leaves the order to the ranking policy.

```bash
spotinfo recommend --cloud gcp --architecture x86_64 --min-vcpu 2 --min-memory-gib 4
```

### `--sort risk` behaves differently on `list` and `recommend`

This asymmetry is deliberate and worth knowing before you report it:

```console
$ spotinfo list --cloud gcp --sort risk
spotinfo: gcp: unsupported capability: risk: this cloud's catalogue carries no risk figure

$ spotinfo recommend --cloud gcp --architecture x86_64 --min-vcpu 2 --min-memory-gib 8 --top 1 --sort risk --output text
rank=1, cloud=gcp, region=us-central1, machine=n2d-standard-2, architecture=x86_64, vcpu=2, memory_gib=8, spot_usd_per_hour=0.026912000, savings_percent=68, risk=unavailable, rationale_codes=ARCHITECTURE_MATCH COST_POLICY KNOWN_POSITIVE_PRICE RESOURCE_MINIMUMS_MET
```

`list --sort risk` asks a whole catalogue to be ordered by a column that is `unavailable` in
every row, which has no answer. `recommend --sort` re-orders a page the ranking policy has
already chosen, so an absent key leaves that order in place. The exit code difference is the
signal: if you need the refusal, ask `list`.

### `unsupported capability: os windows`

```console
$ spotinfo list --cloud gcp --os windows
spotinfo: gcp: unsupported capability: os windows: this cloud publishes spot prices for linux only
```

Google's Spot pricing pages publish no Windows Spot line. Omit `--os` or set `--os linux`.
AWS and Azure both price Windows, so `--cloud azure --os windows` answers.

A value that is not an operating system at all fails later, in the provider:

```console
$ spotinfo list --offline --os plan9
spotinfo: invalid argument: aws does not support os "plan9"
```

## `--live-risk` and the GCP credential flags

### `--live-risk needs a project`

```console
$ spotinfo recommend --cloud gcp --architecture x86_64 --min-vcpu 2 --min-memory-gib 4 --live-risk
spotinfo: invalid argument: --live-risk needs a project; pass --gcp-project or set GOOGLE_CLOUD_PROJECT
```

The call is billed to whichever project it names, so gcloud's ambient `core/project` is
deliberately not read.

### `--live-risk is refused on aws` / `on azure`

```console
$ spotinfo recommend --offline --architecture x86_64 --min-vcpu 2 --min-memory-gib 4 --live-risk
spotinfo: unsupported capability: --live-risk is refused on aws: the flag fetches gcp preemption rates and is implemented for no other cloud

$ spotinfo recommend --cloud azure --architecture x86_64 --min-vcpu 2 --min-memory-gib 4 --live-risk
spotinfo: unsupported capability: --live-risk is refused on azure: azure publishes an eviction rate through Azure Resource Graph, but reading it needs an Azure subscription this build does not authenticate to
```

AWS already publishes an interruption bucket in its snapshot, so it needs no live fetch.

### `--live-risk` returns `unavailable` for every machine

The flag fails soft — the ranked page is complete without risk — so the answer looks the same
whether Google has no preemption history for these machines or the call could not be made at
all. The run exits 0 either way. Distinguish them by the warning on stderr. Every line begins
`live risk unavailable; reporting the snapshot's risk status`; the tail below is whatever
Google returned, abbreviated:

| stderr                                                                      | Meaning                                                                                                                             |
| --------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------- |
| `live risk unavailable ... all N preemption lookups failed, first: ... 403` | Credentials resolved, but the project cannot read `compute.advice`. Enable the Compute Engine API on it, or grant access.           |
| `live risk unavailable ... all N preemption lookups failed, first: ... 404` | The project id names nothing, or the region has no advice endpoint.                                                                 |
| `live risk unavailable ... no Google Cloud credentials`                     | Application Default Credentials are not set. Run `gcloud auth application-default login`, or use a service account.                 |
| nothing on stderr                                                           | Every lookup succeeded and Google genuinely reports no history for those machines. `spotinfo --debug` shows the per-machine detail. |

A batch where _some_ lookups fail is not warned about: those machines keep `unavailable` while
the rest carry their measured rate, which the `RISK` column already shows per row.

The rate is per project. Two callers asking about the same machine in the same region get
different numbers, which is why it is never baked into the committed snapshot.

### `invalid argument: not a Google Cloud project id`

```console
$ spotinfo recommend --cloud gcp --architecture x86_64 --min-vcpu 2 --min-memory-gib 4 --live-risk --gcp-project 'BAD PROJECT!'
spotinfo: invalid argument: not a Google Cloud project id: "BAD PROJECT!" must match ^[a-z][a-z0-9-]{4,28}[a-z0-9]$
```

The value is interpolated into the advice API path, so an unchecked one would silently
redirect the call and every machine would then report `unavailable` for a reason nothing
named. Pass the project **id**, not its number, display name, or a resource path. The check
runs when a flag actually uses the project, so a bad value with neither `--live-risk` nor
`--with-score` passes unremarked.

### `--gcp-project is refused on aws` / `on azure`

```console
$ spotinfo list --offline --gcp-project foo
spotinfo: unsupported capability: --gcp-project is refused on aws: the flag names the project an authenticated gcp call is billed to, and aws makes none
```

`--gcp-billing-key` is refused the same way. Both are accepted on `--cloud gcp` including on
`list`, where nothing consumes them.

### A `--gcp-billing-key` that changes nothing

A key the API refuses is not an error. The run answers from the committed snapshot and logs
the refusal at debug level only:

```console
$ spotinfo --debug list --cloud gcp --gcp-billing-key bogus --region europe-west1 --machine '^n2-standard-4$' --output text
time=... level=DEBUG msg="gcp billing catalogue unavailable; answering from the committed snapshot" error="billing catalogue request was refused: 400 Bad Request: ... \"API key not valid. Please pass a valid API key.\" ..."
time=... level=WARN msg="no machines matched the query; relax a filter, or check the region name is one this cloud publishes" filters="[machine=^n2-standard-4$ region=europe-west1]"
```

If a key seems to be ignored, run with `--debug` before anything else: an invalid key, a
disabled Cloud Billing API and a quota refusal all look identical without it.

## `recommend` refusals

### `--architecture, --min-vcpu, --min-memory-gib are required`

```console
$ spotinfo recommend --offline
spotinfo: invalid argument: --architecture, --min-vcpu, --min-memory-gib are required; every recommendation needs an architecture and a size floor
```

Supply the ones you left out — the message names only those. There is no default size floor,
because a ranked page without one ranks the whole catalogue.

### `--output number` belongs to `spotinfo list`

```console
$ spotinfo recommend --offline --architecture x86_64 --min-vcpu 2 --min-memory-gib 4 --output number
spotinfo: invalid argument: --output number belongs to `spotinfo list`: one savings percent cannot describe a ranked page
```

`recommend` renders `text|json|table|csv`. Only `number` is `list`-only.

## Value and bound errors

Every one of these is checked before any data is read.

| Command fragment             | Message                                                                           |
| ---------------------------- | --------------------------------------------------------------------------------- |
| `--cloud oracle`             | `unknown cloud provider "oracle", want one of aws\|azure\|gcp`                                                 |
| `--architecture s390x`       | `architecture must be x86_64 or arm64`                                            |
| `--workload gpu`             | `workload must be cost, web, ci, or batch`                                        |
| `--sort colour`              | `unknown sort "colour", want one of machine\|price\|region\|risk\|savings\|score` |
| `--order sideways`           | `unknown order "sideways", want asc or desc`                                      |
| `--output yaml`              | `unknown output format "yaml", want one of number\|text\|json\|table\|csv`        |
| `--top 0`, `--top 51`        | `top must be at least 1` / `top must be between 1 and 50`                         |
| `--min-score 11`, `-1`       | `--min-score must be between 1 and 10`                                            |
| `--score-timeout 0`, `301`   | `--score-timeout must be between 1 and 300 seconds`                               |
| `--max-price 0`, `-1`, `NaN` | `--max-price must be a positive USD machine-hour price`                           |
| `--min-vcpu -1`              | `--min-vcpu must be zero or a positive number of vCPU cores`                      |
| `--min-memory-gib -1`        | `--min-memory-gib must be zero or a positive number of GiB`                       |

`NaN` is rejected explicitly. It fails every comparison, so an unchecked `NaN` would slip past
both a `<= 0` guard and the "is it set" branch and silently drop the filter.

### `error parsing regexp`

```console
$ spotinfo list --offline --machine '['
spotinfo: aws candidate acquisition: failed to match instance type: error parsing regexp: missing closing ]: `[`
```

`--machine` is RE2. Escape the metacharacters you mean literally — an AWS machine name
contains a `.`, so `^m5\.large$` is the exact match and `^m5.large$` also matches
`m5xlarge`-shaped names.

## Empty or partial results

### `list` returns nothing, `recommend` refuses

The two commands treat "no rows" differently, and both are correct:

```console
$ spotinfo list --offline --region us-east-1 --machine '^zzzz$'
┌─────────┬──────┬────────────┬────────────────────────┬──────┬──────────┐
│ MACHINE │ VCPU │ MEMORY GIB │ SAVINGS OVER ON-DEMAND │ RISK │ USD/HOUR │
├─────────┼──────┼────────────┼────────────────────────┼──────┼──────────┤
└─────────┴──────┴────────────┴────────────────────────┴──────┴──────────┘
```

with `level=WARN msg="no machines matched the query; relax a filter, or check the region name is one this cloud publishes"` on stderr and exit 0 — an empty browse
is an answer. A ranked page with nothing in it is not:

```console
$ spotinfo recommend --offline --region us-east-1 --architecture x86_64 --min-vcpu 2 --min-memory-gib 4 --machine '^zzzz$'
spotinfo: no candidates: no machine name matches "^zzzz$"; aws publishes c3.2xlarge, c3.xlarge, c5.9xlarge, c5.metal, c5a.2xlarge, c5a.xlarge, c5n.metal, c6a.metal, and more
```

The message names what the cloud does publish, so a typo is usually visible in the sample.

### A region the snapshot does not cover

Same split, with the cloud named:

```console
$ spotinfo list --cloud gcp --region europe-west1 --output text
time=... level=WARN msg="no machines matched the query; relax a filter, or check the region name is one this cloud publishes" filters="[region=europe-west1]"

$ spotinfo recommend --cloud gcp --architecture x86_64 --min-vcpu 2 --min-memory-gib 4 --region europe-west1
spotinfo: no candidates: gcp publishes no machines in europe-west1
```

The committed GCP catalogue carries `us-central1` only; `--gcp-billing-key` prices another
region for one invocation. Azure covers 55 regions, enumerated in
[data-sources.md](data-sources.md#7-azure-retail-prices-api-and-microsoft-learn-vm-size-pages)
and in `support.regions` of `internal/providers/azure/data/source-contract.json`; widening
that matrix is a source-contract change plus a data refresh, not a runtime option.

AWS is stricter — it rejects an unknown region rather than returning nothing:

```console
$ spotinfo list --offline --region atlantis
spotinfo: aws candidate acquisition: region not found: atlantis
```

`all` is a keyword, not a region, so it cannot be mixed with a real one:

```console
$ spotinfo list --offline --region all --region us-east-1
spotinfo: aws candidate acquisition: region not found: all
```

### The price column reads `-`

```console
$ spotinfo list --offline --region me-south-1 --machine '^c7g\.large$' --output text
machine=c7g.large, vCPU=2, memory=4GiB, saving=79%, risk='5-10%', price=-
```

The static AWS price feed has no entry for that machine in that region, which is expected for
very new families and for the regions AWS omits from the feed — currently all `me-*`. In JSON
the field is `null`, not zero. Drop `--offline` and give the process AWS credentials, and the
live `DescribeSpotPriceHistory` path fills it in; the row is then marked `live_price: true`
and the CSV `Price Source` column reads `live` rather than `static`.

### The risk column reads `unavailable`

That is the answer, not a failure. GCP and Azure publish no interruption figure that can ship
in a snapshot, so every candidate reports the absence rather than a zero or a low bucket. See
[clouds.md](clouds.md#why-the-risk-column-differs).

## Provider disabled

### `data unavailable: cloud provider ... is unavailable` (`DATA_UNAVAILABLE`)

The cloud is recognised but this build has no usable snapshot for it — either the provider was
not compiled in (`PROVIDER_NOT_REGISTERED`), or its embedded snapshot failed verification at
startup (`SNAPSHOT_UNAVAILABLE`). A disabled provider is never silently answered with another
cloud's prices.

**Solution:** run `spotinfo --debug ...` — every disabled provider is logged with its reason
code and the underlying detail. `SNAPSHOT_UNAVAILABLE` means the committed data, its manifest
hash, or its source contract failed a gate, which a rebuilt binary from a clean checkout will
fix.

## Performance

Offline answers are fast, measured on this machine:

| Command                                                      | Time   |
| ------------------------------------------------------------ | ------ |
| `spotinfo list --cloud gcp --machine '^n2-standard-4$'`      | 0.04 s |
| `spotinfo list --cloud azure --machine '^Standard_D2as_v5$'` | 0.08 s |
| `spotinfo list --offline --region us-east-1 --machine ...`   | 0.11 s |
| `spotinfo list --offline --machine ...` (all 34 AWS regions) | 0.14 s |

If a command takes seconds rather than tenths, one of these is happening:

- **The AWS feeds are being fetched.** The advisor document alone takes over a second. Add
  `--offline` when snapshot data is good enough, or accept the first call and let the feed
  cache serve the next 24 hours.
- **Placement scores are being fetched.** `--with-score` calls the EC2 API once per region and
  waits up to `--score-timeout` seconds, default 30. `--offline` does **not** suppress it. Name
  the regions you care about instead of letting `--region` default to all 34.
- **An Azure sweep is running.** Naming one or two Azure regions refreshes their prices from
  the Retail Prices API — 10 pages and 5.5 MB per region. `--region all` and `--offline` both
  answer from the committed catalogue instead.

`--refresh` deliberately ignores the cache and always pays full price for the fetch.

## MCP server

### The client sees no tools

```console
$ printf '%s\n' \
   '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}' \
   '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
   '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' \
   | spotinfo --mcp 2>/dev/null | jq -r 'select(.id==2) | [.result.tools[].name] | join(" ")'
list_cloud_regions list_spot_machines recommend_spot_machines
```

If that prints the three names, the server is fine and the problem is in the client
configuration. Check, in order:

1. **The binary path in the client config is absolute and executable.** `which spotinfo`, then
   `chmod +x` it if needed.
2. **The config file is valid JSON.** `jq . claude_desktop_config.json`.
3. **The client was fully restarted**, not just reloaded.
4. **The tool names in your prompt or config are the current ones.** The three pre-2.x names
   return `-32602 tool not found`.

See [claude-desktop-setup.md](claude-desktop-setup.md) for a working configuration.

### The server starts and exits

`spotinfo --mcp` speaks stdio and exits when stdin closes — that is correct behaviour, not a
crash. `SPOTINFO_MODE=mcp` starts the same server without the flag, which is useful when a
launcher cannot pass arguments. `MCP_TRANSPORT=sse` with `MCP_PORT` serves over SSE instead;
check `lsof -i :8080` for a conflict on the default port.

### A tool call returns an error instead of data

A refused question comes back as a `spotinfo.error/v1` payload with `isError: true`, carrying
the same wording the CLI prints:

```json
{
  "schema_version": "spotinfo.error/v1",
  "code": "UNSUPPORTED_CAPABILITY",
  "message": "gcp: unsupported capability: os windows: this cloud publishes spot prices for linux only",
  "cloud": "gcp"
}
```

The codes are `INVALID_ARGUMENT`, `UNSUPPORTED_CAPABILITY`, `DATA_UNAVAILABLE`,
`NO_CANDIDATES` and `INTERNAL`. Look the message up in the sections above — the CLI and MCP
refusals are the same text.

Missing required arguments read the same way:

```json
{
  "schema_version": "spotinfo.error/v1",
  "code": "INVALID_ARGUMENT",
  "message": "invalid argument: architecture must be x86_64 or arm64",
  "cloud": "aws"
}
```

`recommend_spot_machines` requires `architecture`, `min_vcpu` and `min_memory_gib`, exactly as
the CLI does.

### MCP calls are slow

The MCP surface has no global `--offline`, so an AWS tool call fetches both feeds unless the
call itself passes `"offline": true`. One `list_spot_machines` call for `m5.large` in
`us-east-1`, measured end to end with the cache disabled: **0.11 s** with `"offline": true`
against **2.3 s** without it. The advisor document is most of that, and the feed cache absorbs
it for the next 24 hours — but a client that opens a fresh process per call never benefits.

## Network and data

### AWS feeds unreachable

```bash
curl -s https://spot-bid-advisor.s3.amazonaws.com/spot-advisor-data.json | head -c 100
curl -s https://website.spot.ec2.aws.a2z.com/spot.json | head -c 100
```

A failed fetch is not a failed run: resolution order is fresh cache, then the origin, then an
_expired_ cache entry, then the committed snapshot. `data_source.mode` in the JSON output says
which one answered.

Behind a proxy, set `HTTP_PROXY` and `HTTPS_PROXY`. To skip the network entirely, pass
`--offline`.

The static price feed is an **undocumented** endpoint, not a published AWS API, and its
predecessor froze silently for two years. If prices look wrong across the board rather than
for one machine, check the feed's `Last-Modified` before suspecting the code.

### Cache control

Fetched feeds are cached under `os.UserCacheDir()/spotinfo`. `SPOTINFO_CACHE_DIR` moves the
directory and `SPOTINFO_CACHE=off` disables it. Every cache failure is non-fatal — a read-only
filesystem costs time, not answers.

## Platform notes

### macOS: "cannot be opened because it is from an unidentified developer"

```bash
xattr -d com.apple.quarantine /path/to/spotinfo
```

Or allow it once under System Settings → Privacy & Security.

### Windows: PowerShell execution policy

```powershell
Get-ExecutionPolicy
Set-ExecutionPolicy -ExecutionPolicy RemoteSigned -Scope CurrentUser
```

In a client config, use the full path with escaped separators:
`"command": "C:\\Program Files\\spotinfo\\spotinfo.exe"`.

### Linux: "no such file or directory" for a binary that exists

```bash
file $(which spotinfo)
ldd $(which spotinfo)
```

Usually an architecture mismatch — an amd64 binary on arm64 or the reverse. Download the
matching asset.

## Getting help

1. Search [GitHub Issues](https://github.com/alexei-led/spotinfo/issues).
2. Open one with `spotinfo --version`, `uname -a`, the exact command, and both its stdout and
   its stderr. The refusal text is the most useful line in the report — it names the flag and
   the cloud.
3. Reproduce with `--offline` if you can. That removes the network and the vendor from the
   picture and makes the report reproducible for someone else.
