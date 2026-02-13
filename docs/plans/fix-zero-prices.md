# Plan: Fix Zero Prices for Missing Instance Types (Issue #6)

## Context
Some newer AWS instance types (m8g, r8g, m8gd, r8gd, etc.) show price $0 because they're missing from the AWS public spot pricing feed (`spot-price.s3.amazonaws.com/spot.js`). The Spot Advisor data includes them (with savings % and interruption frequency), but the pricing data doesn't have entries for these types.

## Validation Commands
- `make test`
- `make lint`

### Task 1: Add AWS EC2 DescribeSpotPriceHistory API fallback for missing prices
- [x] In `internal/spot/data.go`, after loading and converting the static pricing data, identify instance types that have advisor data but zero/missing pricing
- [x] Add a new function that uses the AWS EC2 `DescribeSpotPriceHistory` API (via aws-sdk-go-v2) to fetch current spot prices for those missing instance types
- [x] Use the existing AWS config pattern from `score.go` (with timeout, retry, rate limiting)
- [x] Only call the API when there are actually missing prices (don't call it for every run)
- [x] Integrate this fallback into the existing data loading flow in `GetAdvice()`
- [x] Add appropriate error handling — if the API call fails, just leave prices as 0 (graceful degradation)
- [x] Add tests with mocked AWS API calls

### Task 2: Update CLI to show price data source indicator
- [x] Add a field to the `Advice` struct indicating if the price came from static data or live API
- [x] In table/text output, optionally mark live-fetched prices (e.g., with an asterisk or different formatting)
- [x] Update JSON/CSV output to include the price source field
- [x] Add tests for the new output formatting

### Task 3: Update embedded data and documentation
- [x] Run `make update-data update-price` to refresh embedded data files
- [x] Update README.md to document the live price fallback behavior
- [x] Add a note about AWS credentials being optional but recommended for complete pricing
- [x] Close reference to issue #6 in commit messages
