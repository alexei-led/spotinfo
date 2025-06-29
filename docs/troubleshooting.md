# Troubleshooting Guide

This guide helps you diagnose and resolve common issues with `spotinfo` in both CLI and MCP server modes.

## General Diagnostics

Before diving into specific issues, gather basic information about your setup:

### System Information
```bash
# Check spotinfo version
spotinfo --version

# Check installation path
which spotinfo

# Check binary permissions
ls -la $(which spotinfo)

# Test basic functionality
spotinfo --type=t3.micro --region=us-east-1 --output=json
```

### MCP Server Test
```bash
# Test MCP server mode
spotinfo --mcp
# Should start without errors and wait for input (Ctrl+C to exit)

# Check if server responds to basic input
echo '{"jsonrpc": "2.0", "method": "initialize", "id": 1}' | spotinfo --mcp
```

## CLI Mode Issues

### 1. No Data Returned

**Symptoms:**
- Empty results or "No instances found"
- Valid filters return no matches

**Diagnosis:**
```bash
# Test with minimal filters
spotinfo --region=us-east-1 --output=json

# Check if specific instance type exists
spotinfo --type=t3.micro --region=us-east-1

# Test with all regions
spotinfo --type=t3.micro --region=all --limit=1
```

**Solutions:**
- **Expand search criteria**: Remove filters to see if data exists
- **Check region availability**: Some instance types aren't available in all regions
- **Verify instance type spelling**: Use patterns like `t3.*` for family searches
- **Update embedded data**: Newer instance types might not be in embedded data

### 2. Slow Performance

**Symptoms:**
- Commands take longer than 10 seconds
- Timeout errors

**Diagnosis:**
```bash
# Test with single region
time spotinfo --type=t3.micro --region=us-east-1

# Test with multiple regions
time spotinfo --type=t3.micro --region=us-east-1 --region=us-west-2

# Test with all regions
time spotinfo --type=t3.micro --region=all
```

**Solutions:**
- **Limit regions**: Use specific regions instead of `--region=all`
- **Reduce result set**: Use `--limit` parameter to reduce output
- **Check network**: Slow DNS resolution can affect data fetching
- **Use embedded data**: Network issues cause fallback to embedded data

### 3. Invalid Output Format

**Symptoms:**
- Malformed JSON output
- Unexpected formatting

**Diagnosis:**
```bash
# Test different output formats
spotinfo --type=t3.micro --region=us-east-1 --output=table
spotinfo --type=t3.micro --region=us-east-1 --output=json
spotinfo --type=t3.micro --region=us-east-1 --output=csv

# Validate JSON output
spotinfo --type=t3.micro --region=us-east-1 --output=json | jq .
```

**Solutions:**
- **Check for stderr mixing**: Redirect stderr: `spotinfo ... 2>/dev/null`
- **Update to latest version**: Older versions might have formatting bugs
- **Use alternative format**: Try `table` or `csv` if `json` is problematic

## MCP Server Issues

### 1. Claude Can't Find Tools

**Symptoms:**
- Claude responds: "I don't have access to AWS spot instance tools"
- MCP tools don't appear in Claude's capabilities

**Diagnosis:**
```bash
# Verify MCP server starts
spotinfo --mcp
# Should show initialization messages

# Test with MCP Inspector
npx @modelcontextprotocol/inspector spotinfo --mcp

# Check Claude Desktop configuration
cat ~/Library/Application\ Support/Claude/claude_desktop_config.json
```

**Solutions:**
1. **Check configuration file syntax**:
   ```bash
   # Validate JSON
   cat claude_desktop_config.json | jq .
   ```

2. **Verify binary path**:
   ```json
   {
     "mcpServers": {
       "spotinfo": {
         "command": "/correct/path/to/spotinfo",
         "args": ["--mcp"]
       }
     }
   }
   ```

3. **Restart Claude Desktop**:
   - Quit completely
   - Wait 5 seconds
   - Restart

