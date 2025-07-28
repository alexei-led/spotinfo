# Examples and Use Cases

This document provides real-world examples of using `spotinfo` for common DevOps scenarios.

## DevOps Use Cases

### 1. Production Workload Deployment

**Scenario**: Deploy a production web application requiring high availability and cost optimization.

**Requirements**:
- At least 4 vCPU and 16GB RAM
- High placement score (8+) for reliability
- Compare multiple regions
- Budget constraint: $0.30/hour

```bash
# Find optimal instances across multiple regions
spotinfo \
  --cpu 4 \
  --memory 16 \
  --with-score \
  --min-score 8 \
  --price 0.30 \
  --region "us-east-1" \
  --region "us-west-2" \
  --region "eu-west-1" \
  --sort score \
  --order desc \
  --output table
```

**Expected Output**:
```
┌───────────────┬──────────┬──────┬────────────┬──────────┬────────────────────────────┐
│ REGION        │ INSTANCE │ VCPU │ MEMORY GIB │ USD/HOUR │ PLACEMENT SCORE (REGIONAL) │
├───────────────┼──────────┼──────┼────────────┼──────────┼────────────────────────────┤
│ us-east-1     │ m5.xlarge│    4 │         16 │   0.0817 │ 🟢 9                       │
│ eu-west-1     │ m5.xlarge│    4 │         16 │   0.0923 │ 🟢 8                       │
└───────────────┴──────────┴──────┴────────────┴──────────┴────────────────────────────┘
```

### 2. Development Environment Setup

**Scenario**: Cost-effective development instances for a team of developers.

**Requirements**:
- Small instances (t3 family)
- Acceptable reliability (score 5+)
- Multiple options for flexibility
- Lowest cost priority

```bash
# Find cheapest t3 instances with decent reliability
spotinfo \
  --type "t3.*" \
  --with-score \
  --min-score 5 \
  --sort price \
  --order asc \
  --region "us-east-1" \
  --output table
```

### 3. Machine Learning Training Jobs

**Scenario**: GPU instances for ML training workloads that can handle interruptions.

**Requirements**:
- GPU instances (p3, g4 families)
- Cost optimization priority
- AZ-level placement for precise targeting

```bash
# Compare GPU instances with AZ-level scores
spotinfo \
  --type "(p3|g4).*" \
  --with-score \
  --az \
  --region "us-east-1" \
  --sort price \
  --order asc \
  --output table
```

### 4. Batch Processing Workloads

**Scenario**: Large-scale data processing requiring high memory.

**Requirements**:
- Memory-optimized instances (r5 family)
- At least 64GB RAM
- Regional analysis for capacity planning

```bash
# Find high-memory instances across regions
spotinfo \
  --type "r5.*" \
  --memory 64 \
  --with-score \
  --region "all" \
  --sort score \
  --order desc \
  --output json > high_memory_options.json
```

## Automation Examples

### 5. Infrastructure as Code Integration

**Terraform Variable Generation**:

```bash
#!/bin/bash
# Generate Terraform variables for spot instances

INSTANCE_DATA=$(spotinfo \
  --type "m5\.(large|xlarge)" \
  --with-score \
  --min-score 7 \
  --region "us-east-1" \
  --output json)

# Extract best instance type
BEST_INSTANCE=$(echo "$INSTANCE_DATA" | jq -r '.[0].instance')

# Generate Terraform variables
cat > terraform.tfvars <<EOF
spot_instance_type = "$BEST_INSTANCE"
availability_zone = "us-east-1a"
max_price = "$(echo "$INSTANCE_DATA" | jq -r '.[0].price * 1.1')"
EOF

echo "Generated terraform.tfvars with optimal spot instance configuration"
```

### 6. CI/CD Pipeline Integration

**GitLab CI Example**:

```yaml
# .gitlab-ci.yml
variables:
  MAX_SPOT_PRICE: "0.50"

validate_spot_cost:
  stage: validate
  image: ghcr.io/alexei-led/spotinfo:latest
  script:
    - |
      CURRENT_PRICE=$(spotinfo --type "$INSTANCE_TYPE" --region "$AWS_REGION" --output number)
      if (( $(echo "$CURRENT_PRICE > $MAX_SPOT_PRICE" | bc -l) )); then
        echo "Spot price $CURRENT_PRICE exceeds budget $MAX_SPOT_PRICE"
        exit 1
      fi
      echo "Spot price validation passed: $CURRENT_PRICE <= $MAX_SPOT_PRICE"

deploy_infrastructure:
  stage: deploy
  dependencies:
    - validate_spot_cost
  script:
    - terraform apply -auto-approve
```

### 7. Cost Monitoring Script

**Daily Cost Analysis**:

```bash
#!/bin/bash
# daily_spot_analysis.sh - Monitor spot instance costs

DATE=$(date +%Y-%m-%d)
REPORT_FILE="spot_report_$DATE.json"

# Generate comprehensive spot analysis
spotinfo \
  --type "m5.*" \
  --with-score \
  --region "us-east-1" \
  --region "us-west-2" \
  --region "eu-west-1" \
  --sort price \
  --order asc \
  --output json > "$REPORT_FILE"

# Extract insights
CHEAPEST=$(jq -r '.[0] | "\(.instance) in \(.region): $\(.price)/hour"' "$REPORT_FILE")
HIGHEST_SCORE=$(jq -r 'sort_by(.region_score) | reverse | .[0] | "\(.instance) in \(.region): score \(.region_score)"' "$REPORT_FILE")

# Send to monitoring system
cat <<EOF | curl -X POST -H 'Content-Type: application/json' -d @- "$WEBHOOK_URL"
{
  "text": "Daily Spot Analysis",
  "blocks": [
    {
      "type": "section",
      "text": {
        "type": "mrkdwn",
        "text": "*Cheapest*: $CHEAPEST\n*Highest Score*: $HIGHEST_SCORE"
      }
    }
  ]
}
EOF
```

