package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"slices"
	"sort"
	"strings"

	"github.com/urfave/cli/v2"

	"spotinfo/internal/cloud"
	"spotinfo/internal/spot"
)

// recommendationSchemaVersion is the AWS compatibility schema. It is emitted
// for an AWS request under one of the interruption-capped workloads, which is
// exactly what the v1 command accepted. Every other combination — another
// cloud, or the risk-free cost policy — is served by the neutral v2 schema.
const recommendationSchemaVersion = "spotinfo.recommend/v1"

type recommendationRequest struct { //nolint:govet // JSON request field grouping is clearer than memory-layout optimization.
	Architecture              spot.Architecture `json:"architecture"`
	InstanceRegexp            string            `json:"instance_regexp"`
	Regions                   []string          `json:"regions"`
	OS                        string            `json:"os"`
	MinimumVCPU               int               `json:"minimum_vcpu"`
	MinimumMemoryGiB          int               `json:"minimum_memory_gib"`
	MaximumUSDPerInstanceHour *float64          `json:"maximum_usd_per_instance_hour"`
	Workload                  spot.Workload     `json:"workload"`
	Top                       int               `json:"top"`
}

type recommendationReport struct {
	SchemaVersion   string                `json:"schema_version"`
	Request         recommendationRequest `json:"request"`
	RankingPolicy   []string              `json:"ranking_policy"`
	Recommendations []spot.Recommendation `json:"recommendations"`
}

// normalizeRecommendationRegions validates and deterministically deduplicates
// recommendation regions before candidate acquisition and report rendering.
func normalizeRecommendationRegions(regions []string) ([]string, error) {
	unique := make(map[string]struct{}, len(regions))
	for _, region := range regions {
		region = strings.TrimSpace(region)
		if region == "" {
			return nil, fmt.Errorf("%w: region must not be empty", spot.ErrInvalidRecommendationInput)
		}
		unique[region] = struct{}{}
	}
	if _, hasAll := unique[allRegions]; hasAll && len(unique) > 1 {
		return nil, fmt.Errorf("%w: region all cannot be combined with explicit regions", spot.ErrInvalidRecommendationInput)
	}

	normalized := make([]string, 0, len(unique))
	for region := range unique {
		normalized = append(normalized, region)
	}
	sort.Strings(normalized)

	return normalized, nil
}

// execRecommendCmd routes one recommendation request. Provider selection and
// the workload policy are resolved first, because together they decide which
// published schema answers the request.
func execRecommendCmd(ctx *cli.Context, execCtx context.Context, registry providerRegistry, client spotClient, output io.Writer) error {
	if err := requireRecommendFlags(ctx); err != nil {
		return err
	}

	outputFormat := lineageString(ctx, flagOutput)
	if outputFormat != outputTable && outputFormat != outputJSON {
		return fmt.Errorf("%w: output must be table or json", spot.ErrInvalidRecommendationInput)
	}

	// MaxTop is enforced here rather than in the neutral validator so both
	// report paths answer the same way. It was previously enforced only on the
	// MCP surface, on the reasoning that it bounds an MCP *input* — but the
	// bound is also written into the v2 payload schema, which pins request.top
	// at a maximum of 50. `--top 999 --output json` therefore emitted a
	// spotinfo.recommend/v2 document that fails its own published contract.
	if top := ctx.Int(flagTop); top > cloud.MaxTop {
		return fmt.Errorf("%w: top must be between 1 and %d", cloud.ErrInvalidArgument, cloud.MaxTop)
	}

	provider, err := resolveProviderForRecommend(ctx, registry)
	if err != nil {
		return err
	}

	workload, err := recommendWorkload(ctx, provider.Capabilities())
	if err != nil {
		return err
	}

	// AWS under an interruption-capped workload is the documented v1 contract and
	// keeps its own acquisition path. Everything else is answered by the neutral
	// engine, including AWS under the cost policy, which v1 never had.
	if provider.ID() == cloud.ProviderAWS && workload != cloud.WorkloadCost {
		return execAWSRecommendV1(ctx, execCtx, provider, client, workload, outputFormat, output)
	}

	return execNeutralRecommendV2(ctx, execCtx, provider, workload, outputFormat, output)
}

