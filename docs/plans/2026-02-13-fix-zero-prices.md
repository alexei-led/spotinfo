# Fix Zero Prices for Newer Instance Types (Issue #6)

## Overview
Some newer AWS instance families (m8g, r8g, m8gd, r8gd) show $0 spot price because the embedded spot-price-data.json from the legacy AWS S3 feed doesn't include them. The spot-advisor-data.json has these instances with savings percentages, but price lookup returns 0.

Fix: add an EC2 DescribeSpotPriceHistory API fallback for instances with zero/missing prices.

## Context
- Files involved: `internal/spot/data.go`, `internal/spot/client.go`, `internal/spot/types.go`
- Related patterns: existing `fetchAdvisorData` and `fetchPriceData` in `data.go` with HTTP fetch + embedded fallback
- Dependencies: `github.com/aws/aws-sdk-go-v2/service/ec2` (already in go.mod)

## Validation Commands
- `make test`
- `make lint`

## Development Approach
- **Testing approach**: Regular (code first, then tests)
- Complete each task fully before moving to the next
- Follow existing patterns in data.go for fetching and error handling
- **CRITICAL: every task MUST include new/updated tests**
- **CRITICAL: all tests must pass before starting next task**

### Task 1: Add EC2 spot price fallback function

**Files:**
- Modify: `internal/spot/data.go`
- Modify: `internal/spot/types.go` (if needed for new types)

- [x] Add a function `fetchEC2SpotPrices(ctx context.Context, region string, instanceTypes []string) (map[string]float64, error)` that calls EC2 DescribeSpotPriceHistory API to get current spot prices for Linux instances
- [x] Use aws-sdk-go-v2 default credential chain (config.LoadDefaultConfig)
- [x] Add proper timeout (5s), error handling, and logging consistent with existing patterns
- [x] Only fetch the most recent price per instance type (limit results)
- [x] Write tests with mocked EC2 client for this function
- [x] Run `make test` - must pass before task 2

### Task 2: Integrate fallback into price lookup

**Files:**
- Modify: `internal/spot/data.go` (getSpotInstancePrice or the caller)
- Modify: `internal/spot/client.go` (if price enrichment happens there)

- [x] After loading prices from the S3 feed/embedded data, identify instances with zero or missing prices that exist in advisor data
- [x] For those instances, batch-call the EC2 fallback function per region
- [x] Merge EC2 prices into the price map so downstream code works unchanged
- [x] If EC2 API fails or returns no data, gracefully keep price as 0 (no regression)
- [x] Write tests covering: successful fallback, EC2 API failure, no zero-price instances
- [x] Run `make test` - must pass before task 3

### Task 3: Verify acceptance criteria

- [ ] Manual test: run `go run ./cmd/spotinfo --type "^m8g.*" --region "us-east-1"` and verify non-zero prices appear (requires AWS credentials)
- [ ] Run full test suite: `make test`
- [ ] Run linter: `make lint`
- [ ] Verify test coverage meets 80%+

### Task 4: Update documentation

- [ ] Update README.md to mention EC2 API fallback for pricing and that AWS credentials are optional (improves accuracy for newer instance types)
- [ ] Update CLAUDE.md if internal patterns changed
- [ ] Move this plan to `docs/plans/completed/`
