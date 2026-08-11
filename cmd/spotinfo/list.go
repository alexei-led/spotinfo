package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"math"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/urfave/cli/v2"

	"spotinfo/internal/cloud"
	awsprovider "spotinfo/internal/providers/aws"
	"spotinfo/internal/spot"
)

// `spotinfo list` browses: it filters a cloud's catalogue and renders every
// match. It requires nothing, which is the whole difference from `recommend` —
// that command requires an architecture and a size floor and answers "what
// should I run", where this one answers "what is there".
//
// It is cloud-neutral. The command it replaces acquired AWS advice and rendered
// an AWS interruption bucket, and told a caller who asked for another cloud to
// run a different command. This one acquires through cloud.Provider and renders
// the neutral risk observation, so a cloud that publishes no risk figure prints
// its status rather than a blank cell or a zero.

// listCommandName is the browse command's name.
const listCommandName = "list"

// outputFormats is the --output vocabulary, shared by the flag validator and
// the renderer switch. All five belong to list: it is the command that renders
// a page a person reads.
var outputFormats = []string{outputNumber, outputText, outputJSON, outputTable, outputCSV}

// sortByNames maps the --sort vocabulary onto the neutral sort keys. Every name
// is the neutral field's own name, so the same word means the same thing on
// both commands and on every cloud. The provider translates them back into its
// own ordering, so a cloud that already sorts its own data keeps deciding what
// each key means for it.
var sortByNames = map[string]cloud.SortKey{
	sortMachine: cloud.SortByMachine,
	sortRisk:    cloud.SortByRisk,
	sortSavings: cloud.SortBySavings,
	sortPrice:   cloud.SortByPrice,
	sortRegion:  cloud.SortByRegion,
	sortScore:   cloud.SortByPlacementScore,
}

// sortKeyNames lists the --sort vocabulary in a stable order, for help and
// error text.
func sortKeyNames() []string { return slices.Sorted(maps.Keys(sortByNames)) }

// parseSortBy resolves the --sort vocabulary. An unset key is not an error: it
// leaves the order to the provider, which is the only honest default across
// clouds that publish different observations.
//
// An unrecognised key used to fall through to the interruption sort, silently
// and with an exit code of 0.
func parseSortBy(value string) (cloud.SortKey, error) {
	if value == "" {
		return "", nil
	}
	if sortBy, ok := sortByNames[value]; ok {
		return sortBy, nil
	}

	return "", fmt.Errorf("%w: unknown sort %q, want one of %s",
		cloud.ErrInvalidArgument, value, strings.Join(sortKeyNames(), "|"))
}

