# Model Context Protocol (MCP) Server

## Overview

The `spotinfo` tool functions as a **Model Context Protocol (MCP) server**, so an AI assistant can query Spot data directly. Three tools are served, and every one of them takes a `cloud` argument — `aws`, `gcp` or `azure`. None of them is AWS-only.

| Tool                      | Answers                                                                     | Payload                 |
| ------------------------- | --------------------------------------------------------------------------- | ----------------------- |
| `list_spot_machines`      | every machine matching a filter, with its price and its risk status         | `spotinfo.list/v1`      |
| `recommend_spot_machines` | a ranked page for a stated requirement                                      | `spotinfo.recommend/v3` |
| `list_cloud_regions`      | every region a cloud publishes, and every document that cloud was read from | `spotinfo.regions/v1`   |

A failing call returns a `spotinfo.error/v1` body with `isError: true`, never a bare string.

## The tools were renamed

The three tools this server used to publish are **gone**. A configuration, prompt or workflow that names one of them fails at call time.

| Retired name               | Use instead               |
| -------------------------- | ------------------------- |
| `find_spot_instances`      | `list_spot_machines`      |
| `recommend_spot_instances` | `recommend_spot_machines` |
| `list_spot_regions`        | `list_cloud_regions`      |

Calling a retired name is a JSON-RPC error, not a payload — there is no `content` block to read:

```json
{
  "code": -32602,
  "message": "tool 'find_spot_instances' not found: tool not found"
}
```