4. **Check permissions**:
   ```bash
   chmod +x /path/to/spotinfo
   ```

### 2. Connection Refused/Timeout

**Symptoms:**
- "Connection refused" errors
- MCP server exits immediately
- Claude shows connection timeout

**Diagnosis:**
```bash
# Check if server stays running
timeout 10s spotinfo --mcp
echo "Exit code: $?"

# Check for error messages
spotinfo --mcp 2>&1 | head -20

# Test stdio communication
echo '{"jsonrpc": "2.0", "method": "ping", "id": 1}' | spotinfo --mcp
```

**Solutions:**
1. **Check for port conflicts** (if using SSE mode):
   ```bash
   # Check if port is in use
   lsof -i :8080
   ```

2. **Verify environment variables**:
   ```bash
   export SPOTINFO_MODE=mcp
   spotinfo
   ```

3. **Check system resources**:
   ```bash
   # Check available memory
   free -h  # Linux
   vm_stat  # macOS
   ```

### 3. Partial or No Data in Responses

**Symptoms:**
- Empty results from MCP tools
- Missing fields in responses
- Tool calls succeed but return no data

**Diagnosis:**
```bash
# Test CLI equivalent
spotinfo --type=t3.micro --region=us-east-1 --output=json

# Test MCP tool directly with Inspector
npx @modelcontextprotocol/inspector spotinfo --mcp
# Then call: find_spot_instances with minimal parameters
```

**Solutions:**
1. **Check parameter formats**:
   ```json
   // Correct format
   {
     "regions": ["us-east-1"],
     "instance_types": "t3.*"
   }
   
   // Incorrect format
   {
     "regions": "us-east-1",
     "instance_types": ["t3.micro"]
   }
   ```

2. **Verify data availability**:
   ```bash
   # Check if data exists for parameters
   spotinfo --type=t3.* --region=us-east-1 --limit=1
   ```

3. **Use broader search criteria**:
   - Remove restrictive filters
   - Increase limit parameter
   - Try different regions

## Platform-Specific Issues

### macOS

#### 1. "Cannot be opened because it is from an unidentified developer"

**Solution:**
```bash
# Remove quarantine
xattr -d com.apple.quarantine /path/to/spotinfo

# Or allow in System Preferences
# System Preferences → Security & Privacy → General → Allow
```

#### 2. Homebrew Installation Issues

**Diagnosis:**
```bash
# Check Homebrew
brew doctor

# Check tap
brew tap alexei-led/spotinfo

# Update Homebrew
brew update
```

**Solution:**
```bash
# Clean reinstall
brew untap alexei-led/spotinfo
brew tap alexei-led/spotinfo
brew install spotinfo
```

### Windows

#### 1. PowerShell Execution Policy

**Symptoms:**
- "Execution of scripts is disabled" errors

**Solution:**
```powershell
# Check current policy
Get-ExecutionPolicy

# Set to allow local scripts
Set-ExecutionPolicy -ExecutionPolicy RemoteSigned -Scope CurrentUser
```

#### 2. Path Issues

**Diagnosis:**
```cmd
# Check if spotinfo is in PATH
where spotinfo

# Check PATH variable
echo %PATH%
```

**Solution:**
```cmd
# Add to PATH or use full path in Claude config
# "command": "C:\\Program Files\\spotinfo\\spotinfo.exe"
```

### Linux

#### 1. Missing Dependencies

**Symptoms:**
- "No such file or directory" for existing binary
- Library errors

**Diagnosis:**
```bash
# Check binary type
file $(which spotinfo)

# Check dependencies
ldd $(which spotinfo)
```

**Solution:**
```bash
# Install missing libraries (Ubuntu/Debian)
sudo apt-get update
sudo apt-get install libc6

# For older systems, use static binary
curl -L https://github.com/alexei-led/spotinfo/releases/latest/download/spotinfo_linux_amd64_static.tar.gz | tar xz
```