// requireRecommendFlags rejects a request that omits one of the three
// constraints every recommendation needs, naming the flags rather than the wire
// fields they become.
//
// urfave/cli's own Required check is not used for these. It prints the entire
// help page to stdout before returning the error, so `spotinfo recommend >
// out.json` produced a file containing a help page and an exit code of 1, and
// the message named only the first missing flag's declaration order rather than
// what the caller should add.
func requireRecommendFlags(ctx *cli.Context) error {
	var missing []string
	for _, name := range []string{flagArchitecture, flagCPU, flagMemory} {
		if !lineageIsSet(ctx, name) {
			missing = append(missing, "--"+name)
		}
	}

	if len(missing) == 0 {
		return nil
	}

	return fmt.Errorf("%w: %s %s required; every recommendation needs an architecture and a size floor",
		cloud.ErrInvalidArgument, strings.Join(missing, ", "), plural(len(missing), "is", "are"))
}

func plural(count int, singular, multiple string) string {
	if count == 1 {
		return singular
	}

	return multiple
}

// resolveProviderForRecommend applies the fixed failure order: an unrecognised
// --cloud is INVALID_ARGUMENT, a recognised but disabled provider is
// DATA_UNAVAILABLE. The capability check belongs to the chosen policy, so it
// runs after the workload is known.
func resolveProviderForRecommend(ctx *cli.Context, registry providerRegistry) (cloud.Provider, error) {
	id, err := providerID(ctx)
	if err != nil {
		return nil, err
	}

	return registry.Get(id)
}

// recommendWorkload resolves the policy. An explicit value is taken as given.
// An unset --workload defaults to the interruption-capped web policy on a
// provider that publishes risk, and to the risk-free cost policy on one that
// does not — a provider without risk data cannot honestly claim an
// interruption ceiling.
func recommendWorkload(ctx *cli.Context, capabilities cloud.Capabilities) (cloud.Workload, error) {
	value := strings.TrimSpace(lineageString(ctx, flagWorkload))
	if value == "" {
		if capabilities.Has(cloud.CapabilityRisk) {
			return cloud.WorkloadWeb, nil
		}

		return cloud.WorkloadCost, nil
	}

	return cloud.ParseWorkload(value)
}

// execAWSRecommendV1 is the unchanged AWS recommendation path: legacy
// acquisition, legacy ranking, and the spotinfo.recommend/v1 report.
func execAWSRecommendV1(ctx *cli.Context, execCtx context.Context, provider cloud.Provider,
	client spotClient, workload cloud.Workload, outputFormat string, output io.Writer,
) error {
	opts, err := legacyRecommendationOptions(ctx, workload)
	if err != nil {
		return err
	}

	regions, err := normalizeRecommendationRegions(lineageStringSlice(ctx, flagRegion))
	if err != nil {
		return err
	}

	// The capability check runs before acquisition, so an unsupported request
	// costs no I/O.
	if capErr := provider.Capabilities().Require(recommendCapabilityRequest(ctx)); capErr != nil {
		return fmt.Errorf("%s: %w", provider.ID(), capErr)
	}

	lookup, err := spot.LoadEmbeddedArchitectureLookup()
	if err != nil {
		return fmt.Errorf("load recommendation architecture data: %w", err)
	}

	advices, err := client.GetSpotSavings(execCtx, legacyQueryOptions(opts, regions)...)
	if err != nil {
		return fmt.Errorf("failed to get recommendation candidates: %w", err)
	}

	recommendations, err := spot.Recommend(advices, opts, lookup)
	if err != nil {
		return err
	}

	report := recommendationReport{
		SchemaVersion: recommendationSchemaVersion,
		Request: recommendationRequest{
			Architecture:     opts.Architecture,
			InstanceRegexp:   opts.Instance,
			Regions:          regions,
			OS:               opts.OS,
			MinimumVCPU:      opts.CPU,
			MinimumMemoryGiB: opts.Memory,
			Workload:         opts.Workload,
			Top:              opts.Top,
		},
		RankingPolicy:   spot.RecommendationRankingPolicy(),
		Recommendations: recommendations,
	}
	if opts.Budget > 0 {
		report.Request.MaximumUSDPerInstanceHour = &opts.Budget
	}

	if outputFormat == outputTable {
		return writeRecommendationTable(report.Recommendations, output)
	}

	return writeJSONReport(report, output)
}

