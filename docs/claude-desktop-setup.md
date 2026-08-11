# Claude Desktop Integration Guide

This guide provides detailed instructions for integrating `spotinfo` with Claude Desktop using the Model Context Protocol (MCP). After setup, you can ask Claude questions about Spot machines on AWS, GCP and Azure.

## Prerequisites

- **Claude Desktop** installed and running
- **spotinfo** binary installed and accessible
- Basic familiarity with JSON configuration files

## Already have spotinfo configured? Read this first

The three MCP tools were renamed. Anything that names an old one now fails with
`tool '<name>' not found: tool not found`, a JSON-RPC error that carries no payload.

| Retired name               | Use instead               |
| -------------------------- | ------------------------- |
| `find_spot_instances`      | `list_spot_machines`      |
| `recommend_spot_instances` | `recommend_spot_machines` |
| `list_spot_regions`        | `list_cloud_regions`      |

**The `claude_desktop_config.json` block below does not change.** The command is still
`spotinfo` and the argument is still `--mcp`, so a plain configuration keeps working after an
upgrade. What breaks is anything that _names a tool_: a tool allow-list or `autoApprove` array,
a saved prompt or project instruction, a stored workflow. The tool arguments were renamed with
the tools — see [MCP Server](mcp-server.md#argument-renames) for the full table.

## Step 1: Install spotinfo

### Option A: macOS with Homebrew (Recommended)

```bash
brew tap alexei-led/spotinfo
brew install spotinfo
```

### Option B: Download from Releases

1. Visit the [releases page](https://github.com/alexei-led/spotinfo/releases)
2. Download the appropriate binary for your platform:
   - **macOS Intel**: `spotinfo_darwin_amd64.tar.gz`
   - **macOS Apple Silicon**: `spotinfo_darwin_arm64.tar.gz`
   - **Windows Intel/AMD**: `spotinfo_windows_amd64.zip`
   - **Windows ARM**: `spotinfo_windows_arm64.zip`
   - **Linux Intel/AMD**: `spotinfo_linux_amd64.tar.gz`
   - **Linux ARM**: `spotinfo_linux_arm64.tar.gz`

3. Extract and install:

   ```bash
   # Example for macOS/Linux
   tar -xzf spotinfo_darwin_amd64.tar.gz
   chmod +x spotinfo
   sudo mv spotinfo /usr/local/bin/
   ```

### Option C: Build from Source

```bash
git clone https://github.com/alexei-led/spotinfo.git
cd spotinfo
make build
sudo cp .bin/spotinfo /usr/local/bin/
```

## Step 2: Verify Installation

Test that spotinfo is working correctly:

```bash
# Test CLI functionality
spotinfo list --offline --region us-east-1 --machine '^t3\.micro$' --output json

# Test MCP server mode
spotinfo --mcp
# Should start and wait for input (press Ctrl+C to exit)
```

`--offline` answers from the embedded snapshot and makes no request. It belongs to the
`list` and `recommend` subcommands, not to the root command: `spotinfo --offline --mcp` exits
1 with `flag provided but not defined: -offline`. Over MCP, pass `offline: true` as a tool
argument instead.

## Step 3: Configure Claude Desktop

### Locate Configuration File

The configuration file location depends on your operating system:

- **macOS**: `~/Library/Application Support/Claude/claude_desktop_config.json`
- **Windows**: `%APPDATA%\Claude\claude_desktop_config.json`
- **Linux**: `~/.config/Claude/claude_desktop_config.json`

### Find Binary Path

Determine the full path to your spotinfo binary:

```bash
# Find the path
which spotinfo
# Output example: /opt/homebrew/bin/spotinfo (macOS Homebrew)
# Output example: /usr/local/bin/spotinfo (manual install)
```

### Create/Edit Configuration

Create or edit the Claude Desktop configuration file:

#### Basic Configuration

```json
{
  "mcpServers": {
    "spotinfo": {
      "command": "/opt/homebrew/bin/spotinfo",
      "args": ["--mcp"]
    }
  }
}
```

#### Advanced Configuration with Environment Variables

```json
{
  "mcpServers": {
    "spotinfo": {
      "command": "/opt/homebrew/bin/spotinfo",
      "args": ["--mcp"],
      "env": {
        "AWS_REGION": "us-east-1",
        "SPOTINFO_MODE": "mcp"
      }
    }
  }
}
```

### Platform-Specific Examples

#### macOS (Homebrew Installation)

```json
{
  "mcpServers": {
    "spotinfo": {
      "command": "/opt/homebrew/bin/spotinfo",
      "args": ["--mcp"]
    }
  }
}
```

#### macOS (Manual Installation)

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

#### Windows

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

#### Linux

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

## Step 4: Restart Claude Desktop

After editing the configuration file:

1. **Quit Claude Desktop completely**
2. **Wait a few seconds**
3. **Launch Claude Desktop again**

## Step 5: Test Integration

Start a conversation with Claude and try these test queries:

### Basic Test

```text
Human: Can you list the AWS regions that publish Spot machines?
```

Expected: Claude uses `list_cloud_regions` with `{"cloud": "aws"}` and returns 34 region
codes, plus the documents the catalogue was read from.

### Advanced Test

```text
Human: Show me the m5.large spot price in us-east-1 and us-east-2
```

Expected: Claude uses `list_spot_machines` with `{"cloud": "aws", "regions":
["us-east-1", "us-east-2"], "machine": "^m5\\.large$"}` and returns one row per region with
its price, savings and interruption bucket.

### Cross-Cloud Test

```text
Human: What are the three cheapest arm64 Azure Spot VMs with 4 vCPUs and 16 GiB?
```

Expected: Claude uses `recommend_spot_machines` with `{"cloud": "azure", "architecture":
"arm64", "min_vcpu": 4, "min_memory_gib": 16, "top": 3}`. Azure publishes no redistributable
eviction rate, so every candidate reports `risk.status: "unavailable"` — that is the honest
answer, not a failure.

## Troubleshooting

### Common Issues and Solutions

#### 1. "tool not found" after an upgrade

**Symptoms:**

- A call fails with `tool 'find_spot_instances' not found: tool not found`
- The same for `recommend_spot_instances` or `list_spot_regions`

**Cause:** those three names were retired. The error is a JSON-RPC error (`code: -32602`), so
there is no payload to read.

**Solutions:**

- Replace the name: `find_spot_instances` → `list_spot_machines`,
  `recommend_spot_instances` → `recommend_spot_machines`, `list_spot_regions` →
  `list_cloud_regions`
- Check every place a tool is named by hand — allow-lists, `autoApprove` arrays, saved
  prompts, project instructions
- The `mcpServers` block itself needs no change

Confirm what the server actually publishes:

```bash
printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"probe","version":"1"}}}' \
  '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' | spotinfo --mcp
# The tools/list result names list_cloud_regions, list_spot_machines and
# recommend_spot_machines — nothing else.
```

#### 2. Claude doesn't see the spotinfo tools

**Symptoms:**

- Claude responds with "I don't have access to Spot instance tools"
- No MCP tools appear in Claude's responses

**Solutions:**

```bash
# Test the MCP server directly
spotinfo --mcp
# Should start without errors

# Check binary permissions
ls -la $(which spotinfo)
# Should show execute permissions

# Verify configuration syntax
cat ~/Library/Application\ Support/Claude/claude_desktop_config.json | jq .
# Should parse without errors
```

#### 3. Permission denied errors

**Symptoms:**

- Claude shows connection errors
- Console shows permission denied messages

**Solutions:**

```bash
# Make binary executable
chmod +x /path/to/spotinfo

# For macOS, you might need to allow the binary
spctl --add /path/to/spotinfo
```

#### 4. Binary not found

**Symptoms:**

- "Command not found" or similar errors

**Solutions:**

```bash
# Verify the binary path exists
ls -la /opt/homebrew/bin/spotinfo

# If not found, reinstall or check installation
which spotinfo

# Update configuration with correct path
```

#### 5. Configuration file issues

**Symptoms:**

- Claude Desktop fails to start
- MCP servers don't load

**Solutions:**

```bash
# Validate JSON syntax
cat claude_desktop_config.json | jq .

# Check for common issues:
# - Missing commas
# - Incorrect quotes
# - Wrong file paths
# - Invalid escape characters in Windows paths
```

#### 6. macOS Security Warnings

**Symptoms:**

- "spotinfo cannot be opened because it is from an unidentified developer"

**Solutions:**

```bash
# Remove quarantine attribute
xattr -d com.apple.quarantine /path/to/spotinfo

# Or allow in Security & Privacy settings:
# System Preferences → Security & Privacy → General → Allow apps downloaded from: Anywhere
```

## Advanced Configuration

### Multiple MCP Servers

You can configure multiple MCP servers alongside spotinfo:

```json
{
  "mcpServers": {
    "spotinfo": {
      "command": "/opt/homebrew/bin/spotinfo",
      "args": ["--mcp"]
    },
    "other-tool": {
      "command": "/path/to/other-tool",
      "args": ["--mcp"]
    }
  }
}
```

### Environment Variables

Configure a default AWS region or other settings:

```json
{
  "mcpServers": {
    "spotinfo": {
      "command": "/opt/homebrew/bin/spotinfo",
      "args": ["--mcp"],
      "env": {
        "AWS_REGION": "us-west-2",
        "SPOTINFO_MODE": "mcp"
      }
    }
  }
}
```

The MCP surface takes no credential as a tool argument, so the `env` block is the only place
one can be given. `SPOTINFO_GCP_BILLING_KEY` is the one that changes an answer: it holds a
Cloud Billing Catalog API key and lets GCP be priced beyond the committed `us-central1`
snapshot. Without it, GCP tool calls answer from that snapshot. See
[MCP Server](mcp-server.md#environment-variables) for the whole table.

## Usage Examples

Once configured, you can ask Claude various questions about Spot machines on AWS, GCP and
Azure:

### Cost Optimization

```text
Human: What are the most cost-effective ways to run a web server on AWS using spot machines?

Claude: I'll rank AWS Spot machines under the `web` workload, which caps interruption at 5%...
```

### Regional Comparison

```text
Human: Compare t3.medium spot prices in us-east-1, us-east-2 and us-west-2

Claude: I'll list that machine in those three regions, cheapest first...
```

### Requirements-Based Search

```text
Human: I need a machine with at least 8 vCPUs and 32 GiB for data processing, but I want to keep costs under $0.50/hour. What are my options?

Claude: I'll rank candidates that meet those minimums within your budget...
```

### Cross-Cloud Comparison

```text
Human: Is GCP or Azure cheaper for a 4 vCPU, 16 GiB arm64 machine?

Claude: I'll rank each cloud separately — neither publishes an interruption figure, so both
answer on price alone and report their risk as unavailable...
```

## Verification Steps

### 1. Check MCP Server Status

```bash
# Manual test
spotinfo --mcp
# Logs go to stderr, so they never corrupt the JSON-RPC stream on stdout:
# time=... level=INFO msg="MCP tools registered" count=3 providers="[aws azure gcp]"
```

### 2. Test with MCP Inspector

```bash
# Install MCP Inspector
npm install -g @modelcontextprotocol/inspector

# Test integration
npx @modelcontextprotocol/inspector spotinfo --mcp
```

### 3. Claude Desktop Logs

Check Claude Desktop logs for any error messages:

- **macOS**: `~/Library/Logs/Claude/`
- **Windows**: `%LOCALAPPDATA%\Claude\logs\`

## Getting Help

If you encounter issues not covered in this guide:

1. **Check the [troubleshooting document](troubleshooting.md)**
2. **Review the [API reference](api-reference.md)**
3. **Test with CLI first**: `spotinfo list --offline --region us-east-1 --machine '^t3\.micro$'`
4. **File an issue**: [GitHub Issues](https://github.com/alexei-led/spotinfo/issues)

## Next Steps

- Explore the [API reference](api-reference.md) for detailed tool documentation
- Review [troubleshooting guide](troubleshooting.md) for common issues
- Learn about advanced usage patterns in the main README
- Consider contributing improvements or reporting bugs

The integration transforms Claude into an intelligent Spot advisor across AWS, GCP and Azure, making infrastructure decisions more informed and efficient.
