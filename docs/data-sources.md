# Data Sources

## Overview

`spotinfo` combines multiple data sources to provide comprehensive AWS EC2 Spot Instance information, including pricing, interruption rates, and placement scores.

## Primary Data Sources

### 1. AWS Spot Instance Advisor Data
- **Source**: [AWS Spot Advisor JSON feed](https://spot-bid-advisor.s3.amazonaws.com/spot-advisor-data.json)
- **Maintained by**: AWS team
- **Update frequency**: Regularly updated by AWS
- **Contains**:
  - Instance specifications (vCPU, memory, EMR compatibility)
  - Interruption frequency ranges
  - Savings percentages compared to on-demand pricing
  - Regional availability data

### 2. AWS Spot Pricing Data
- **Source**: [AWS Spot Pricing JS callback file](http://spot-price.s3.amazonaws.com/spot.js)
- **Maintained by**: AWS team
- **Update frequency**: Regularly updated by AWS
- **Contains**:
  - Current spot prices by region and instance type
  - Operating system pricing variations (Linux/Windows)
  - Historical pricing trends

### 3. AWS Spot Placement Scores API
- **Source**: AWS `GetSpotPlacementScores` API
- **Access**: Real-time API calls (requires IAM permissions)
- **Contains**:
  - Regional placement scores (1-10 scale)
  - Availability zone-level placement scores
  - Likelihood of successful spot instance launch
  - Contextual scoring based on request composition

## Data Flow Architecture

```mermaid
graph TB
    A[AWS Spot Advisor<br/>JSON Feed] --> D[Data Aggregation]
    B[AWS Spot Pricing<br/>JS Feed] --> D
    C[AWS Placement Scores<br/>API] --> D
    
    D --> E[spotinfo Engine]
    
    E --> F[Embedded Data<br/>Fallback]
    E --> G[Cached Results]
    
    G --> H[CLI Output]
    F --> H
    
    style A fill:#e1f5fe
    style B fill:#e1f5fe
    style C fill:#fff3e0
    style F fill:#f3e5f5
    style G fill:#e8f5e8
```

## Network Resilience

### Embedded Data
- **Purpose**: Ensure functionality without network connectivity
- **Implementation**: Data is [embedded](https://golang.org/pkg/embed) into the binary during build
- **Coverage**: Complete spot advisor and pricing data snapshot
- **Update process**: Refreshed during each build via `make update-data`

### Fallback Strategy
1. **Primary**: Fetch fresh data from AWS feeds
2. **Secondary**: Use embedded data if network unavailable
3. **Placement Scores**: Graceful degradation to mock scores if API inaccessible

## Data Processing Pipeline

### 1. Data Fetching
```go
// Pseudo-code flow
func fetchData() {
    advisorData := fetchFromURL("https://spot-bid-advisor.s3.amazonaws.com/spot-advisor-data.json")
    if advisorData == nil {
        advisorData = loadEmbeddedAdvisorData()
    }
    
    pricingData := fetchFromURL("http://spot-price.s3.amazonaws.com/spot.js")
    if pricingData == nil {
        pricingData = loadEmbeddedPricingData()
    }
}
```

### 2. Data Transformation
- **JSON parsing**: Convert AWS JSON format to internal structures
- **Price extraction**: Parse JavaScript callback format for pricing
- **Data normalization**: Standardize formats across sources
- **Validation**: Ensure data integrity and completeness

### 3. Data Enrichment
- **Instance type mapping**: Combine advisor and pricing data
- **Score integration**: Add placement scores when requested
- **Regional filtering**: Apply user-specified region constraints
- **Specification filtering**: Apply CPU, memory, and price filters

## Cache Strategy

### Placement Score Caching
- **Cache duration**: 10 minutes
- **Cache key format**: `region:az_flag:instance_types`
- **Purpose**: Reduce AWS API calls and improve performance
- **Implementation**: LRU cache with expiration

### Data Freshness Tracking
- **Timestamp tracking**: Record when data was last fetched
- **Freshness indicators**: Visual indicators for stale data (>30 minutes)
- **JSON metadata**: Include `score_fetched_at` timestamps in output

## Data Accuracy and Limitations

### Spot Advisor Data
- **Accuracy**: High - directly from AWS
- **Limitations**: 
  - Static snapshot updated periodically by AWS
  - May not reflect real-time market conditions
  - Regional variations in update frequency

### Spot Pricing Data
- **Accuracy**: High - current market prices
- **Limitations**:
  - Prices change frequently
  - Some regions may have delayed updates
  - Embedded data becomes stale over time

### Placement Scores
- **Accuracy**: Real-time from AWS API
- **Limitations**:
  - Requires proper IAM permissions
  - May be restricted by Service Control Policies
  - Contextual scoring can be confusing to users
  - API rate limits apply

## Data Update Process

### Build-Time Updates
```bash
# Update embedded data during build
make update-data    # Updates spot advisor data
make update-price   # Updates spot pricing data
make build          # Embeds fresh data in binary
```

### Runtime Data Flow
1. **Startup**: Load embedded data as baseline
2. **Network fetch**: Attempt to fetch fresh data from AWS feeds
3. **Merge**: Combine fresh data with embedded fallback
4. **API calls**: Fetch placement scores on demand (if enabled)
5. **Cache**: Store results for performance optimization

## Monitoring and Observability

### Data Source Health
- **Connection testing**: Verify AWS feed accessibility
- **Data validation**: Ensure JSON structure integrity
- **Fallback detection**: Log when embedded data is used

### Performance Metrics
- **Fetch duration**: Monitor AWS feed response times
- **Cache hit rate**: Track placement score cache effectiveness
- **API quota usage**: Monitor placement score API consumption

## Security Considerations

### API Access
- **IAM permissions**: Requires `ec2:GetSpotPlacementScores` permission
- **Credential management**: Uses AWS SDK default credential chain
- **Network security**: HTTPS for advisor data, HTTP for pricing (AWS provided)

### Data Privacy
- **No personal data**: All data is public AWS pricing information
- **No data retention**: Only temporary caching for performance
- **No external transmission**: Data stays within AWS and local system

## Troubleshooting Data Issues

### Common Problems

**Stale pricing data:**
```bash
# Force fresh data fetch
make update-data update-price build
```

**Missing placement scores:**
```bash
# Verify API permissions
aws ec2 get-spot-placement-scores --instance-types t3.micro --target-capacity 1 --region us-east-1
```

**Network connectivity issues:**
- Tool automatically falls back to embedded data
- Check network connectivity to `spot-bid-advisor.s3.amazonaws.com`
- Verify firewall settings for outbound HTTPS

**Permission errors:**
- Check IAM policy includes `ec2:GetSpotPlacementScores`
- Verify no Service Control Policy blocks the action
- Test with AWS CLI: `aws sts get-caller-identity`

## See Also

- [AWS Spot Placement Scores](aws-spot-placement-scores.md) - Detailed placement score documentation
- [Troubleshooting](troubleshooting.md) - Common issues and solutions
- [Usage Guide](usage.md) - Command reference and examples