// listCommand declares `spotinfo list` with the vocabulary flags marked for it.
//
//nolint:funlen // CLI flag declarations are intentionally kept together.
func listCommand(action cli.ActionFunc) *cli.Command {
	return &cli.Command{
		Name:         listCommandName,
		Usage:        "list every matching machine with its price and risk",
		Action:       action,
		OnUsageError: renameHint,
		Flags: []cli.Flag{
			cloudFlag(),
			&cli.StringFlag{
				Name:  flagMachine,
				Usage: "filter: machine type RE2 regexp pattern",
			},
			&cli.StringFlag{
				Name:  flagArchitecture,
				Usage: "filter: processor architecture x86_64|arm64",
			},
			&cli.StringFlag{
				Name:  flagOS,
				Usage: osFlagUsage,
				Value: defaultOS,
			},
			&cli.StringSliceFlag{
				Name:  flagRegion,
				Usage: regionFlagUsage,
				Value: cli.NewStringSlice(defaultRegion),
			},
			&cli.StringFlag{
				Name:  flagOutput,
				Usage: "format output: " + outputFormatUsage,
				Value: defaultOutput,
			},
			// These filters are off when unset, and urfave/cli renders that as
			// "(default: 0)" — which reads as a ceiling of zero dollars or a
			// floor of zero cores rather than as "not filtering".
			&cli.IntFlag{
				Name:        flagMinVCPU,
				Usage:       "filter: minimum vCPU cores",
				DefaultText: unfilteredDefaultText,
			},
			&cli.IntFlag{
				Name:        flagMinMemoryGiB,
				Usage:       "filter: minimum memory GiB",
				DefaultText: unfilteredDefaultText,
			},
			&cli.Float64Flag{
				Name:        flagMaxPrice,
				Usage:       "filter: positive maximum USD per machine-hour",
				DefaultText: unfilteredDefaultText,
			},
			&cli.StringFlag{
				Name:        flagSort,
				Usage:       "sort results by " + strings.Join(sortKeyNames(), "|"),
				DefaultText: "the order the cloud publishes",
			},
			&cli.StringFlag{
				Name:        flagOrder,
				Usage:       "sort order " + orderAsc + "|" + orderDesc,
				DefaultText: orderAsc,
			},
			&cli.BoolFlag{
				Name:  flagWithScore,
				Usage: "include spot placement scores (experimental)",
			},
			&cli.IntFlag{
				Name:        flagMinScore,
				Usage:       "filter: minimum spot placement score (1-10, needs --" + flagWithScore + ")",
				DefaultText: unfilteredDefaultText,
			},
			&cli.BoolFlag{
				Name:  flagAZ,
				Usage: "request zone-level scores instead of region-level (use with --" + flagWithScore + ")",
			},
			&cli.IntFlag{
				Name:  flagScoreTimeout,
				Usage: "timeout for score enrichment in seconds",
				Value: spot.DefaultScoreTimeoutSeconds,
			},
			&cli.BoolFlag{
				Name:  flagOffline,
				Usage: offlineFlagUsage,
			},
			&cli.BoolFlag{
				Name:  flagRefresh,
				Usage: refreshFlagUsage,
			},
			gcpProjectFlag(),
		},
	}
}

// listCmd is the production action: it opens the acquisition client this
// invocation needs and answers from it.
func listCmd(ctx *cli.Context) error {
	client := newSpotClient(ctx)

	registry, err := newProviderRegistry(client)
	if err != nil {
		return err
	}

	return execListCmd(ctx, mainCtx, registry, client, os.Stdout)
}

// execListCmd is the testable version of listCmd that accepts dependencies.
func execListCmd(ctx *cli.Context, execCtx context.Context,
	registry providerRegistry, client spotClient, output io.Writer,
) error {
	if v := execCtx.Value("key"); v != nil {
		log.Debug("context value received", slog.Any("value", v))
	}

	// An unrecognised --sort used to fall through to the interruption sort and
	// an unrecognised --output to the number format, both silently and both
	// exiting 0. `--output jsonn` printed "59" and reported success, which a
	// script reads as a valid answer in the format it asked for.
	sortKey, err := parseSortBy(ctx.String(flagSort))
	if err != nil {
		return err
	}

	// Provider selection and capability checks run before acquisition, so an
	// unsupported request costs no I/O.
	provider, err := resolveListProvider(ctx, registry, client, listCapabilityRequest(ctx, sortKey))
	if err != nil {
		return err
	}

	if invalid := validateListFlags(ctx); invalid != nil {
		return invalid
	}

	query, err := listQuery(ctx, sortKey)
	if err != nil {
		return err
	}

	result, err := provider.Query(execCtx, query)
	if err != nil {
		return fmt.Errorf("failed to get spot savings: %w", err)
	}
	candidates := result.Candidates

	// decide if region should be printed
	regions := ctx.StringSlice(flagRegion)
	printRegion := len(regions) > 1 || (len(regions) == 1 && regions[0] == allRegions)

	// An empty match used to print a bare table frame and exit 0 with nothing
	// on stderr, so a typo'd machine name was indistinguishable from a real
	// "no such thing here". The formats keep rendering — an empty JSON array
	// stays parseable — and the note goes to stderr so it never contaminates a
	// piped result.
	if len(candidates) == 0 {
		log.Warn("no machines matched the query", slog.Any("filters", describeListFilters(ctx)))
	}

	return renderList(ctx.String(flagOutput), query, &result, printRegion, output)
}