## Network and Data Issues

### 1. AWS Data Feed Unavailable

**Symptoms:**
- "Failed to fetch data" warnings
- Outdated pricing information

**Diagnosis:**
```bash
# Test AWS endpoints
curl -s https://spot-bid-advisor.s3.amazonaws.com/spot-advisor-data.json | head
curl -s http://spot-price.s3.amazonaws.com/spot.js | head

# Check network connectivity
ping spot-bid-advisor.s3.amazonaws.com
```

**Solutions:**
- **Use embedded data**: spotinfo automatically falls back to embedded data
- **Check proxy settings**: Configure HTTP_PROXY if behind corporate firewall
- **Update embedded data**: Download latest version with updated embedded data

### 2. Corporate Firewall Issues

**Symptoms:**
- Connection timeouts to AWS endpoints
- Proxy authentication errors

**Solutions:**
```bash
# Set proxy environment variables
export HTTP_PROXY=http://proxy.company.com:8080
export HTTPS_PROXY=http://proxy.company.com:8080

# Test with proxy
curl -s --proxy http://proxy.company.com:8080 https://spot-bid-advisor.s3.amazonaws.com/spot-advisor-data.json
```

## Error Messages and Solutions

### "invalid character" in JSON output

**Cause:** stderr mixed with stdout

**Solution:**
```bash
spotinfo --output=json 2>/dev/null
```

### "no such file or directory"

**Cause:** Binary not found or wrong path

**Solution:**
```bash
# Find correct path
which spotinfo

# Update configuration with full path
```

### "permission denied"

**Cause:** Binary not executable

**Solution:**
```bash
chmod +x /path/to/spotinfo
```

### "context deadline exceeded"

**Cause:** Network timeout or slow response

**Solution:**
- Reduce search scope
- Check network connectivity
- Use embedded data mode

## Debugging Techniques

### 1. Enable Verbose Logging

```bash
# Add debug flags (if implemented)
spotinfo --debug --type=t3.micro --region=us-east-1

# Use strace/dtrace for system call tracing
strace -e network spotinfo --mcp  # Linux
dtruss -n spotinfo --mcp          # macOS
```

### 2. Test MCP Protocol Manually

```bash
# Send raw MCP messages
echo '{"jsonrpc": "2.0", "method": "initialize", "id": 1, "params": {"protocolVersion": "2024-11-05", "capabilities": {}}}' | spotinfo --mcp

# Test tool calls
echo '{"jsonrpc": "2.0", "method": "tools/call", "id": 2, "params": {"name": "list_spot_regions", "arguments": {}}}' | spotinfo --mcp
```

### 3. Compare CLI vs MCP Results

```bash
# CLI result
spotinfo --type=t3.micro --region=us-east-1 --output=json

# MCP equivalent - use Inspector to call:
# find_spot_instances with {"instance_types": "t3.micro", "regions": ["us-east-1"]}
```

## Getting Help

If issues persist after trying these solutions:

1. **Search existing issues**: [GitHub Issues](https://github.com/alexei-led/spotinfo/issues)
2. **Create detailed bug report**:
   - Include spotinfo version (`spotinfo --version`)
   - Include platform information (`uname -a`)
   - Include error messages and logs
   - Include steps to reproduce
3. **Provide configuration**: Include sanitized Claude Desktop config
4. **Test with minimal case**: Try to reproduce with simplest possible example

## Prevention Tips

- **Keep updated**: Regularly update spotinfo to latest version
- **Test changes**: Test configuration changes with MCP Inspector first
- **Monitor logs**: Check Claude Desktop logs for warnings
- **Backup config**: Keep backup of working configuration
- **Document setup**: Note any custom configurations or environment variables

This troubleshooting guide covers the most common issues. For additional help, consult the [Claude Desktop integration guide](claude-desktop-setup.md) and [API reference](api-reference.md).