// foldVocabulary normalises a fixed-vocabulary flag value the way the neutral
// parsers in internal/cloud do, so the same spelling is accepted on both the v1
// and v2 recommend paths. It normalises only; an unrecognised value is still
// rejected downstream, with that path's own error type.
func foldVocabulary(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// validBudget reports whether a --budget value can act as a price ceiling.
//
// Both non-finite values have to be named explicitly. NaN fails every
// comparison, so `budget <= 0` alone lets it through as a set-but-unenforceable
// ceiling; +Inf passes that check too and would be stored raw as a ceiling no
// price can exceed, making the filter a silent no-op. The same three terms guard
// --price in main.go — see the comment there.
func validBudget(budget float64) bool {
	return !math.IsNaN(budget) && !math.IsInf(budget, 0) && budget > 0
}

// legacyRecommendationOptions builds and validates the v1 constraint set.
func legacyRecommendationOptions(ctx *cli.Context, workload cloud.Workload) (*spot.RecommendationOptions, error) {
	budget := ctx.Float64(flagBudget)
	// NaN fails every comparison, so `budget <= 0` alone would let it through as
	// a set-but-unenforceable ceiling. +Inf passes it too, and would be stored raw
	// as a ceiling nothing can exceed, making the filter a silent no-op.
	if ctx.IsSet(flagBudget) && !validBudget(budget) {
		return nil, fmt.Errorf("%w: budget must be a positive USD instance-hour price", spot.ErrInvalidRecommendationInput)
	}
	if ctx.IsSet(flagTop) && ctx.Int(flagTop) <= 0 {
		return nil, fmt.Errorf("%w: top must be positive", spot.ErrInvalidRecommendationInput)
	}

	// Folded here, not in the validator: the v1 vocabulary is lowercase, and the
	// neutral parsers on the v2 path fold the same values. Casting the raw flag
	// straight through made `--architecture X86_64` and `--os LINUX` fail on this
	// path alone, while `--cloud AWS` was accepted on both.
	opts := &spot.RecommendationOptions{
		Architecture: spot.Architecture(foldVocabulary(ctx.String(flagArchitecture))),
		Instance:     machineFilter(ctx),
		OS:           foldVocabulary(lineageString(ctx, flagOS)),
		CPU:          lineageInt(ctx, flagCPU, "vcpu"),
		Memory:       lineageInt(ctx, flagMemory, "memory-gib"),
		Budget:       budget,
		Workload:     spot.Workload(workload),
		Top:          ctx.Int(flagTop),
	}
	if err := spot.ValidateRecommendationOptions(opts); err != nil {
		return nil, err
	}

	return opts, nil
}

func legacyQueryOptions(opts *spot.RecommendationOptions, regions []string) []spot.GetSpotSavingsOption {
	queryOpts := []spot.GetSpotSavingsOption{
		spot.WithRegions(regions),
		spot.WithOS(opts.OS),
		spot.WithCPU(opts.CPU),
		spot.WithMemory(opts.Memory),
	}
	if opts.Instance != "" {
		queryOpts = append(queryOpts, spot.WithPattern(opts.Instance))
	}
	if opts.Budget > 0 {
		queryOpts = append(queryOpts, spot.WithMaxPrice(opts.Budget))
	}

	return queryOpts
}

// execNeutralRecommendV2 answers with the provider-neutral engine and the
// spotinfo.recommend/v2 report.
func execNeutralRecommendV2(ctx *cli.Context, execCtx context.Context, provider cloud.Provider,
	workload cloud.Workload, outputFormat string, output io.Writer,
) error {
	request, err := neutralRecommendRequest(ctx, provider.ID(), workload)
	if err != nil {
		return err
	}

	report, err := cloud.Recommend(execCtx, provider, request)
	if err != nil {
		return err
	}

	if outputFormat == outputTable {
		return writeNeutralRecommendationTable(report.Recommendations, output)
	}

	return writeJSONReport(report, output)
}

// neutralRecommendRequest maps CLI flags onto the neutral request. Values
// outside the neutral vocabulary are rejected here; bounds and combinations are
// validated by the request itself, before any provider is queried.
func neutralRecommendRequest(ctx *cli.Context, id cloud.ProviderID, workload cloud.Workload) (*cloud.RecommendRequest, error) {
	architecture, err := cloud.ParseArchitecture(ctx.String(flagArchitecture))
	if err != nil {
		return nil, err
	}

	instanceOS, err := cloud.ParseOperatingSystem(lineageString(ctx, flagOS))
	if err != nil {
		return nil, err
	}

	budget := ctx.Float64(flagBudget)
	if ctx.IsSet(flagBudget) && !validBudget(budget) {
		return nil, fmt.Errorf("%w: budget must be a positive USD machine-hour price", cloud.ErrInvalidArgument)
	}

	// IsSet, not a zero check: an explicit --top 0 must reach the request
	// validator and be rejected, exactly as it is on the v1 path, rather than
	// being read as "unset" and silently answered with the default.
	top := cloud.DefaultTop
	if ctx.IsSet(flagTop) {
		top = ctx.Int(flagTop)
	}

	request := &cloud.RecommendRequest{
		Cloud:        id,
		Machine:      machineFilter(ctx),
		Architecture: architecture,
		OS:           instanceOS,
		Workload:     workload,
		Regions:      requestedRegions(ctx, id),
		MinMemoryGiB: float64(lineageInt(ctx, flagMemory, "memory-gib")),
		MinVCPU:      lineageInt(ctx, flagCPU, "vcpu"),
		Top:          top,
	}
	if budget > 0 {
		// A ceiling, not a measured price: --budget is routinely a monthly figure
		// divided by 720, which carries more fractional digits than the scale.
		// Truncating can only tighten it, where MoneyFromFloat would reject the
		// request outright — and the v1 path already accepts that same value.
		ceiling, err := cloud.MoneyCeilingFromFloat(budget)
		if err != nil {
			return nil, err
		}
		request.MaxPrice = &ceiling
	}

	return request, nil
}

// requestedRegions resolves --region for the neutral path. The declared default
// is an AWS region name, which no other cloud publishes, so an unset --region on
// another provider means every region that provider publishes rather than an
// AWS region it will never match. An explicit --region is always honoured.
func requestedRegions(ctx *cli.Context, id cloud.ProviderID) []cloud.Region {
	if id != cloud.ProviderAWS && !lineageIsSet(ctx, flagRegion) {
		return []cloud.Region{cloud.RegionAll}
	}

	return neutralRegions(lineageStringSlice(ctx, flagRegion))
}

// neutralRegions trims and deduplicates repeated --region flags. It rejects
// nothing: an empty or contradictory region list is the request validator's
// job, so both CLI schemas report it with their own error vocabulary.
func neutralRegions(regions []string) []cloud.Region {
	unique := make([]cloud.Region, 0, len(regions))
	for _, region := range regions {
		trimmed := cloud.Region(strings.TrimSpace(region))
		if !slices.Contains(unique, trimmed) {
			unique = append(unique, trimmed)
		}
	}
	slices.Sort(unique)

	return unique
}

func writeJSONReport(report any, output io.Writer) error {
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("render recommendation JSON: %w", err)
	}
	if _, err := fmt.Fprintln(output, string(encoded)); err != nil {
		return fmt.Errorf("write recommendation output: %w", err)
	}

	return nil
}