// renderList writes the answer in the requested format.
//
// Only the JSON form carries the request echo and the provenance: it is the
// document a program reads, and the four rendered formats are pages a person
// reads. That is why it takes the whole result rather than the candidates.
func renderList(format string, query *cloud.Query, result *cloud.Result, printRegion bool, output io.Writer) error {
	candidates := result.Candidates

	switch format {
	case outputNumber:
		printAdvicesNumber(candidates, printRegion, output)
	case outputText:
		printAdvicesText(candidates, printRegion, output)
	case outputJSON:
		report, err := cloud.NewListReport(query, result)
		if err != nil {
			return err
		}

		return writeJSONReport(report, output)
	case outputTable:
		printAdvicesTable(candidates, false, printRegion, output)
	case outputCSV:
		printAdvicesTable(candidates, true, printRegion, output)
	default:
		// Unreachable: validateListFlags rejects an unknown format before any
		// acquisition. Kept so a new format added to the flag but not here
		// fails loudly instead of silently printing savings numbers.
		return fmt.Errorf("%w: unsupported output format %q", cloud.ErrInvalidArgument, format)
	}

	return nil
}

// listCapabilityRequest is what `list` renders: spot prices, machine
// specifications, and whatever the flags actually ask for on top.
//
// Risk is deliberately not required. Every cloud is browsable, and one that
// publishes no interruption figure reports that absence in the risk column
// rather than being refused — the column prints a status, never a zero. Sorting
// *by* risk is a different question: that one does need a published figure, so
// the sort key adds the capability it depends on.
func listCapabilityRequest(ctx *cli.Context, sortKey cloud.SortKey) cloud.CapabilityRequest {
	needed := []cloud.Capability{cloud.CapabilitySpotPrice, cloud.CapabilityMachineSpec}

	if sortKey == cloud.SortByRisk {
		needed = append(needed, cloud.CapabilityRisk)
	}
	if ctx.Bool(flagWithScore) || ctx.Int(flagMinScore) > 0 || sortKey == cloud.SortByPlacementScore {
		needed = append(needed, cloud.CapabilityPlacementScore)
	}
	if ctx.Bool(flagAZ) {
		needed = append(needed, cloud.CapabilityZoneDetail)
	}

	return cloud.CapabilityRequest{
		OS:           requestedOS(ctx.String(flagOS)),
		Architecture: requestedArchitecture(ctx.String(flagArchitecture)),
		Needed:       needed,
	}
}

// resolveListProvider picks the provider that answers this request, after
// proving it can answer it.
//
// AWS does not resolve through the registry, and that is deliberate. What AWS
// can answer is a property of the feeds, so the package declaration answers the
// gate and awsQueryProvider then builds the provider from the acquisition
// client alone. Resolving through the registry made this command inherit every
// input that factory reads, so an unreadable architecture manifest failed
// `spotinfo list --machine t3.micro` with SNAPSHOT_UNAVAILABLE while the advisor
// and price data it does read were intact. A genuinely broken advisor or price
// snapshot still fails at acquisition, where the legacy client verifies its own
// payloads against their manifests.
func resolveListProvider(ctx *cli.Context, registry providerRegistry,
	client spotClient, request cloud.CapabilityRequest,
) (cloud.Provider, error) {
	id, err := providerID(ctx)
	if err != nil {
		return nil, err
	}

	if id == cloud.ProviderAWS {
		if capErr := awsprovider.Capabilities().Require(request); capErr != nil {
			return nil, fmt.Errorf("%s: %w", id, capErr)
		}

		return awsQueryProvider(client, request.Architecture != "")
	}

	provider, err := registry.Get(id)
	if err != nil {
		return nil, err
	}
	if capErr := provider.Capabilities().Require(request); capErr != nil {
		return nil, fmt.Errorf("%s: %w", id, capErr)
	}

	return provider, nil
}

