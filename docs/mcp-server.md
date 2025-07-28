# Model Context Protocol (MCP) Server

## Overview

The `spotinfo` tool functions as a **Model Context Protocol (MCP) server**, enabling AI assistants like Claude to directly query AWS EC2 Spot Instance data. This provides a seamless way for AI agents to access real-time spot pricing and interruption data for infrastructure recommendations.

## What is MCP?

The [Model Context Protocol (MCP)](https://modelcontextprotocol.io/) is an open standard that allows AI assistants to securely connect to external data sources and tools. By running `spotinfo` in MCP mode, you can:

- **Ask Claude for spot recommendations**: "Find me the cheapest t3 instances with <10% interruption rate"
- **Get real-time pricing**: "What's the current m5.large spot price in us-east-1?"  
- **Compare across regions**: "Show me r5.xlarge prices across all US regions"
- **Infrastructure planning**: Use AI to analyze and recommend optimal spot instance configurations

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

Restart Claude Desktop and start asking about AWS Spot Instances!

## Available MCP Tools

### `find_spot_instances`

Search for AWS EC2 Spot Instance options based on requirements.

**Parameters:**
- `regions` (optional): AWS regions to search (e.g., `["us-east-1", "eu-west-1"]`). Use `["all"]` for all regions
- `instance_types` (optional): Instance type pattern (e.g., `"m5.large"`, `"t3.*"`)  
- `min_vcpu` (optional): Minimum vCPUs required
- `min_memory_gb` (optional): Minimum memory in gigabytes
- `max_price_per_hour` (optional): Maximum spot price per hour in USD
- `max_interruption_rate` (optional): Maximum interruption rate percentage (0-100)
- `sort_by` (optional): Sort by `"price"`, `"reliability"`, or `"savings"` (default: `"reliability"`)
- `limit` (optional): Maximum results to return (default: 10, max: 50)

**Response:** Array of spot instances with pricing, savings, interruption data, and specs.

### `list_spot_regions`

List all AWS regions where EC2 Spot Instances are available.

**Parameters:**
- `include_names` (optional): Include human-readable region names (default: true)

**Response:** Array of available region codes and total count.

## Configuration Options

### Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `SPOTINFO_MODE` | Set to "mcp" to enable MCP server mode | CLI mode |
| `MCP_TRANSPORT` | Transport method | "stdio" |
| `MCP_PORT` | Port for SSE transport | "8080" |

### Command Line Flags

```bash
# Start MCP server with stdio transport (for Claude Desktop)
spotinfo --mcp

# Or using environment variable
SPOTINFO_MODE=mcp spotinfo
```

## Example Usage

Once configured with Claude Desktop, you can ask natural language questions:

### Example 1: Finding Cost-Effective Instances

**Human**: Find me the 5 cheapest t3 instances globally with less than 10% interruption rate

**Claude**: I'll search for t3 instances with low interruption rates and sort by price.

```json
{
  "instance_types": "t3.*",
  "max_interruption_rate": 10,
  "sort_by": "price", 
  "limit": 5
}
```

**Results**: Found 5 t3 instances under $0.05/hour with <10% interruption rates:
- t3.nano in ap-south-1: $0.0017/hour (5-10% interruption)
- t3.micro in ap-south-1: $0.0033/hour (<5% interruption)
- ...

### Example 2: Regional Comparison

**Human**: Compare m5.large spot prices across US East regions

**Claude**: I'll check m5.large pricing in US East regions for you.

```json
{
  "regions": ["us-east-1", "us-east-2"],
  "instance_types": "m5.large"
}
```

**Results**: m5.large spot prices in US East:
- us-east-1: $0.0928/hour (70% savings, <5% interruption)
- us-east-2: $0.1024/hour (68% savings, <5% interruption)

### Example 3: Infrastructure Planning

**Human**: I need instances with at least 16 vCPUs and 64GB RAM for machine learning workloads. What are my most reliable options under $1/hour?

**Claude**: I'll find high-spec instances optimized for reliability within your budget.

```json
{
  "min_vcpu": 16,
  "min_memory_gb": 64,
  "max_price_per_hour": 1.0,
  "sort_by": "reliability",
  "limit": 10
}
```

**Results**: Found 8 instances meeting your criteria, with r5.4xlarge and m5.4xlarge offering the best reliability...

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

**Claude can't find spotinfo tools:**
- Verify `spotinfo --mcp` runs without errors
- Check the binary path in your configuration
- Restart Claude Desktop after configuration changes

**Permission denied errors:**
- Ensure the spotinfo binary is executable: `chmod +x /path/to/spotinfo`
- Check file paths in configuration are correct

**No data returned:**
- The tool uses embedded data and works offline
- Check if specific regions/instance types exist with CLI: `spotinfo --type=m5.large --region=us-east-1`

### Debug Mode

```bash
# Test MCP server manually
spotinfo --mcp
# Should start server and wait for input

# Test with MCP Inspector (requires Node.js)
npx @modelcontextprotocol/inspector spotinfo --mcp
```

### Verification Steps

1. **Test CLI mode first**:
   ```bash
   spotinfo --type "t3.micro" --region "us-east-1"
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
- **Transport**: `stdio` (Claude Desktop compatible)
- **Capabilities**: `tools`

### Response Format

All responses follow MCP specification:

```json
{
  "jsonrpc": "2.0",
  "result": {
    "content": [
      {
        "type": "text",
        "text": "Found 5 matching spot instances..."
      }
    ]
  },
  "id": "request-id"
}
```

## Benefits of MCP Integration

1. **Natural Language Interface**: Ask questions about spot instances in plain English
2. **Intelligent Recommendations**: Claude can analyze your requirements and suggest optimal configurations  
3. **Real-time Data**: Access current spot pricing and interruption data
4. **Cross-region Analysis**: Easily compare options across multiple AWS regions
5. **Automated Decision Making**: Use Claude's reasoning to optimize cost vs. reliability trade-offs

The MCP integration transforms `spotinfo` from a CLI tool into an intelligent infrastructure advisor, making AWS Spot Instance selection more accessible and efficient.

## API Reference

For complete MCP tool specifications, see [API Reference](api-reference.md).

## See Also

- [Claude Desktop Setup](claude-desktop-setup.md) - Detailed setup instructions
- [Usage Guide](usage.md) - CLI command reference
- [Troubleshooting](troubleshooting.md) - Common issues and solutions