// writeRecommendationTable renders the v1 report. Like the v2 renderer below,
// the region and instance columns are sized from the rows: the fixed %-11s and
// %-14s this used to carry were narrower than real values — ap-southeast-3 is
// fourteen characters and m7i-flex.xlarge is fifteen — so any long row pushed
// every later column out of alignment.
func writeRecommendationTable(recommendations []spot.Recommendation, output io.Writer) error {
	regionWidth, instanceWidth := len("REGION"), len("INSTANCE")
	for i := range recommendations {
		regionWidth = max(regionWidth, len(recommendations[i].Region))
		instanceWidth = max(instanceWidth, len(recommendations[i].Instance))
	}

	header := fmt.Sprintf("RANK  %-*s  %-*s  ARCHITECTURE  vCPU  MEMORY GiB  USD/HOUR  SAVINGS  INTERRUPTION  WHY",
		regionWidth, "REGION", instanceWidth, "INSTANCE")
	if _, err := fmt.Fprintln(output, header); err != nil {
		return fmt.Errorf("write recommendation output: %w", err)
	}

	for index, recommendation := range recommendations {
		if _, err := fmt.Fprintf(output, "%4d  %-*s  %-*s  %-12s  %4d  %10.1f  %8.4f  %6d%%  %-12s  %s\n",
			index+1, regionWidth, recommendation.Region, instanceWidth, recommendation.Instance,
			recommendation.Architecture,
			recommendation.VCPU, recommendation.MemoryGiB, recommendation.PriceUSDPerHour,
			recommendation.SavingsPercent, recommendation.InterruptionFrequency,
			strings.Join(recommendation.RationaleCodes, ",")); err != nil {
			return fmt.Errorf("write recommendation output: %w", err)
		}
	}

	return nil
}