// awsQueryProvider builds the neutral provider this command acquires through.
//
// The architecture snapshot is read only when --architecture asks for it. The
// lookup is the one input this command can do without: without it the provider
// declares no architecture, so a request that filters on one would be refused
// rather than answered from an unclassified catalogue — and an unreadable
// snapshot must not fail a query that never mentions an architecture.
func awsQueryProvider(client spotClient, withArchitecture bool) (cloud.Provider, error) {
	if !withArchitecture {
		provider, err := awsprovider.New(client, nil)
		if err != nil {
			return nil, fmt.Errorf("build aws provider: %w", err)
		}

		return provider, nil
	}

	lookup, err := spot.LoadEmbeddedArchitectureLookup()
	if err != nil {
		return nil, fmt.Errorf("--%s needs the aws architecture snapshot: %w", flagArchitecture, err)
	}

	provider, err := awsprovider.New(client, lookup)
	if err != nil {
		return nil, fmt.Errorf("build aws provider: %w", err)
	}

	return provider, nil
}

// listQuery maps the command's flags onto the neutral query. Every filter is
// re-applied by the provider exactly against the mapped candidate.
func listQuery(ctx *cli.Context, sortKey cloud.SortKey) (*cloud.Query, error) {
	requested := ctx.StringSlice(flagRegion)
	regions := make([]cloud.Region, 0, len(requested))
	for _, region := range requested {
		regions = append(regions, cloud.Region(strings.TrimSpace(region)))
	}

	query := &cloud.Query{
		Regions: regions,
		// Folded, not validated. The capability check compares against the
		// lowercase neutral constants exactly, so casting the raw flag refused
		// `--os Linux` and `--os Windows` — spellings the legacy client accepted
		// with EqualFold and the recommend path still folds the same way. An
		// operating system outside the vocabulary survives the fold unchanged and
		// stays the provider's error to report, in its own wording.
		OS:             cloud.OperatingSystem(foldVocabulary(ctx.String(flagOS))),
		MachinePattern: ctx.String(flagMachine),
		MinVCPU:        ctx.Int(flagMinVCPU),
		MinMemoryGiB:   float64(ctx.Int(flagMinMemoryGiB)),
		Sort: cloud.SortOrder{
			Key:        sortKey,
			Descending: strings.EqualFold(ctx.String(flagOrder), orderDesc),
		},
	}

	// Rejected here rather than dropped: requestedArchitecture reads an
	// unparsable value as "no architecture asked for", which would answer an
	// unfiltered list as though it were filtered.
	if value := strings.TrimSpace(ctx.String(flagArchitecture)); value != "" {
		architecture, err := cloud.ParseArchitecture(value)
		if err != nil {
			return nil, err
		}
		query.Architecture = architecture
	}

	if maxPrice := ctx.Float64(flagMaxPrice); maxPrice > 0 {
		// A ceiling, not a measured price. Truncating to the fixed-point scale can
		// only tighten it, where MoneyFromFloat would reject a value the float
		// comparison this replaces accepted.
		ceiling, err := cloud.MoneyCeilingFromFloat(maxPrice)
		if err != nil {
			return nil, err
		}
		query.MaxPrice = &ceiling
	}

	// Scores are fetched under --with-score; --min-score filters whatever scores
	// are present. validateListFlags already rejects the second without the first.
	if ctx.Bool(flagWithScore) {
		query.Placement.Enabled = true
		query.Placement.SingleZone = ctx.Bool(flagAZ)
		if timeout := ctx.Int(flagScoreTimeout); timeout > 0 {
			query.Placement.Timeout = time.Duration(timeout) * time.Second
		}
	}
	if minScore := ctx.Int(flagMinScore); minScore > 0 {
		query.Placement.MinScore = minScore
	}

	return query, nil
}

