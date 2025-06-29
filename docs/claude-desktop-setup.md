# Claude Desktop Integration Guide

This guide provides detailed instructions for integrating `spotinfo` with Claude Desktop using the Model Context Protocol (MCP). After setup, you'll be able to ask Claude natural language questions about AWS EC2 Spot Instances.

## Prerequisites

- **Claude Desktop** installed and running
- **spotinfo** binary installed and accessible
- Basic familiarity with JSON configuration files

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
sudo cp spotinfo /usr/local/bin/
```

## Step 2: Verify Installation

Test that spotinfo is working correctly:

```bash
# Test CLI functionality
spotinfo --type=t3.micro --region=us-east-1 --output=json

# Test MCP server mode
spotinfo --mcp
# Should start and wait for input (press Ctrl+C to exit)
```

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
```
Human: Can you list the available AWS regions for spot instances?
```

Expected: Claude should use the `list_spot_regions` tool and return a list of AWS regions.

### Advanced Test
```
Human: Find me the cheapest t3.micro instances with less than 10% interruption rate
```

Expected: Claude should use the `find_spot_instances` tool with appropriate filters.

## Troubleshooting

### Common Issues and Solutions

#### 1. Claude doesn't see the spotinfo tools

**Symptoms:**
- Claude responds with "I don't have access to AWS spot instance tools"
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

#### 2. Permission denied errors

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

#### 3. Binary not found

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

#### 4. Configuration file issues

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

#### 5. macOS Security Warnings

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
Configure default AWS region or other settings:

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

## Usage Examples

Once configured, you can ask Claude various questions about AWS Spot Instances:

### Cost Optimization
```
Human: What are the most cost-effective ways to run a web server on AWS using spot instances?

Claude: I'll help you find cost-effective spot instances for web server workloads...
```

### Regional Comparison
```
Human: Compare t3.medium spot prices across all US regions and recommend the best option

Claude: I'll compare t3.medium spot pricing across US regions for you...
```

### Requirements-Based Search
```
Human: I need an instance with at least 8 vCPUs and 32GB RAM for data processing, but I want to keep costs under $0.50/hour. What are my options?

Claude: I'll search for instances meeting your specifications within budget...
```

### Infrastructure Planning
```
Human: Help me plan a cost-effective Kubernetes cluster using spot instances with different node types

Claude: I'll help you design a diverse spot instance setup for Kubernetes...
```

## Verification Steps

### 1. Check MCP Server Status
```bash
# Manual test
spotinfo --mcp
# Should start and display server information
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
3. **Test with CLI first**: `spotinfo --type=t3.micro --region=us-east-1`
4. **File an issue**: [GitHub Issues](https://github.com/alexei-led/spotinfo/issues)

## Next Steps

- Explore the [API reference](api-reference.md) for detailed tool documentation
- Review [troubleshooting guide](troubleshooting.md) for common issues
- Learn about advanced usage patterns in the main README
- Consider contributing improvements or reporting bugs

The integration transforms Claude into an intelligent AWS Spot Instance advisor, making infrastructure decisions more informed and efficient.