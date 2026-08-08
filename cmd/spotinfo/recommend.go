package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	outputFormat := lineageString(ctx, flagOutput)
	if outputFormat != outputTable && outputFormat != outputJSON {
		return fmt.Errorf("%w: output must be table or json", spot.ErrInvalidRecommendationInput)
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

// legacyRecommendationOptions builds and validates the v1 constraint set.
func legacyRecommendationOptions(ctx *cli.Context, workload cloud.Workload) (*spot.RecommendationOptions, error) {
	budget := ctx.Float64(flagBudget)
	if ctx.IsSet(flagBudget) && budget <= 0 {
		return nil, fmt.Errorf("%w: budget must be a positive USD instance-hour price", spot.ErrInvalidRecommendationInput)
	}
	if ctx.IsSet(flagTop) && ctx.Int(flagTop) <= 0 {
		return nil, fmt.Errorf("%w: top must be positive", spot.ErrInvalidRecommendationInput)
	}

	opts := &spot.RecommendationOptions{
		Architecture: spot.Architecture(ctx.String(flagArchitecture)),
		Instance:     ctx.String(flagInstance),
		OS:           lineageString(ctx, flagOS),
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
	if ctx.IsSet(flagBudget) && budget <= 0 {
		return nil, fmt.Errorf("%w: budget must be a positive USD machine-hour price", cloud.ErrInvalidArgument)
	}

	top := ctx.Int(flagTop)
	if top == 0 {
		top = cloud.DefaultTop
	}

	request := &cloud.RecommendRequest{
		Cloud:        id,
		Machine:      ctx.String(flagInstance),
		Architecture: architecture,
		OS:           instanceOS,
		Workload:     workload,
		Regions:      neutralRegions(lineageStringSlice(ctx, flagRegion)),
		MinMemoryGiB: float64(lineageInt(ctx, flagMemory, "memory-gib")),
		MinVCPU:      lineageInt(ctx, flagCPU, "vcpu"),
		Top:          top,
	}
	if budget > 0 {
		ceiling, err := cloud.MoneyFromFloat(budget)
		if err != nil {
			return nil, err
		}
		request.MaxPrice = &ceiling
	}

	return request, nil
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

func writeRecommendationTable(recommendations []spot.Recommendation, output io.Writer) error {
	if _, err := fmt.Fprintln(output, "RANK  REGION       INSTANCE       ARCHITECTURE  vCPU  MEMORY GiB  USD/HOUR  SAVINGS  INTERRUPTION  WHY"); err != nil {
		return fmt.Errorf("write recommendation output: %w", err)
	}
	for index, recommendation := range recommendations {
		if _, err := fmt.Fprintf(output, "%4d  %-11s %-14s %-12s %4d  %10.1f  %8.4f  %6d%%  %-12s  %s\n",
			index+1, recommendation.Region, recommendation.Instance, recommendation.Architecture,
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
	if _, err := fmt.Fprintln(output, "RANK  CLOUD  REGION       MACHINE        ARCHITECTURE  vCPU  MEMORY GiB  USD/HOUR       RISK          WHY"); err != nil {
		return fmt.Errorf("write recommendation output: %w", err)
	}
	for _, recommendation := range recommendations {
		if _, err := fmt.Fprintf(output, "%4d  %-5s  %-11s  %-14s %-12s %4d  %10.1f  %-13s  %-12s  %s\n",
			recommendation.Rank, recommendation.Cloud, recommendation.Region, recommendation.Machine,
			recommendation.Architecture, recommendation.VCPU, recommendation.MemoryGiB,
			recommendation.SpotUSDPerHour, riskDisplay(&recommendation.Risk),
			strings.Join(recommendation.RationaleCodes, ",")); err != nil {
			return fmt.Errorf("write recommendation output: %w", err)
		}
	}

	return nil
}

func riskDisplay(risk *cloud.RiskDTO) string {
	if risk.Label != nil && *risk.Label != "" {
		return *risk.Label
	}

	return string(risk.Status)
}