**The `mcpServers` entry itself does not change.** The command is still `spotinfo` and the argument is still `--mcp`, so nothing in the [Quick Start](#quick-start-with-claude-desktop) block below needs editing. What breaks is anything that _names a tool_: a client allow-list or `autoApprove` array, a saved agent prompt, a stored workflow, a test fixture. Update those.

### Argument renames

The arguments were renamed with the tools. Every one is derived mechanically from a CLI flag — strip the `--`, replace `-` with `_` — so a name cannot drift between the two surfaces again.

| Retired argument        | Use instead      | Note                                                                                                                                                                                                          |
| ----------------------- | ---------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `instance_types`        | `machine`        | one RE2 string, not an array                                                                                                                                                                                  |
| `min_memory_gb`         | `min_memory_gib` | the unit label was wrong; the figure was always GiB                                                                                                                                                           |
| `max_price_per_hour`    | `max_price`      | must be `> 0`; there is no "0 means no ceiling" any more                                                                                                                                                      |
| `sort_by`               | `sort`           | values are `machine\|price\|region\|risk\|savings\|score`; `reliability` is gone, `risk` is the nearest                                                                                                       |
| `limit`                 | `top`            | `top` exists on `recommend_spot_machines` only — see below                                                                                                                                                    |
| `max_interruption_rate` | —                | **removed with no equivalent.** The nearest answer is `recommend_spot_machines` with `workload: web`, `ci` or `batch`, which caps interruption at an AWS Spot Advisor bucket boundary and answers on AWS only |

`list_spot_machines` has **no result cap**. A request for "the 5 cheapest" belongs to `recommend_spot_machines` with `top: 5`.

A retired argument is rejected by name rather than ignored, so a stale call fails loudly instead of silently dropping a filter:

```json
{
  "schema_version": "spotinfo.error/v1",
  "code": "INVALID_ARGUMENT",
  "message": "invalid argument: unknown argument: limit",
  "cloud": "aws"
}
```

## What is MCP?

The [Model Context Protocol (MCP)](https://modelcontextprotocol.io/) is an open standard that allows AI assistants to securely connect to external data sources and tools. By running `spotinfo` in MCP mode, you can:

- **Ask Claude for Spot recommendations**: "Rank the three cheapest arm64 Azure Spot VMs with 4 vCPUs and 16 GiB"
- **Get pricing**: "What's the m5.large spot price in us-east-1?"
- **Compare across regions**: "Show me m5.large prices in us-east-1 and us-east-2"
- **Compare across clouds**: the same question, asked once per `cloud`, returns the same document shape
- **Infrastructure planning**: use AI to analyse and recommend Spot configurations

## Quick Start with Claude Desktop

### 1. Install spotinfo

```bash
# macOS with Homebrew
brew tap alexei-led/spotinfo
brew install spotinfo

# Or download from releases page
curl -L https://github.com/alexei-led/spotinfo/releases/latest/download/spotinfo_linux_amd64.tar.gz | tar xz
```

### 2. Add to Claude Desktop Configuration

Open Claude Desktop settings and add to your `claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "spotinfo": {
      "command": "spotinfo",
      "args": ["--mcp"]
    }
  }
}
```

### 3. Restart Claude Desktop

Restart Claude Desktop and start asking about Spot machines.

## Available MCP Tools

All three tools declare `readOnlyHint: true`, `idempotentHint: true`, `destructiveHint: false` and `openWorldHint: true`, and all three reject an argument they do not declare (`additionalProperties: false`).

### `list_spot_machines`

List every Spot machine matching a filter, with its price and its risk status. Returns a `spotinfo.list/v1` payload; a cloud that publishes no interruption figure reports that absence rather than a zero.

**Required:** none.

| Argument         | Type                                           | Default   | Notes                                                           |
| ---------------- | ---------------------------------------------- | --------- | --------------------------------------------------------------- |
| `cloud`          | `aws\|gcp\|azure`                              | `aws`     |                                                                 |
| `regions`        | array of string                                | `["all"]` | unique, at least one entry; `["all"]` is every published region |
| `machine`        | string                                         | `""`      | RE2 regexp                                                      |
| `architecture`   | `x86_64\|arm64`                                | —         | omit for no architecture filter                                 |
| `os`             | `linux\|windows`                               | `linux`   |                                                                 |
| `min_vcpu`       | integer ≥ 0                                    | —         | omit for no floor                                               |
| `min_memory_gib` | number ≥ 0                                     | —         | omit for no floor                                               |
| `max_price`      | number > 0                                     | —         | omit for no ceiling                                             |
| `sort`           | `machine\|price\|region\|risk\|savings\|score` | —         | omit to keep the order the cloud publishes                      |
| `order`          | `asc\|desc`                                    | `asc`     |                                                                 |
| `offline`        | boolean                                        | `false`   | answer from the committed snapshots and make no request at all  |
| `refresh`        | boolean                                        | `false`   | ignore any locally cached provider document for this call       |
| `with_score`     | boolean                                        | `false`   | placement figures; see the constraints below                    |
| `min_score`      | integer 0-10                                   | —         | needs `with_score`                                              |
| `az`             | boolean                                        | `false`   | zone-level figures; needs `with_score`                          |
| `score_timeout`  | integer 1-300                                  | `30`      | seconds                                                         |

**Response:** `schema_version`, `status`, `request`, `data_source`, `candidates`, `warnings`. Each candidate carries `cloud`, `region`, `machine`, `architecture`, `os`, `vcpu`, `memory_gib`, `spot_usd_per_hour`, `on_demand_usd_per_hour`, `savings_percent`, `risk` and `live_price`.

### `recommend_spot_machines`

Recommend Spot machines from committed multi-cloud price data. Returns a `spotinfo.recommend/v3` payload with explicit risk availability per cloud.

**Required:** `architecture`, `min_vcpu`, `min_memory_gib`.

| Argument         | Type                   | Default   | Notes                                                 |
| ---------------- | ---------------------- | --------- | ----------------------------------------------------- |
| `architecture`   | `x86_64\|arm64`        | —         | **required**                                          |
| `min_vcpu`       | integer ≥ 1            | —         | **required**                                          |
| `min_memory_gib` | number > 0             | —         | **required**                                          |
| `cloud`          | `aws\|gcp\|azure`      | `aws`     |                                                       |
| `regions`        | array of string        | `["all"]` |                                                       |
| `machine`        | string                 | `""`      | RE2 regexp, combined with `architecture`              |
| `os`             | `linux\|windows`       | `linux`   |                                                       |
| `max_price`      | number > 0             | —         | omit for no ceiling                                   |
| `workload`       | `cost\|web\|ci\|batch` | `cost`    | `cost` ranks by price and makes no interruption claim |
| `top`            | integer 1-50           | `3`       |                                                       |
| `offline`        | boolean                | `false`   |                                                       |
| `refresh`        | boolean                | `false`   |                                                       |

**Response:** `schema_version`, `status`, `request`, `ranking_policy`, `data_source`, `recommendations`, `warnings`. See [API Reference](api-reference.md) for the full schema.

`recommend_spot_machines` declares **no** `sort`, `order`, `with_score`, `min_score`, `az` or `score_timeout` argument. A ranked page's placement figures are reachable from the CLI only.

### `list_cloud_regions`

List every region a cloud publishes Spot machines in, with the complete list of documents that cloud was read from. This is where the `sources_omitted` count of a `list` or `recommend` answer is resolved.

**Required:** none.

| Argument | Type              | Default | Notes |
| -------- | ----------------- | ------- | ----- |
| `cloud`  | `aws\|gcp\|azure` | `aws`   |       |

**Response:** `schema_version`, `status`, `cloud`, `regions`, `data_source`. Measured on the committed snapshots: `aws` returns 34 regions and 3 sources, `azure` 55 regions and 81 sources, `gcp` 1 region (`us-central1`) and 5 sources.

## Cloud constraints

A request a cloud cannot answer is refused **before** any price is read, with `code: UNSUPPORTED_CAPABILITY`.

|                                      | `aws`                                                       | `gcp`             | `azure`       |
| ------------------------------------ | ----------------------------------------------------------- | ----------------- | ------------- |
| `os: windows`                        | yes                                                         | **refused**       | yes           |
| `workload: web\|ci\|batch`           | yes                                                         | **refused**       | **refused**   |
| risk status on a candidate           | `available`                                                 | `unavailable`     | `unavailable` |
| `with_score` on `list_spot_machines` | yes, needs AWS credentials and `ec2:GetSpotPlacementScores` | **refused**       | **refused**   |
| `sort: risk` on `list_spot_machines` | yes                                                         | **refused**       | **refused**   |
| regions in the committed snapshot    | 34                                                          | 1 (`us-central1`) | 55            |

Neither GCP nor Azure publishes a redistributable interruption figure, so both report `risk.status: "unavailable"` — never a zero and never an AWS-shaped bucket — and both serve the risk-free `cost` workload only. The three capped workloads use AWS Spot Advisor bucket boundaries, which no other vendor measures the same way.

Measured refusals, verbatim:

```json
{"schema_version": "spotinfo.error/v1", "code": "UNSUPPORTED_CAPABILITY", "message": "gcp: unsupported capability: os windows: this cloud publishes spot prices for linux only", "cloud": "gcp"}
{"schema_version": "spotinfo.error/v1", "code": "UNSUPPORTED_CAPABILITY", "message": "gcp: unsupported capability: risk: the web workload caps interruption frequency at 5%, an AWS Spot Advisor bucket boundary, and gcp publishes no figure measured that way; workload cost applies no ceiling and answers on every cloud", "cloud": "gcp"}
{"schema_version": "spotinfo.error/v1", "code": "UNSUPPORTED_CAPABILITY", "message": "azure: unsupported capability: placement_score: this cloud publishes no placement figure", "cloud": "azure"}
{"schema_version": "spotinfo.error/v1", "code": "UNSUPPORTED_CAPABILITY", "message": "unsupported capability: gcp obtainability is fetched for a ranked recommendation only, and needs a Google Cloud project", "cloud": "gcp"}
{"schema_version": "spotinfo.error/v1", "code": "UNSUPPORTED_CAPABILITY", "message": "azure: unsupported capability: risk: this cloud's catalogue carries no risk figure", "cloud": "azure"}
```

`min_score` and `az` are refused whenever `with_score` is absent, on every cloud:

```json
{"schema_version": "spotinfo.error/v1", "code": "INVALID_ARGUMENT", "message": "invalid argument: min_score needs with_score, which is what fetches the placement scores it filters on", "cloud": "gcp"}
{"schema_version": "spotinfo.error/v1", "code": "INVALID_ARGUMENT", "message": "invalid argument: az needs with_score, which is what fetches the placement scores it splits by zone", "cloud": "aws"}
```

### A region the cloud does not publish

The two tools answer this differently, and the answer differs on AWS again. All six cells were measured:

|         | `list_spot_machines`                                                       | `recommend_spot_machines`                                           |
| ------- | -------------------------------------------------------------------------- | ------------------------------------------------------------------- |
| `aws`   | error, `INTERNAL`: `aws candidate acquisition: region not found: atlantis` | error, `INTERNAL`, same message                                     |
| `gcp`   | a normal payload with zero candidates and no warnings                      | error, `NO_CANDIDATES`: `gcp publishes no machines in europe-west1` |
| `azure` | a normal payload with zero candidates and no warnings                      | error, `NO_CANDIDATES`: `azure publishes no machines in atlantis`   |

## Configuration Options

### Environment Variables

| Variable                   | Description                                                               | Default  |
| -------------------------- | ------------------------------------------------------------------------- | -------- |
| `SPOTINFO_MODE`            | Set to `mcp` to enable MCP server mode                                    | CLI mode |
| `MCP_TRANSPORT`            | Transport method — `stdio` or `sse`                                       | `stdio`  |
| `MCP_PORT`                 | Port for SSE transport                                                    | `8080`   |
| `SPOTINFO_GCP_BILLING_KEY` | Cloud Billing Catalog API key that prices GCP regions beyond the snapshot | unset    |

The MCP surface takes **no credential as a tool argument**. `SPOTINFO_GCP_BILLING_KEY` in the server's `env` block is the only way to reach GCP's live price catalogue from a tool call; without it, GCP answers from the committed snapshot.

### Command Line Flags

```bash
# Start MCP server with stdio transport (for Claude Desktop)
spotinfo --mcp

# Or using environment variable
SPOTINFO_MODE=mcp spotinfo
```

`--mcp`, `--debug`, `--quiet` and `--json-log` are the only flags the root command takes. Every query flag — including `--offline` — belongs to the `list` and `recommend` subcommands, so `spotinfo --offline --mcp` exits 1 with `flag provided but not defined: -offline`. Over MCP, pass `offline: true` in the tool arguments instead; it is per call.

## Example Usage

Once configured with Claude Desktop, you can ask natural language questions. Every result below was captured from the running server against the committed snapshots; live prices move, so your figures will differ.

### Example 1: The cheapest machines that meet a requirement

**Human**: Find me three x86_64 machines with at least 16 vCPUs and 64 GiB under $1/hour that rarely get interrupted.

**Claude**: I'll rank AWS Spot machines under the `web` workload, which caps interruption at 5%.

```json
{
  "cloud": "aws",
  "architecture": "x86_64",
  "min_vcpu": 16,
  "min_memory_gib": 64,
  "max_price": 1.0,
  "workload": "web",
  "top": 3
}
```

**Results**: three candidates, each in the `<5%` interruption bucket:

- `m6i.4xlarge` in ap-east-2: $0.095000/hour, 66% savings
- `m5.4xlarge` in eu-north-1: $0.095400/hour, 69% savings
- `r6i.4xlarge` in ca-west-1: $0.110400/hour, 85% savings

Each carries its rationale: `ARCHITECTURE_MATCH`, `BUDGET_CAP_MET`, `KNOWN_POSITIVE_PRICE`, `RESOURCE_MINIMUMS_MET`, `WORKLOAD_WEB_CAP_MET`.

### Example 2: Regional comparison

**Human**: Compare m5.large spot prices in us-east-1 and us-east-2.

**Claude**: I'll list that machine in both regions, cheapest first.

```json
{
  "cloud": "aws",
  "regions": ["us-east-1", "us-east-2"],
  "machine": "^m5\\.large$",
  "sort": "price"
}
```

**Results**:

- us-east-2: $0.033900/hour (70% savings, `>20%` interruption)
- us-east-1: $0.039900/hour (59% savings, `>20%` interruption)

### Example 3: GCP Spot recommendations

**Human**: Find me the 3 cheapest GCP Spot VMs with at least 2 vCPUs and 8 GiB of memory for x86_64.

**Claude**: I'll query the GCP catalogue for Spot VMs matching your requirements.

```json
{
  "cloud": "gcp",
  "architecture": "x86_64",
  "min_vcpu": 2,
  "min_memory_gib": 8,
  "top": 3
}
```

**Results**: Found 3 GCP Spot VMs in us-central1, ranked by price:

- n2d-standard-2: $0.026912/hour (68% savings, risk unavailable)
- t2d-standard-2: $0.034676/hour (58% savings, risk unavailable)
- c3d-highcpu-4: $0.035088/hour (76% savings, risk unavailable)

> GCP preemption history is not available in the public pricing feed. All candidates report `risk.status: "unavailable"`.

### Example 4: Azure Arm Spot recommendations

**Human**: What are the cheapest arm64 Azure Spot VMs with 4 vCPUs and 16 GiB across your regions?

**Claude**: I'll rank the committed Azure catalogue by price.

```json
{
  "cloud": "azure",
  "architecture": "arm64",
  "min_vcpu": 4,
  "min_memory_gib": 16,
  "top": 3
}
```

**Results**: three arm64 Spot VMs, each naming its own region:

- `Standard_D4ps_v6` in centralindia: $0.017076/hour (81% savings)
- `Standard_D4ps_v5` in centralindia: $0.018665/hour (81% savings)
- `Standard_D4pds_v5` in centralindia: $0.022361/hour (81% savings)

> Azure eviction rates require a subscription, so all candidates report `risk.status: "unavailable"`
> and only the `cost` workload applies.

### Example 5: Resolving provenance

**Human**: Which documents did that Azure answer come from? It said 77 sources were omitted.

**Claude**: `list_cloud_regions` publishes the complete list a trimmed answer was drawn from.

```json
{ "cloud": "azure" }
```

**Results**: 55 regions and all 81 source documents — the Retail Prices query per region and the Microsoft Learn size page per series — each with its `fetched_at`, `content_sha256` and `parser_version`, so any published price can be checked against the document it came from.

## Advanced Configuration

### Claude Desktop Configuration (macOS)

Configuration file location: `~/Library/Application Support/Claude/claude_desktop_config.json`

```json
{
  "mcpServers": {
    "spotinfo": {
      "command": "/opt/homebrew/bin/spotinfo",
      "args": ["--mcp"],
      "env": {
        "AWS_REGION": "us-east-1"
      }
    }
  }
}
```

### Claude Desktop Configuration (Windows)

Configuration file location: `%APPDATA%\Claude\claude_desktop_config.json`

```json
{
  "mcpServers": {
    "spotinfo": {
      "command": "C:\\Program Files\\spotinfo\\spotinfo.exe",
      "args": ["--mcp"]
    }
  }
}
```

### Claude Desktop Configuration (Linux)

Configuration file location: `~/.config/claude-desktop/claude_desktop_config.json`

```json
{
  "mcpServers": {
    "spotinfo": {
      "command": "/usr/local/bin/spotinfo",
      "args": ["--mcp"]
    }
  }
}
```

## Troubleshooting

### Common Issues

**Claude reports a tool was not found:**

- The message is `tool '<name>' not found: tool not found`. Compare the name against the three this server publishes — see [the rename table](#the-tools-were-renamed).
- Confirm what is actually served: `tools/list` must return exactly `list_cloud_regions`, `list_spot_machines` and `recommend_spot_machines`.

**Claude can't find spotinfo tools at all:**

- Verify `spotinfo --mcp` runs without errors
- Check the binary path in your configuration
- Restart Claude Desktop after configuration changes

**Permission denied errors:**

- Ensure the spotinfo binary is executable: `chmod +x /path/to/spotinfo`
- Check file paths in configuration are correct

**No data returned:**

- Every cloud ships a committed snapshot, so an unreachable feed degrades the answer rather than failing it. `offline: true` pins that behaviour and makes no request at all
- A `list_spot_machines` call that matches nothing is not an error: it returns a normal payload with an empty `candidates` array
- Check whether a machine exists in a region with the CLI:

  ```bash
  spotinfo list --offline --region us-east-1 --machine '^m5\.large$'
  ```

### Debug Mode

```bash
# Test MCP server manually. Logs go to stderr, so they never corrupt the
# JSON-RPC stream on stdout:
spotinfo --mcp
# time=... level=INFO msg="MCP tools registered" count=3 providers="[aws azure gcp]"

# --debug adds DEBUG lines, --quiet leaves only errors, --json-log makes them JSON
spotinfo --mcp --debug

# Test with MCP Inspector (requires Node.js)
npx @modelcontextprotocol/inspector spotinfo --mcp
```

### Verification Steps

1. **Test CLI mode first**:

   ```bash
   spotinfo list --offline --region us-east-1 --machine '^t3\.micro$'
   ```

2. **Test MCP mode**:

   ```bash
   spotinfo --mcp
   # Should start and wait for JSON-RPC input
   ```

3. **Verify Claude Desktop config**:
   - Check file exists and is valid JSON
   - Verify binary path is correct
   - Restart Claude Desktop

4. **Check logs**:
   - Enable debug mode: `spotinfo --mcp --debug`
   - Check Claude Desktop logs for MCP connection issues

## Server Capabilities

### Protocol Details

- **Protocol Version**: `2024-11-05`
- **Server Name**: `spotinfo`
- **Transport**: `stdio` (Claude Desktop compatible), or `sse` via `MCP_TRANSPORT`
- **Capabilities**: `tools`

### Response Format

A successful call returns the payload as text content — a `spotinfo.list/v1`, `spotinfo.recommend/v3` or `spotinfo.regions/v1` document:

```json
{
  "jsonrpc": "2.0",
  "id": "request-id",
  "result": {
    "content": [
      {
        "type": "text",
        "text": "{\"schema_version\":\"spotinfo.list/v1\",\"status\":\"ok\",\"request\":{...},\"data_source\":{...},\"candidates\":[...],\"warnings\":[]}"
      }
    ]
  }
}
```

A failing call sets `isError: true` and carries a `spotinfo.error/v1` body in the same place:

```json
{
  "schema_version": "spotinfo.error/v1",
  "code": "INVALID_ARGUMENT",
  "message": "invalid argument: architecture must be x86_64 or arm64",
  "cloud": "aws"
}
```

`code` is one of `INVALID_ARGUMENT`, `UNSUPPORTED_CAPABILITY`, `DATA_UNAVAILABLE`, `NO_CANDIDATES` or `INTERNAL`. A retired tool name never reaches this shape — it fails as a JSON-RPC error instead.

## Benefits of MCP Integration

1. **Natural Language Interface**: Ask questions about Spot machines in plain English
2. **Intelligent Recommendations**: Claude can analyse your requirements and suggest configurations
3. **One vocabulary across three clouds**: the same argument names and the same document shape on AWS, GCP and Azure
4. **Honest absence**: a cloud that publishes no interruption figure says so, so a recommendation is never ranked against another cloud's silence
5. **Verifiable provenance**: every answer names the documents it was read from, with their hashes

## API Reference

For complete MCP tool specifications, see [API Reference](api-reference.md).

## See Also

- [Claude Desktop Setup](claude-desktop-setup.md) - Detailed setup instructions
- [Usage Guide](usage.md) - CLI command reference
- [Troubleshooting](troubleshooting.md) - Common issues and solutions
