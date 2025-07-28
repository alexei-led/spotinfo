# API Reference

This document provides complete technical specifications for the `spotinfo` MCP server tools.

## Overview

The spotinfo MCP server exposes two tools that enable AI assistants to query AWS EC2 Spot Instance data:

- **`find_spot_instances`**: Search and filter spot instances based on requirements
- **`list_spot_regions`**: List available AWS regions for spot instances

All tools follow the [Model Context Protocol (MCP) specification](https://modelcontextprotocol.io/) and communicate via JSON-RPC 2.0 over stdio transport.

## Server Information

- **Protocol Version**: `2024-11-05`
- **Server Name**: `spotinfo`
- **Transport**: `stdio` (Claude Desktop compatible)
- **Capabilities**: `tools`

## Tools

### `find_spot_instances`

Search for AWS EC2 Spot Instance options based on specified requirements.

#### Description
This tool searches the embedded AWS Spot Instance data and returns matching instances with pricing, savings, and interruption frequency information. It supports filtering by instance specifications, price constraints, and reliability requirements.

#### Input Schema

```json
{
  "type": "object",
  "properties": {
    "regions": {
      "type": "array",
      "items": {
        "type": "string"
      },
      "description": "AWS regions to search (e.g., [\"us-east-1\", \"eu-west-1\"]). Use [\"all\"] or omit to search all regions",
      "default": ["all"],
      "examples": [
        ["us-east-1"],
        ["us-east-1", "us-west-2"],
        ["eu-west-1", "eu-central-1"],
        ["all"]
      ]
    },
    "instance_types": {
      "type": "string",
      "description": "Instance type pattern - exact type (e.g., 'm5.large') or pattern (e.g., 't3.*', 'm5.*')",
      "examples": [
        "t3.micro",
        "m5.large",
        "t3.*",
        "m5.*",
        "r5.xlarge"
      ]
    },
    "min_vcpu": {
      "type": "number",
      "minimum": 0,
      "description": "Minimum number of vCPUs required",
      "default": 0,
      "examples": [2, 4, 8, 16]
    },
    "min_memory_gb": {
      "type": "number",
      "minimum": 0,
      "description": "Minimum memory in gigabytes",
      "default": 0,
      "examples": [4, 8, 16, 32, 64]
    },
    "max_price_per_hour": {
      "type": "number",
      "minimum": 0,
      "description": "Maximum spot price per hour in USD",
      "default": 0,
      "examples": [0.1, 0.5, 1.0, 2.0]
    },
    "max_interruption_rate": {
      "type": "number",
      "minimum": 0,
      "maximum": 100,
      "description": "Maximum acceptable interruption rate percentage (0-100)",
      "default": 100,
      "examples": [5, 10, 15, 20]
    },
    "sort_by": {
      "type": "string",
      "enum": ["price", "reliability", "savings", "score"],
      "description": "Sort results by: 'price' (cheapest first), 'reliability' (lowest interruption first), 'savings' (highest savings first), 'score' (highest score first)",
      "default": "reliability"
    },
    "limit": {
      "type": "number",
      "minimum": 1,
      "maximum": 50,
      "description": "Maximum number of results to return",
      "default": 10
    },
    "with_score": {
      "type": "boolean",
      "description": "Include AWS spot placement scores (experimental)",
      "default": false
    },
    "min_score": {
      "type": "number",
      "minimum": 0,
      "maximum": 10,
      "description": "Filter: minimum spot placement score (1-10)",
      "default": 0
    },
    "az": {
      "type": "boolean", 
      "description": "Request AZ-level scores instead of region-level (use with with_score)",
      "default": false
    },
    "score_timeout": {
      "type": "number",
      "minimum": 1,
      "maximum": 300,
      "description": "Timeout for score enrichment in seconds",
      "default": 30
    }
  },
  "additionalProperties": false
}
```

#### Output Schema

```json
{
  "type": "object",
  "properties": {
    "results": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "instance_type": {
            "type": "string",
            "description": "EC2 instance type",
            "example": "t3.micro"
          },
          "region": {
            "type": "string",
            "description": "AWS region code",
            "example": "us-east-1"
          },
          "spot_price_per_hour": {
            "type": "number",
            "description": "Current spot price per hour in USD",
            "example": 0.0104
          },
          "spot_price": {
            "type": "string",
            "description": "Formatted spot price with currency",
            "example": "$0.0104/hour"
          },
          "savings_percentage": {
            "type": "number",
            "description": "Savings compared to on-demand pricing as percentage",
            "example": 68
          },
          "savings": {
            "type": "string",
            "description": "Formatted savings description",
            "example": "68% cheaper than on-demand"
          },
          "interruption_rate": {
            "type": "number",
            "description": "Average interruption rate as percentage",
            "example": 7.5
          },
          "interruption_frequency": {
            "type": "string",
            "description": "Interruption frequency range label",
            "examples": ["<5%", "5-10%", "10-15%", "15-20%", ">20%"]
          },
          "interruption_range": {
            "type": "string",
            "description": "Formatted interruption range",
            "example": "5-10%"
          },
          "vcpu": {
            "type": "number",
            "description": "Number of virtual CPUs",
            "example": 1
          },
          "memory_gb": {
            "type": "number",
            "description": "Memory in gigabytes",
            "example": 1.0
          },
          "specs": {
            "type": "string",
            "description": "Formatted instance specifications",
            "example": "1 vCPU, 1 GB RAM"
          },
          "reliability_score": {
            "type": "number",
            "description": "Calculated reliability score (100 - interruption_rate)",
            "example": 92
          },
          "region_score": {
            "type": "number",
            "description": "AWS spot placement score for the region (1-10, higher is better)",
            "example": 8
          },
          "zone_scores": {
            "type": "object",
            "description": "AWS spot placement scores by availability zone (1-10, higher is better)",
            "example": {
              "us-east-1a": 9,
              "us-east-1b": 7,
              "us-east-1c": 8
            }
          },
          "score_fetched_at": {
            "type": "string",
            "format": "date-time",
            "description": "Timestamp when scores were fetched from AWS API",
            "example": "2024-01-15T10:30:00Z"
          }
        },
        "required": [
          "instance_type",
          "region",
          "spot_price_per_hour",
          "spot_price",
          "savings_percentage",
          "savings",
          "interruption_rate",
          "interruption_frequency",
          "interruption_range",
          "vcpu",
          "memory_gb",
          "specs",
          "reliability_score"
        ]
      }
    },
    "metadata": {
      "type": "object",
      "properties": {
        "total_results": {
          "type": "number",
          "description": "Number of results returned"
        },
        "regions_searched": {
          "type": "array",
          "items": {
            "type": "string"
          },
          "description": "List of regions that were searched"
        },
        "query_time_ms": {
          "type": "number",
          "description": "Query execution time in milliseconds"
        },
        "data_source": {
          "type": "string",
          "description": "Source of the data",
          "enum": ["embedded"]
        },
        "data_freshness": {
          "type": "string",
          "description": "Freshness indicator for the data",
          "enum": ["current"]
        }
      },
      "required": [
        "total_results",
        "regions_searched", 
        "query_time_ms",
        "data_source",
        "data_freshness"
      ]
    }
  },
  "required": ["results", "metadata"]
}
```

#### Example Requests

##### Basic Search
```json
{
  "name": "find_spot_instances",
  "arguments": {
    "instance_types": "t3.micro",
    "regions": ["us-east-1"]
  }
}
```

##### Advanced Filtering
```json
{
  "name": "find_spot_instances",
  "arguments": {
    "instance_types": "m5.*",
    "min_vcpu": 4,
    "min_memory_gb": 16,
    "max_price_per_hour": 0.5,
    "max_interruption_rate": 10,
    "sort_by": "price",
    "limit": 5
  }
}
```

##### Global Search
```json
{
  "name": "find_spot_instances",
  "arguments": {
    "instance_types": "r5.xlarge",
    "regions": ["all"],
    "sort_by": "reliability",
    "limit": 10
  }
}
```

#### Example Response

```json
{
  "results": [
    {
      "instance_type": "t3.micro",
      "region": "us-east-1",
      "spot_price_per_hour": 0.0104,
      "spot_price": "$0.0104/hour",
      "savings_percentage": 68,
      "savings": "68% cheaper than on-demand",
      "interruption_rate": 7.5,
      "interruption_frequency": "5-10%",
      "interruption_range": "5-10%",
      "vcpu": 1,
      "memory_gb": 1.0,
      "specs": "1 vCPU, 1 GB RAM",
      "reliability_score": 92
    }
  ],
  "metadata": {
    "total_results": 1,
    "regions_searched": ["us-east-1"],
    "query_time_ms": 45,
    "data_source": "embedded",
    "data_freshness": "current"
  }
}
```

---

### `list_spot_regions`

List all AWS regions where EC2 Spot Instances are available.

#### Description
This tool returns a list of all AWS regions that have spot instance data available. The list is dynamically generated from the embedded spot instance data.

#### Input Schema

```json
{
  "type": "object",
  "properties": {
    "include_names": {
      "type": "boolean",
      "description": "Include human-readable region names (e.g., 'US East (N. Virginia)')",
      "default": true
    }
  },
  "additionalProperties": false
}
```

#### Output Schema

```json
{
  "type": "object",
  "properties": {
    "regions": {
      "type": "array",
      "items": {
        "type": "string"
      },
      "description": "Array of AWS region codes"
    },
    "total": {
      "type": "number",
      "description": "Total number of available regions"
    }
  },
  "required": ["regions", "total"]
}
```

#### Example Request

```json
{
  "name": "list_spot_regions",
  "arguments": {}
}
```

#### Example Response

```json
{
  "regions": [
    "ap-northeast-1",
    "ap-northeast-2", 
    "ap-south-1",
    "ap-southeast-1",
    "ap-southeast-2",
    "ca-central-1",
    "eu-central-1",
    "eu-north-1",
    "eu-west-1",
    "eu-west-2",
    "eu-west-3",
    "sa-east-1",
    "us-east-1",
    "us-east-2",
    "us-west-1",
    "us-west-2"
  ],
  "total": 16
}
```

## Error Handling

All tools return standard MCP error responses when issues occur:

### Error Response Format

```json
{
  "error": {
    "code": -32603,
    "message": "Internal error",
    "data": {
      "details": "Specific error description"
    }
  }
}
```

### Common Error Scenarios

#### Invalid Parameters
```json
{
  "error": {
    "code": -32602,
    "message": "Invalid params",
    "data": {
      "details": "max_interruption_rate must be between 0 and 100"
    }
  }
}
```

#### No Results Found
When no instances match the search criteria, the tool returns a successful response with empty results:

```json
{
  "results": [],
  "metadata": {
    "total_results": 0,
    "regions_searched": ["us-east-1"],
    "query_time_ms": 12,
    "data_source": "embedded",
    "data_freshness": "current"
  }
}
```

## Data Sources and Freshness

### Embedded Data
The spotinfo MCP server uses embedded AWS data that is included in the binary during build time:

- **Spot Advisor Data**: Instance interruption frequency data from AWS Spot Instance Advisor
- **Spot Pricing Data**: Current spot pricing from AWS Spot Pricing API
- **Update Frequency**: Data is refreshed with each release of spotinfo

### Data Limitations

1. **Offline Operation**: All data is embedded, enabling offline functionality
2. **Update Lag**: Data freshness depends on release frequency
3. **Regional Coverage**: Limited to regions with public spot instance data
4. **Instance Types**: Covers standard EC2 instance types available in spot market

## Performance Characteristics

### Response Times
- **Single Region**: < 50ms typical
- **Multiple Regions**: < 100ms typical  
- **Global Search**: < 200ms typical

### Result Limits
- **Default Limit**: 10 results
- **Maximum Limit**: 50 results
- **Recommendation**: Use filters to reduce result set for better performance

### Memory Usage
- **Server Memory**: ~10MB typical
- **Per Request**: ~1MB additional during processing

## Integration Patterns

### Typical Usage Flows

#### 1. Cost Optimization
```
1. Call list_spot_regions to see available regions
2. Call find_spot_instances with price constraints
3. Sort by price to find most cost-effective options
```

#### 2. Reliability-First
```
1. Call find_spot_instances with max_interruption_rate filter
2. Sort by reliability to find most stable instances
3. Compare across regions for best availability
```

#### 3. Requirements-Based
```
1. Call find_spot_instances with min_vcpu and min_memory_gb
2. Apply price and interruption constraints
3. Review specs to confirm suitability
```

### Best Practices

1. **Filter Early**: Use constraints to reduce result set
2. **Limit Results**: Use appropriate limit values for your use case
3. **Cache Results**: Consider caching region lists as they change infrequently
4. **Handle Empty Results**: Always check for empty result sets
5. **Progressive Refinement**: Start broad, then apply additional filters

## Testing and Validation

### Testing with MCP Inspector

```bash
# Install MCP Inspector
npm install -g @modelcontextprotocol/inspector

# Test the server
npx @modelcontextprotocol/inspector spotinfo --mcp

# Test specific tool calls
# In the Inspector interface:
find_spot_instances {"instance_types": "t3.micro", "regions": ["us-east-1"]}
list_spot_regions {}
```

### Validation Examples

#### Valid Parameter Combinations
```json
// Minimal request
{"instance_types": "t3.micro"}

// With constraints
{
  "min_vcpu": 2,
  "max_price_per_hour": 0.1,
  "sort_by": "price"
}

// Regional focus
{
  "regions": ["us-east-1", "us-west-2"],
  "instance_types": "m5.*",
  "limit": 5
}
```

#### Invalid Parameter Examples
```json
// Invalid sort option
{"sort_by": "invalid"}

// Negative values
{"min_vcpu": -1}

// Exceeding limits
{"limit": 100}

// Invalid region format
{"regions": "us-east-1"}  // Should be array
```

## Version Compatibility

### MCP Protocol
- **Supported Version**: `2024-11-05`
- **Backward Compatibility**: None (first MCP implementation)

### API Versioning
- **Current Version**: 1.0
- **Stability**: Stable (no breaking changes planned)
- **Deprecation Policy**: 6-month notice for breaking changes

This API reference provides complete technical specifications for integrating with the spotinfo MCP server. For setup instructions, see the [Claude Desktop integration guide](claude-desktop-setup.md).