## Advanced Queries

### 8. Multi-Criteria Optimization

**Find the sweet spot between cost and reliability**:

```bash
# Score instances by combined cost-reliability metric
spotinfo \
  --type "c5.*" \
  --with-score \
  --region "us-east-1" \
  --output json | \
jq 'map(. + {
  "cost_reliability_ratio": (.price / (.region_score // 1))
}) | sort_by(.cost_reliability_ratio) | .[0:5]'
```

### 9. Capacity Planning Analysis

**Compare placement scores for different instance families**:

```bash
# Analyze placement scores across instance families
for family in "m5" "c5" "r5"; do
  echo "=== $family family ==="
  spotinfo \
    --type "${family}.*" \
    --with-score \
    --region "us-east-1" \
    --output json | \
  jq -r '.[] | "\(.instance): \(.region_score // "N/A")"' | \
  head -5
  echo
done
```

### 10. AZ-Level Deployment Strategy

**Compare regional vs AZ-level scores for deployment decisions**:

```bash
#!/bin/bash
INSTANCE_TYPE="m5.large"
REGION="us-east-1"

echo "Regional Score:"
spotinfo --type "$INSTANCE_TYPE" --with-score --region "$REGION" --output text

echo -e "\nAZ-Level Scores:"
spotinfo --type "$INSTANCE_TYPE" --with-score --az --region "$REGION" --output text

echo -e "\nRecommendation:"
REGIONAL_SCORE=$(spotinfo --type "$INSTANCE_TYPE" --with-score --region "$REGION" --output json | jq -r '.[0].region_score // 0')
AZ_SCORE=$(spotinfo --type "$INSTANCE_TYPE" --with-score --az --region "$REGION" --output json | jq -r '.[0].zone_scores | to_entries[0].value // 0')

if [ "$AZ_SCORE" -gt "$REGIONAL_SCORE" ]; then
  echo "Deploy to specific AZ for better placement score"
else
  echo "Regional deployment recommended"
fi
```

## Output Format Examples

### 11. CSV Export for Spreadsheet Analysis

```bash
# Generate CSV report for stakeholders
spotinfo \
  --type "m5.*" \
  --with-score \
  --region "all" \
  --output csv > "spot_instances_$(date +%Y%m%d).csv"

# Import into Google Sheets or Excel for further analysis
```

### 12. JSON Processing with jq

```bash
# Extract specific fields for monitoring
spotinfo \
  --type "c5.*" \
  --with-score \
  --region "us-east-1" \
  --output json | \
jq -r '.[] | select(.region_score >= 8) | 
  "Instance: \(.instance), Score: \(.region_score), Price: $\(.price)/hour"'
```

### 13. Text Format for Logging

```bash
# Log format for system monitoring
spotinfo \
  --type "t3.medium" \
  --with-score \
  --region "us-east-1" \
  --output text | \
  logger -t "spotinfo" -p user.info
```

## Integration Patterns

### 14. Kubernetes Cluster Autoscaler

**Node group optimization**:

```bash
# Find optimal instance types for Kubernetes node groups
spotinfo \
  --cpu 2 \
  --memory 8 \
  --with-score \
  --min-score 7 \
  --region "us-east-1" \
  --sort price \
  --order asc \
  --output json | \
jq -r '.[0:3][] | .instance' | \
tr '\n' ',' | \
sed 's/,$//'
```

### 15. AWS Auto Scaling Groups

**Mixed instance policy configuration**:

```bash
# Generate instance types for ASG mixed instance policy
INSTANCE_TYPES=$(spotinfo \
  --type "m5\.(large|xlarge|2xlarge)" \
  --with-score \
  --min-score 6 \
  --region "us-east-1" \
  --output json | \
jq -r '[.[].instance] | unique | join(",")')

echo "InstanceTypes: $INSTANCE_TYPES"
```

## Troubleshooting Examples

### 16. Permission Debugging

```bash
# Test placement score permissions
if spotinfo --type "t3.micro" --with-score --region "us-east-1" &>/dev/null; then
  echo "✅ Placement score permissions working"
else
  echo "❌ Placement score permissions failed"
  echo "Check IAM policy for ec2:GetSpotPlacementScores"
fi
```

### 17. Performance Testing

```bash
# Measure query performance
time spotinfo --type "m5.*" --region "all" --output json >/dev/null

# Test with placement scores
time spotinfo --type "m5.*" --with-score --region "us-east-1" --output json >/dev/null
```

These examples demonstrate the flexibility and power of `spotinfo` for various DevOps scenarios. Adapt them to your specific requirements and integrate them into your infrastructure automation workflows.

## See Also

- [Usage Guide](usage.md) - Complete command reference
- [AWS Spot Placement Scores](aws-spot-placement-scores.md) - Detailed placement score documentation
- [Troubleshooting](troubleshooting.md) - Common issues and solutions