// writeNeutralRecommendationTable renders the v2 report. Risk is printed as its
// status when no figure was published, so a cost recommendation never reads as
// an interruption claim.
func writeNeutralRecommendationTable(recommendations []cloud.RecommendationDTO, output io.Writer) error {
	// The region and machine columns are sized from the rows rather than fixed:
	// an Azure size name runs to twenty-odd characters where an AWS instance
	// type takes ten, and a fixed width lets the longer one push every later
	// column out of alignment.
	regionWidth, machineWidth := len("REGION"), len("MACHINE")
	for i := range recommendations {
		regionWidth = max(regionWidth, len(recommendations[i].Region))
		machineWidth = max(machineWidth, len(recommendations[i].Machine))
	}

	header := fmt.Sprintf("RANK  CLOUD  %-*s  %-*s  ARCHITECTURE  vCPU  MEMORY GiB  USD/HOUR    SAVINGS  RISK          WHY",
		regionWidth, "REGION", machineWidth, "MACHINE")
	if _, err := fmt.Fprintln(output, header); err != nil {
		return fmt.Errorf("write recommendation output: %w", err)
	}

	decimals := priceDecimals(recommendations)
	for _, recommendation := range recommendations {
		if _, err := fmt.Fprintf(output, "%4d  %-5s  %-*s  %-*s  %-12s  %4d  %10.1f  %-10s  %7s  %-12s  %s\n",
			recommendation.Rank, recommendation.Cloud,
			regionWidth, recommendation.Region, machineWidth, recommendation.Machine,
			recommendation.Architecture, recommendation.VCPU, recommendation.MemoryGiB,
			humanPrice(recommendation.SpotUSDPerHour, decimals), savingsDisplay(recommendation.SavingsPercent),
			riskDisplay(&recommendation.Risk),
			strings.Join(recommendation.RationaleCodes, ",")); err != nil {
			return fmt.Errorf("write recommendation output: %w", err)
		}
	}

	return nil
}

// savingsDisplay renders the discount against on-demand.
//
// The figure was computed and published in the v2 JSON from the start, but the
// table never had a column for it: a GCP or Azure recommendation showed an
// absolute hourly price where the AWS table showed price *and* savings, so the
// one number people compare clouds on was the one the table dropped. Absent
// savings prints as "-" rather than as 0% — a provider that publishes no
// on-demand price has not measured a discount of nothing.
func savingsDisplay(savings *float64) string {
	if savings == nil {
		return "-"
	}

	return fmt.Sprintf("%.0f%%", *savings)
}

// priceDecimals picks one decimal count for the whole price column: the fewest
// that keeps every amount in the set exact, and never fewer than four.
//
// The v2 wire format is a nine-decimal string and stays that way — the schema
// pins the pattern and a consumer parses it. But a person reading the table saw
// GCP's 0.042496000 beside AWS's 0.0502 in the same column family. Trimming each
// amount independently traded that for a ragged column (0.027894 above 0.02862),
// so the width is decided once, from all the rows.
func priceDecimals(recommendations []cloud.RecommendationDTO) int {
	const minDecimals = 4

	decimals := minDecimals
	for i := range recommendations {
		amount := recommendations[i].SpotUSDPerHour

		dot := strings.IndexByte(amount, '.')
		if dot < 0 {
			continue
		}

		decimals = max(decimals, len(strings.TrimRight(amount, "0"))-dot-1)
	}

	return decimals
}

// humanPrice renders a fixed-point amount at the column's decimal count.
func humanPrice(amount string, decimals int) string {
	dot := strings.IndexByte(amount, '.')
	if dot < 0 || len(amount) < dot+1+decimals {
		return amount
	}

	return amount[:dot+1+decimals]
}

func riskDisplay(risk *cloud.RiskDTO) string {
	if risk.Label != nil && *risk.Label != "" {
		return *risk.Label
	}

	return string(risk.Status)
}