// validateListFlags rejects input the query would otherwise answer wrongly and
// silently. Each case below produced a plausible-looking result rather than an
// error, which is worse than failing: a wrong answer that exits 0 is acted on.
func validateListFlags(ctx *cli.Context) error {
	if !slices.Contains(outputFormats, ctx.String(flagOutput)) {
		return fmt.Errorf("%w: unknown output format %q, want one of %s",
			cloud.ErrInvalidArgument, ctx.String(flagOutput), strings.Join(outputFormats, "|"))
	}

	if err := validateOrder(ctx.String(flagOrder)); err != nil {
		return err
	}

	// ParseFloat accepts NaN and ±Inf, and neither is caught by the `> 0` test
	// that decides whether to append the filter: NaN fails every comparison and
	// -Inf is not positive, so both drop the ceiling and answer an unfiltered
	// query as if it were filtered. +Inf would be stored raw and compared
	// against in the client, making the filter a silent no-op.
	maxPrice := ctx.Float64(flagMaxPrice)
	if ctx.IsSet(flagMaxPrice) && (math.IsNaN(maxPrice) || math.IsInf(maxPrice, 0) || maxPrice <= 0) {
		return fmt.Errorf("%w: --%s must be a positive USD machine-hour price",
			cloud.ErrInvalidArgument, flagMaxPrice)
	}

	// A negative minimum is not a filter anyone means, and it was accepted
	// while a negative price ceiling was rejected — the same mistake, two
	// different answers.
	if ctx.Int(flagMinVCPU) < 0 {
		return fmt.Errorf("%w: --%s must be zero or a positive number of vCPU cores",
			cloud.ErrInvalidArgument, flagMinVCPU)
	}

	if ctx.Int(flagMinMemoryGiB) < 0 {
		return fmt.Errorf("%w: --%s must be zero or a positive number of GiB",
			cloud.ErrInvalidArgument, flagMinMemoryGiB)
	}

	return validateScoreFloor(ctx.Int(flagMinScore), ctx.Bool(flagWithScore))
}

// validateOrder accepts an unset order as ascending: --sort leaves the order to
// the provider when it is unset, and --order alone reverses whatever that is.
func validateOrder(order string) error {
	if order == "" || strings.EqualFold(order, orderAsc) || strings.EqualFold(order, orderDesc) {
		return nil
	}

	return fmt.Errorf("%w: unknown order %q, want %s or %s",
		cloud.ErrInvalidArgument, order, orderAsc, orderDesc)
}

// validateScoreFloor rejects a minimum score that nothing can satisfy. Scores
// are only fetched under --with-score, so on its own this filter compared every
// candidate against an absent score and dropped all of them: zero bytes on
// stdout, zero on stderr, exit 0.
func validateScoreFloor(minScore int, withScore bool) error {
	if minScore != 0 && !withScore {
		return fmt.Errorf("%w: --%s needs --%s, which is what fetches the placement scores it filters on",
			cloud.ErrInvalidArgument, flagMinScore, flagWithScore)
	}

	if minScore < 0 || minScore > maxPlacementScore {
		return fmt.Errorf("%w: --%s must be between 1 and %d",
			cloud.ErrInvalidArgument, flagMinScore, maxPlacementScore)
	}

	return nil
}

// describeListFilters reports the filters that were actually set, so an empty
// result names what narrowed it rather than repeating the whole flag set.
func describeListFilters(ctx *cli.Context) []string {
	var set []string

	for _, name := range []string{
		flagMachine, flagArchitecture, flagRegion, flagOS,
		flagMinVCPU, flagMinMemoryGiB, flagMaxPrice, flagMinScore,
	} {
		if lineageIsSet(ctx, name) {
			set = append(set, name+"="+lineageContextValue(ctx, name))
		}
	}

	return set
}

func lineageContextValue(ctx *cli.Context, name string) string {
	resolved := flagLineageContext(ctx, name)
	if values := resolved.StringSlice(name); len(values) > 0 {
		return strings.Join(values, ",")
	}

	return resolved.String(name)
}
