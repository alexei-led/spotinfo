package main

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/urfave/cli/v2"

	"spotinfo/internal/cloud"
)

// execRecommendCmd answers one recommendation request.
//
// There is one report. The workload used to pick between two published schemas
// — AWS under web, ci or batch was answered by spotinfo.recommend/v1 and
// everything else by the neutral engine — so the same question returned a
// different document depending on which cloud and which policy answered it.
// Every cloud and every workload now emits spotinfo.recommend/v3.
func execRecommendCmd(ctx *cli.Context, execCtx context.Context, registry providerRegistry, output io.Writer) error {
	if err := requireRecommendFlags(ctx); err != nil {
		return err
	}

	outputFormat := lineageString(ctx, flagOutput)
	if outputFormat != outputTable && outputFormat != outputJSON {
		return fmt.Errorf("%w: output must be table or json", cloud.ErrInvalidArgument)
	}

	// MaxTop is enforced here rather than in the neutral validator, which bounds
	// only the lower end: the bound is written into the v3 payload schema, which
	// pins request.top at a maximum of 50, so an unbounded CLI emitted documents
	// that fail their own published contract.
	if top := ctx.Int(flagTop); top > cloud.MaxTop {
		return fmt.Errorf("%w: top must be between 1 and %d", cloud.ErrInvalidArgument, cloud.MaxTop)
	}

	sortKey, err := parseSortBy(ctx.String(flagSort))
	if err != nil {
		return err
	}
	if invalid := validateOrder(ctx.String(flagOrder)); invalid != nil {
		return invalid
	}

	provider, err := resolveProviderForRecommend(ctx, registry)
	if err != nil {
		return err
	}

	if liveRiskErr := rejectLiveRiskOffGCP(ctx, provider.ID()); liveRiskErr != nil {
		return liveRiskErr
	}

	// Every other flag this cloud cannot act on, refused here rather than
	// accepted and ignored — and before acquisition, so a refusal costs no I/O.
	if refusal := refuseUnsupportedFlags(ctx, provider); refusal != nil {
		return refusal
	}

	// The same score rules `list` applies. Declaring the four flags without
	// this is what would land three of them accepted and silently ignored.
	if invalid := validateScoreFlags(ctx); invalid != nil {
		return invalid
	}
	if invalid := requireScoresToSortByThem(ctx, sortKey); invalid != nil {
		return invalid
	}

	workload, err := recommendWorkload(ctx)
	if err != nil {
		return err
	}

	return execRecommend(ctx, execCtx, provider, workload,
		cloud.SortOrder{Key: sortKey, Descending: strings.EqualFold(ctx.String(flagOrder), orderDesc)},
		outputFormat, output)
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
	for _, name := range []string{flagArchitecture, flagMinVCPU, flagMinMemoryGiB} {
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

// recommendWorkload resolves the policy.
//
// The default is cost on every cloud, and the same value over MCP. It used to
// depend on the provider — web where risk was published, cost where it was not —
// which meant the same question returned a different document depending on which
// cloud answered it, and a different one again on the other surface. cost is
// also the only policy every cloud can serve honestly: an interruption ceiling
// is a claim a provider without risk data cannot make.
func recommendWorkload(ctx *cli.Context) (cloud.Workload, error) {
	value := strings.TrimSpace(lineageString(ctx, flagWorkload))
	if value == "" {
		return cloud.WorkloadCost, nil
	}

	return cloud.ParseWorkload(value)
}

// foldVocabulary normalises a fixed-vocabulary flag value the way the neutral
// parsers in internal/cloud do, so the same spelling is accepted on both
// commands. It normalises only; an unrecognised value is still rejected
// downstream, with that path's own error type.
func foldVocabulary(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// validCeiling reports whether a --max-price value can act as a price ceiling.
//
// Both non-finite values have to be named explicitly. NaN fails every
// comparison, so `ceiling <= 0` alone lets it through as a set-but-unenforceable
// ceiling; +Inf passes that check too and would be stored raw as a ceiling no
// price can exceed, making the filter a silent no-op. The same three terms guard
// the flag on `list` — see validateListFlags.
func validCeiling(ceiling float64) bool {
	return !math.IsNaN(ceiling) && !math.IsInf(ceiling, 0) && ceiling > 0
}

// sortRecommendations orders the ranked page for presentation.
//
// Selection is not affected: which candidates appear is decided by the canonical
// ranking policy, published in ranking_policy, and Rank keeps naming each row's
// position in it. A row labelled rank 3 printed first is honest; renumbering
// would be a claim about the policy that is not true. The sort is applied here
// rather than through Query.Sort because the recommender re-sorts every
// candidate by that policy, which would make the key a silent no-op.
//
// An unset key leaves the policy order alone.
func sortRecommendations(recommendations []cloud.RecommendationDTO, order cloud.SortOrder) error {
	if order.Key == "" {
		if order.Descending {
			slices.Reverse(recommendations)
		}

		return nil
	}

	compare, err := recommendationComparator(order.Key)
	if err != nil {
		return err
	}

	slices.SortStableFunc(recommendations, func(left, right cloud.RecommendationDTO) int {
		if order.Descending {
			return compare(&right, &left)
		}

		return compare(&left, &right)
	})

	return nil
}

// requireScoresToSortByThem refuses `--sort score` on a page that fetched none.
//
// The ranked page publishes a placement figure now, so the key is no longer
// meaningless — but only under --with-score. Without it every row's figure is
// absent, compareOptionalFloat sorts them all equal, and the flag exits 0
// having done nothing. It is stated as a companion rule rather than as an
// unsupported key so the message says what to add.
//
// It is a recommend-only rule: `list` reaches its placement capability through
// listCapabilityRequest, which already treats a score sort as a score request.
func requireScoresToSortByThem(ctx *cli.Context, key cloud.SortKey) error {
	if key != cloud.SortByPlacementScore || lineageBool(ctx, flagWithScore) {
		return nil
	}

	return fmt.Errorf("%w: --%s %s needs --%s, which is what fetches the placement figures it orders by",
		cloud.ErrInvalidArgument, flagSort, sortScore, flagWithScore)
}

// recommendationComparator resolves a neutral sort key against the fields a
// recommendation publishes.
func recommendationComparator(key cloud.SortKey) (func(left, right *cloud.RecommendationDTO) int, error) {
	switch key {
	case cloud.SortByMachine:
		return func(left, right *cloud.RecommendationDTO) int {
			return cmp.Compare(left.Machine, right.Machine)
		}, nil
	case cloud.SortByRegion:
		return func(left, right *cloud.RecommendationDTO) int {
			return cmp.Compare(left.Region, right.Region)
		}, nil
	case cloud.SortByPrice:
		return comparePrices, nil
	case cloud.SortBySavings:
		return func(left, right *cloud.RecommendationDTO) int {
			return compareOptionalFloat(left.SavingsPercent, right.SavingsPercent)
		}, nil
	case cloud.SortByRisk:
		return func(left, right *cloud.RecommendationDTO) int {
			return compareOptionalFloat(left.Risk.MaxPercent, right.Risk.MaxPercent)
		}, nil
	case cloud.SortByPlacementScore:
		return comparePlacements, nil
	default:
		return nil, fmt.Errorf("%w: unknown sort %q", cloud.ErrInvalidArgument, key)
	}
}

// comparePrices orders the fixed-point wire amounts numerically. They are
// nine-decimal strings, so a lexicographic comparison would sort 10.000000000
// below 2.000000000.
//
// The shared candidate block types the amount as nullable because `list`
// publishes rows AWS has no price for. A recommendation never carries one —
// accepts() drops a price-less candidate before ranking — so an absent amount
// is treated the same as an unparsable one rather than given an order.
func comparePrices(left, right *cloud.RecommendationDTO) int {
	if left.SpotUSDPerHour == nil || right.SpotUSDPerHour == nil {
		return 0
	}

	leftAmount, leftErr := cloud.ParseMoney(*left.SpotUSDPerHour)
	rightAmount, rightErr := cloud.ParseMoney(*right.SpotUSDPerHour)
	if leftErr != nil || rightErr != nil {
		return 0
	}

	return cmp.Compare(leftAmount.Nanos(), rightAmount.Nanos())
}

// comparePlacements orders the ranked page by its regional placement figure.
//
// The branch is chosen by the kind the page carries, and each is compared on
// its own scale. A page cannot mix kinds — one answer comes from one cloud —
// which is what makes comparing within a branch safe; there is deliberately no
// conversion between them, because a shared number is one no vendor published.
//
// Only the regional figure orders a page. Under --az a candidate carries one
// observation per zone and no regional one, and picking a maximum or a mean
// across zones would be this tool inventing a regional figure the provider
// declined to give — so those rows keep the ranking policy's own order.
func comparePlacements(left, right *cloud.RecommendationDTO) int {
	if left.RegionScore != nil || right.RegionScore != nil {
		return compareOptionalInt(left.RegionScore, right.RegionScore)
	}

	return compareOptionalFloat(left.RegionObtainability, right.RegionObtainability)
}

// compareOptionalInt orders an integer figure a provider may not publish,
// putting an absent one last under either order — see compareOptionalFloat.
func compareOptionalInt(left, right *int) int {
	switch {
	case left == nil && right == nil:
		return 0
	case left == nil:
		return 1
	case right == nil:
		return -1
	default:
		return cmp.Compare(*left, *right)
	}
}

// compareOptionalFloat orders a figure a provider may not publish. An absent
// value sorts last under either order, because "not measured" is not a position
// on the scale.
func compareOptionalFloat(left, right *float64) int {
	switch {
	case left == nil && right == nil:
		return 0
	case left == nil:
		return 1
	case right == nil:
		return -1
	default:
		return cmp.Compare(*left, *right)
	}
}

// execRecommend answers with the provider-neutral engine and the
// spotinfo.recommend/v3 report.
func execRecommend(ctx *cli.Context, execCtx context.Context, provider cloud.Provider,
	workload cloud.Workload, order cloud.SortOrder, outputFormat string, output io.Writer,
) error {
	request, err := neutralRecommendRequest(ctx, provider.ID(), workload)
	if err != nil {
		return err
	}

	provider, err = withLiveRisk(ctx, provider, request)
	if err != nil {
		return err
	}

	report, err := cloud.Recommend(execCtx, provider, request)
	if err != nil {
		return err
	}

	if err := sortRecommendations(report.Recommendations, order); err != nil {
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

	budget := ctx.Float64(flagMaxPrice)
	if ctx.IsSet(flagMaxPrice) && !validCeiling(budget) {
		return nil, fmt.Errorf("%w: --%s must be a positive USD machine-hour price",
			cloud.ErrInvalidArgument, flagMaxPrice)
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
		Regions:      neutralRegions(lineageStringSlice(ctx, flagRegion)),
		MinMemoryGiB: float64(lineageInt(ctx, flagMinMemoryGiB)),
		MinVCPU:      lineageInt(ctx, flagMinVCPU),
		Top:          top,
		// Carried on the request, not applied afterwards: RecommendRequest
		// derives its capability needs from the Query this becomes, so a cloud
		// that publishes no placement figure is refused before acquisition
		// rather than answering with an empty column.
		Placement: placementRequest(ctx),
	}
	if budget > 0 {
		// A ceiling, not a measured price: --max-price is routinely a monthly figure
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

// writeNeutralRecommendationTable renders the ranked page. Risk is printed as
// its status when no figure was published, so a cost recommendation never reads
// as an interruption claim.
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

	placement := placementColumn(recommendations)

	header := fmt.Sprintf("RANK  CLOUD  %-*s  %-*s  ARCHITECTURE  vCPU  MEMORY GiB  USD/HOUR    SAVINGS  RISK          %sWHY",
		regionWidth, "REGION", machineWidth, "MACHINE", placement.cell(placement.title))
	if _, err := fmt.Fprintln(output, header); err != nil {
		return fmt.Errorf("write recommendation output: %w", err)
	}

	decimals := priceDecimals(recommendations)
	for i, recommendation := range recommendations {
		if _, err := fmt.Fprintf(output, "%4d  %-5s  %-*s  %-*s  %-12s  %4d  %10.1f  %-10s  %7s  %-12s  %s%s\n",
			recommendation.Rank, recommendation.Cloud,
			regionWidth, recommendation.Region, machineWidth, recommendation.Machine,
			recommendation.Architecture, recommendation.VCPU, recommendation.MemoryGiB,
			humanPrice(recommendation.SpotUSDPerHour, decimals), savingsDisplay(recommendation.SavingsPercent),
			riskDisplay(&recommendation.Risk),
			placement.cell(placement.cells[i]),
			strings.Join(recommendation.RationaleCodes, ",")); err != nil {
			return fmt.Errorf("write recommendation output: %w", err)
		}
	}

	return nil
}

// rankedPlacements is the ranked page's placement column, or nothing at all.
//
// A page that asked for no placement figure has no column, which is what keeps
// a recommendation that asked for none rendering exactly as it always has. The
// values are the undecorated ones: this table is hand-padded, and an emoji is
// two columns wide in some terminals and one in others.
type rankedPlacements struct {
	title string
	cells []string
	width int
}

// cell renders one value at the column's width, or nothing when the page has no
// placement column at all.
func (p rankedPlacements) cell(value string) string {
	if p.width == 0 {
		return ""
	}

	return fmt.Sprintf("%-*s  ", p.width, value)
}

func placementColumn(recommendations []cloud.RecommendationDTO) rankedPlacements {
	column := rankedPlacements{cells: make([]string, len(recommendations))}

	var kind cloud.PlacementKind

	present := false

	for i := range recommendations {
		published := &recommendations[i].PlacementDTO
		column.cells[i] = rankedPlacementCell(published)

		if column.cells[i] != absentValue {
			present = true
		}
		if kind == "" {
			kind = rankedPlacementKind(published)
		}
	}

	if !present {
		return rankedPlacements{cells: column.cells}
	}

	column.title = strings.ToUpper(determineScoreHeader(scoreTypeInfo{kind: kind, hasScores: true}))
	column.width = len(column.title)

	for _, cell := range column.cells {
		column.width = max(column.width, len(cell))
	}

	return column
}

// rankedPlacementKind names the measurement a published row carries, so the
// column can be titled after it rather than after whichever kind came first.
func rankedPlacementKind(published *cloud.PlacementDTO) cloud.PlacementKind {
	switch {
	case published.RegionScore != nil || len(published.ZoneScores) > 0:
		return cloud.PlacementKindPlacementScore
	case published.RegionObtainability != nil || len(published.ZoneObtainability) > 0:
		return cloud.PlacementKindObtainability
	default:
		return ""
	}
}

// rankedPlacementCell renders one row's placement figure, on its own kind's
// scale and never converted onto another's.
func rankedPlacementCell(published *cloud.PlacementDTO) string {
	switch {
	case published.RegionScore != nil:
		return strconv.Itoa(*published.RegionScore)
	case published.RegionObtainability != nil:
		return strconv.FormatFloat(*published.RegionObtainability, 'f', obtainabilityDecimals, 64)
	case len(published.ZoneScores) > 0:
		return zonedCells(published.ZoneScores, strconv.Itoa)
	case len(published.ZoneObtainability) > 0:
		return zonedCells(published.ZoneObtainability, func(value float64) string {
			return strconv.FormatFloat(value, 'f', obtainabilityDecimals, 64)
		})
	case published.PlacementStatus == cloud.PlacementStatusUnavailable:
		return string(cloud.PlacementStatusUnavailable)
	default:
		return absentValue
	}
}

// zonedCells renders a zone-keyed figure in zone order, matching what `list`
// prints for the same observation.
func zonedCells[V any](values map[string]V, render func(V) string) string {
	cells := make([]string, 0, len(values))
	for _, zone := range slices.Sorted(maps.Keys(values)) {
		cells = append(cells, zone+":"+render(values[zone]))
	}

	return strings.Join(cells, ",")
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
// The wire format is a nine-decimal string and stays that way — the schema
// pins the pattern and a consumer parses it. But a person reading the table saw
// GCP's 0.042496000 beside AWS's 0.0502 in the same column family. Trimming each
// amount independently traded that for a ragged column (0.027894 above 0.02862),
// so the width is decided once, from all the rows.
func priceDecimals(recommendations []cloud.RecommendationDTO) int {
	const minDecimals = 4

	decimals := minDecimals
	for i := range recommendations {
		if recommendations[i].SpotUSDPerHour == nil {
			continue
		}
		amount := *recommendations[i].SpotUSDPerHour

		dot := strings.IndexByte(amount, '.')
		if dot < 0 {
			continue
		}

		decimals = max(decimals, len(strings.TrimRight(amount, "0"))-dot-1)
	}

	return decimals
}

// humanPrice renders a fixed-point amount at the column's decimal count. An
// absent amount prints as "-", the way savingsDisplay prints an absent
// discount: a price nobody published is not a price of zero.
func humanPrice(amount *string, decimals int) string {
	if amount == nil {
		return "-"
	}

	dot := strings.IndexByte(*amount, '.')
	if dot < 0 || len(*amount) < dot+1+decimals {
		return *amount
	}

	return (*amount)[:dot+1+decimals]
}

func riskDisplay(risk *cloud.RiskDTO) string {
	if risk.Label != nil && *risk.Label != "" {
		return *risk.Label
	}

	return string(risk.Status)
}
