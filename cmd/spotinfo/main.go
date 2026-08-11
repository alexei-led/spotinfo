// Package main provides the CLI application for spotinfo.
package main

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/jedib0t/go-pretty/v6/text"
	"github.com/urfave/cli/v2"

	"spotinfo/internal/cloud"
	"spotinfo/internal/mcp"
	"spotinfo/internal/providers"
	awsprovider "spotinfo/internal/providers/aws"
	azureprovider "spotinfo/internal/providers/azure"
	gcpprovider "spotinfo/internal/providers/gcp"
	"spotinfo/internal/spot"
)

var (
	// main context
	mainCtx context.Context
	// logger instance
	log *slog.Logger
	// structuredLogging records whether the caller asked for machine-readable
	// logs, which decides how a fatal error is rendered. Set in the app's Before
	// hook, read once in reportFatal.
	structuredLogging bool
	// Version contains the current version.
	Version = "dev"
	// BuildDate contains a string with the build date.
	BuildDate = unknownBuildValue
	// GitCommit git commit SHA
	GitCommit = "dirty"
	// GitBranch git branch
	GitBranch = "master"
	// GitHubRelease indicates if this is a GitHub release build
	GitHubRelease = ""
)

const (
	// Table column headers. `machine` and `risk` are the vocabulary words: the
	// command is cloud-neutral now, so the column carries a neutral risk
	// observation rather than the AWS Spot Advisor's "frequency of interruption",
	// and the machine-type column is named after the --machine filter that
	// selects it.
	regionColumn        = "Region"
	machineColumn       = "Machine"
	vCPUColumn          = "vCPU"
	memoryColumn        = "Memory GiB"
	savingsColumn       = "Savings over On-Demand"
	riskColumn          = "Risk"
	priceColumn         = "USD/Hour"
	priceSourceColumn   = "Price Source"
	scoreColumn         = "Score"
	scoreHeaderAZ       = "Placement Score (AZ)"
	scoreHeaderRegional = "Placement Score (Regional)"
	scoreHeaderGeneric  = "Placement Score"
	// The obtainability column names GCP's own measurement. It is a separate
	// set of headers rather than a reuse of the three above because the two
	// figures are on different scales — an integer 1-10 against a 0.0-1.0
	// probability — and one column name for both would tell a reader they are
	// the same number.
	obtainabilityHeaderAZ       = "Obtainability (AZ)"
	obtainabilityHeaderRegional = "Obtainability (Regional)"
	obtainabilityHeaderGeneric  = "Obtainability"

	// obtainabilityDecimals is how many fractional digits a probability is
	// printed to. Google publishes obtainability as a fraction; two digits is
	// the precision a person compares regions on, and the JSON form carries the
	// unrounded value for anything that needs more.
	obtainabilityDecimals = 2

	// absentValue is what every rendered format prints for an observation the
	// cloud did not publish. It is the same "-" the ranked page already prints
	// for an absent price or savings figure, so one glyph means one thing across
	// both commands.
	absentValue = "-"

	// Sort keys. The vocabulary itself lives in internal/cloud so `--sort` and
	// the MCP `sort` argument cannot accept different words; these names are
	// spelled from the neutral keys rather than beside them. "score" is the one
	// word that is not its field's name — the field is placement_score.
	sortMachine = string(cloud.SortByMachine)
	sortRisk    = string(cloud.SortByRisk)
	sortSavings = string(cloud.SortBySavings)
	sortPrice   = string(cloud.SortByPrice)
	sortRegion  = string(cloud.SortByRegion)
	sortScore   = "score"

	// Score thresholds
	excellentScoreThreshold = 8 // Scores 8-10 are excellent
	moderateScoreThreshold  = 5 // Scores 5-7 are moderate
	poorScoreThreshold      = 1 // Scores 1-4 are poor
	// maxPlacementScore is the top of the placement-score scale, which the
	// --min-score usage text already published as 1-10. Read from the neutral
	// domain so the CLI flag and the MCP argument cannot advertise two bounds.
	maxPlacementScore = cloud.MaxPlacementScore

	// Build constants
	unknownBuildValue = "unknown"

	// MCP mode constants
	mcpModeEnv      = "SPOTINFO_MODE"
	mcpTransportEnv = "MCP_TRANSPORT"
	mcpPortEnv      = "MCP_PORT"
	mcpModeValue    = "mcp"
	stdioTransport  = "stdio"
	sseTransport    = "sse"
	defaultMCPPort  = "8080"

	// Output formats
	outputNumber = "number"
	outputText   = "text"
	outputJSON   = "json"
	outputTable  = "table"
	outputCSV    = "csv"

	// Mode and logging flags. They select how the binary runs rather than what
	// it is asked, so they are not vocabulary entries — see vocabulary.go, which
	// declares every flag that describes a query.
	flagMCP     = "mcp"
	flagDebug   = "debug"
	flagQuiet   = "quiet"
	flagJSONLog = "json-log"

	// Sort order values, read from the neutral domain: one spelling for the
	// flag, the tool argument and the published request echo.
	orderAsc  = cloud.OrderAsc
	orderDesc = cloud.OrderDesc

	// allRegions is the --region value selecting every published region.
	allRegions = string(cloud.RegionAll)

	// appName is the CLI application name.
	appName = "spotinfo"

	// requiredFlagDefaultText replaces the "(default: 0)" urfave/cli prints for
	// an int flag with no value, on the three recommend flags that have no
	// default because they must be given.
	requiredFlagDefaultText = "none, this flag is required"

	// unfilteredDefaultText replaces the same "(default: 0)" on the optional
	// numeric filters, where zero means "do not filter" rather than a ceiling
	// of zero dollars or a floor of zero cores.
	unfilteredDefaultText = "no filter"

	recommendCommandName = "recommend"
	refreshFlagUsage     = "ignore any cached provider feed and fetch it again"
	offlineFlagUsage     = "answer from the embedded snapshot instead of fetching the live feeds"
	outputFormatUsage    = "number|text|json|table|csv"
	osFlagUsage          = "machine operating system: linux|windows"

	// regionFlagUsage documents the default and its cost. Every published
	// region is what makes the same question return the same document on both
	// surfaces, but on AWS it also means the live-price fallback can fire in
	// every region, so the fast alternatives are named here rather than left
	// for a reader to discover from a slow run.
	regionFlagUsage = "one or more provider regions, or \"all\" for every published region; " +
		"\"all\" on AWS queries every region, so pass --offline or an explicit --region when speed matters"
)

// rootCmd is what bare `spotinfo` does. The query moved to `spotinfo list`, so
// the root command only selects a mode: --mcp serves the MCP server (urfave/cli
// answers --version before this runs), and anything else is a caller who has not
// said which question they are asking. Help plus a non-zero exit says so; a
// silent zero-exit help page reads as success to a script.
func rootCmd(ctx *cli.Context) error {
	// Checked before anything else: --mcp has always been dispatched from here,
	// and it is the one bare invocation that means something.
	if isMCPMode(ctx) {
		return runMCPServer(mainCtx)
	}

	_ = cli.ShowAppHelp(ctx)

	return fmt.Errorf("%w: no command given; run %q to browse or %q to rank",
		cloud.ErrInvalidArgument,
		appName+" "+listCommandName, appName+" "+recommendCommandName)
}

// renameHint answers a retired flag name with the one that replaced it.
//
// Two things are wrong with the default usage-error path for this. It prints
// "Incorrect Usage" and the whole help page to stdout before returning, so
// `spotinfo list --type m5.large > out.json` writes a help page into the file
// it was asked to fill; and "flag provided but not defined: -type" tells a
// caller their spelling is wrong without saying that the concept still exists
// under one name, which is the whole point of collapsing the vocabulary.
//
// Anything that is not a retired name is returned unchanged, so a genuinely
// unknown flag still fails as one.
func renameHint(_ *cli.Context, err error, _ bool) error {
	name, ok := undefinedFlagName(err)
	if !ok {
		return err
	}

	replacement, retired := renamedFlags[name]
	if !retired {
		return err
	}

	return fmt.Errorf("%w: --%s was renamed to --%s", cloud.ErrInvalidArgument, name, replacement)
}

// undefinedFlagName reads the flag name out of the standard library's
// "flag provided but not defined: -name" message, which is what urfave/cli
// hands to OnUsageError for an unknown flag.
func undefinedFlagName(err error) (string, bool) {
	const marker = "flag provided but not defined: -"

	message := err.Error()

	index := strings.Index(message, marker)
	if index < 0 {
		return "", false
	}

	return strings.TrimLeft(message[index+len(marker):], "-"), true
}

// newSpotClient builds the AWS client for this invocation.
//
// --offline answers from the committed snapshot instead of the live feeds. It is
// worth a flag because the feeds are most of an invocation: the advisor document
// alone takes over a second, against roughly a tenth of a second to answer from
// embedded data. GCP and Azure are already offline-only, so this is what makes
// the AWS surface consistent with them for a caller that wants it.
//
// The default is unchanged: without the flag, live data is still fetched and the
// snapshot remains a fallback.
func newSpotClient(ctx *cli.Context) *spot.Client {
	return newSpotClientFor(fetchPolicy(ctx))
}

// fetchPolicy reads --offline and --refresh from the nearest context that set
// them. It is one function because two consumers need the same answer: the AWS
// acquisition client, and the provider registry, whose Azure provider carries
// its own live path.
func fetchPolicy(ctx *cli.Context) cloud.FetchPolicy {
	return cloud.FetchPolicy{
		Offline: lineageBool(ctx, flagOffline),
		Refresh: lineageBool(ctx, flagRefresh),
	}
}

// newSpotClientFor builds the client for one data policy. It is split from the
// flag reading above because the MCP surface takes the same policy per tool
// call rather than once per process.
func newSpotClientFor(requested cloud.FetchPolicy) *spot.Client {
	policy := spot.FetchPolicy{
		UseEmbedded: requested.Offline,
		Refresh:     requested.Refresh,
	}

	client := spot.NewWithFetchOptions(spot.DefaultTimeoutSeconds*time.Second, policy)
	if !policy.UseEmbedded {
		return client
	}

	// Offline has to mean offline. The embedded feeds price most instances but
	// not all, and an unpriced one otherwise falls through to
	// DescribeSpotPriceHistory — an AWS API call that, without credentials,
	// blocks for the live-price timeout per region and made `--offline` slower
	// than the live path rather than faster.
	client.SetLivePriceProvider(nil)

	return client
}

// newAWSProvider builds the AWS adapter. Every surface builds it here — the
// registry that `recommend` and the MCP server resolve through, and `spotinfo
// list`, which builds from the acquisition client it already holds — so no two
// surfaces can disagree about what an AWS provider is.
//
// `list` used to read the architecture snapshot only when --architecture asked
// for it. That made the same list question answer differently depending on who
// asked: every AWS row carried an empty architecture from the CLI and a real one
// over MCP, which resolved through the registry and always loaded the lookup.
//
// An unreadable snapshot disables architecture filtering, not the whole
// provider: the provider stops declaring architectures, so a request that
// filters on one is refused instead of being answered from an unclassified
// catalogue, and a request that never mentions one still answers. The nil is a
// literal on purpose — a typed nil *spot.ArchitectureLookup is a non-nil
// interface, and the provider would go on declaring architectures it cannot
// resolve.
func newAWSProvider(client spotClient) (cloud.Provider, error) {
	lookup, err := spot.LoadEmbeddedArchitectureLookup()
	if err != nil {
		log.Warn("aws architecture snapshot is unusable; architecture filtering is disabled",
			slog.Any("error", err))

		return awsprovider.New(client, nil)
	}

	return awsprovider.New(client, lookup)
}

// newProviderRegistry composes the providers this binary serves. The AWS
// client is shared with the legacy acquisition path so a single invocation
// opens one client. A provider whose snapshot fails its gates is reported
// disabled with its reason, never substituted by another cloud.
func newProviderRegistry(client *spot.Client) (*providers.Registry, error) {
	return newProviderRegistryFor(client, cloud.FetchPolicy{})
}

// newProviderRegistryFor is the same composition under an explicit data policy.
//
// The AWS client already carries its own policy, which is why the zero-policy
// spelling above is still the honest one for callers that only hold a client.
// Azure needs the policy as a value: its live price path is inside the provider,
// not inside an acquisition client, so --offline and --refresh reach it here.
func newProviderRegistryFor(client *spot.Client, policy cloud.FetchPolicy) (*providers.Registry, error) {
	registry, err := providers.New(
		providers.Registration{
			ID:    cloud.ProviderAWS,
			Build: func() (cloud.Provider, error) { return newAWSProvider(client) },
		},
		providers.Registration{
			ID:    cloud.ProviderGCP,
			Build: func() (cloud.Provider, error) { return gcpprovider.New() },
		},
		providers.Registration{
			ID: cloud.ProviderAzure,
			Build: func() (cloud.Provider, error) {
				provider, buildErr := azureprovider.New()
				if buildErr != nil {
					return nil, buildErr
				}

				return provider.WithLivePrices(azureprovider.LivePriceConfig{Policy: policy}), nil
			},
		},
	)
	if err != nil {
		return nil, err
	}

	// Why a cloud is missing is a --debug question, not an error: the request
	// that selects it reports the same reason code on stderr.
	//
	// NOT_LOADED is skipped rather than logged as unavailable. Providers are
	// built on first use, so at this point every registered cloud is unloaded —
	// reporting that as "unavailable" would be noise, and reading the status of
	// the others is exactly the eager construction the registry now defers.
	for _, status := range registry.Status() {
		if !status.Enabled && status.Reason != providers.ReasonNotLoaded {
			log.Debug("cloud provider unavailable",
				slog.String("provider", string(status.ID)),
				slog.String("reason", string(status.Reason)),
				slog.String("detail", status.Detail))
		}
	}

	return registry, nil
}

// mcpProviders hands the MCP server the providers one tool call must be
// answered from. Each call names its own data policy — one server answers many
// clients, and a snapshot-only question from one of them must not put the
// process into snapshot-only mode for the rest.
//
// A registry is built at most once per policy and then reused: the feeds a
// spot.Client caches are what make a second call fast. A refreshing call always
// builds a fresh one — "ignore the cache" cannot be answered from a client that
// already read it — and replaces the memoised entry, so the next call answers
// from the data that refresh just fetched rather than from the copy it was
// meant to supersede.
type mcpProviders struct {
	built map[bool]*providers.Registry
	mutex sync.Mutex
}

// newMCPProviders builds the default registry eagerly, so a composition failure
// is reported before the server starts serving rather than inside a tool call.
func newMCPProviders() (*mcpProviders, error) {
	registry, err := newProviderRegistry(newSpotClientFor(cloud.FetchPolicy{}))
	if err != nil {
		return nil, err
	}

	return &mcpProviders{built: map[bool]*providers.Registry{false: registry}}, nil
}

// Get resolves one provider under the request's data policy.
func (m *mcpProviders) Get(id cloud.ProviderID, policy cloud.FetchPolicy) (cloud.Provider, error) {
	registry, err := m.registry(policy)
	if err != nil {
		return nil, err
	}

	return registry.Get(id)
}

// Registered names the clouds this binary serves. It reads the default
// registry: which providers are compiled in does not depend on data policy.
func (m *mcpProviders) Registered() []cloud.ProviderID {
	registry, err := m.registry(cloud.FetchPolicy{})
	if err != nil {
		return nil
	}

	return registry.Registered()
}

func (m *mcpProviders) registry(policy cloud.FetchPolicy) (*providers.Registry, error) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if built, ok := m.built[policy.Offline]; ok && !policy.Refresh {
		return built, nil
	}

	registry, err := newProviderRegistryFor(newSpotClientFor(policy), policy)
	if err != nil {
		return nil, err
	}
	m.built[policy.Offline] = registry

	return registry, nil
}

// isMCPMode checks if the application should run in MCP server mode
func isMCPMode(ctx *cli.Context) bool {
	// Check CLI flag first
	if ctx.Bool(flagMCP) {
		return true
	}

	// Check environment variable
	if mode, exists := os.LookupEnv(mcpModeEnv); exists && strings.EqualFold(mode, mcpModeValue) {
		return true
	}

	return false
}

// runMCPServer starts the MCP server.
//
// It reads no flags: the query flags live on the two subcommands now, and the
// data policy this server answers under is a per-tool-call argument rather than
// a property of the process. Transport and port still come from the
// environment.
func runMCPServer(execCtx context.Context) error {
	log.Info("starting MCP server mode")

	// Get transport mode
	transport := getMCPTransport()
	port := getMCPPort()

	// The port is only meaningful for a network transport. Logging port=8080
	// beside transport=stdio described a socket that was never opened.
	if transport == stdioTransport {
		log.Info("MCP server configuration", slog.String("transport", transport))
	} else {
		log.Info("MCP server configuration",
			slog.String("transport", transport),
			slog.String("port", port))
	}

	registries, err := newMCPProviders()
	if err != nil {
		return err
	}

	// Create MCP server
	mcpServer, err := mcp.NewServer(mcp.Config{
		Version:   Version,
		Transport: transport,
		Port:      port,
		Logger:    log,
		Providers: registries,
	})
	if err != nil {
		return fmt.Errorf("failed to create MCP server: %w", err)
	}

	// Start server based on transport
	switch transport {
	case stdioTransport:
		return mcpServer.ServeStdio(execCtx)
	case sseTransport:
		return mcpServer.ServeSSE(execCtx, port)
	default:
		return fmt.Errorf("unsupported transport: %s", transport)
	}
}

// getMCPTransport returns the configured MCP transport mode
func getMCPTransport() string {
	if transport, exists := os.LookupEnv(mcpTransportEnv); exists && transport != "" {
		return transport
	}
	return stdioTransport // default
}

// getMCPPort returns the configured MCP port for SSE transport
func getMCPPort() string {
	if port, exists := os.LookupEnv(mcpPortEnv); exists && port != "" {
		return port
	}
	return defaultMCPPort
}

// spotClient is the AWS acquisition client an invocation opens. It is exactly
// the input internal/providers/aws needs, so the query command can build its
// provider over the same client the legacy recommend path still uses, without a
// second adapter in between.
type spotClient interface {
	GetSpotSavings(ctx context.Context, opts ...spot.GetSpotSavingsOption) ([]spot.Advice, error)
	// DataSource reports whether the advice came from the live feeds, the local
	// cache, or the embedded snapshot. The query command renders none of it; the
	// provider publishes it as Result.Mode.
	DataSource() string
}

// flagLineageContext returns the nearest context that explicitly set one of
// names. LocalFlagNames reports only flags a context actually parsed, so this
// preserves root flags placed before a subcommand without letting the
// subcommand's own defaults shadow them.
func flagLineageContext(ctx *cli.Context, names ...string) *cli.Context {
	for _, candidate := range ctx.Lineage() {
		for _, setName := range candidate.LocalFlagNames() {
			for _, name := range names {
				if setName == name {
					return candidate
				}
			}
		}
	}

	return ctx
}

func lineageString(ctx *cli.Context, name string) string {
	return flagLineageContext(ctx, name).String(name)
}

func lineageStringSlice(ctx *cli.Context, name string) []string {
	return flagLineageContext(ctx, name).StringSlice(name)
}

func lineageBool(ctx *cli.Context, name string) bool {
	return flagLineageContext(ctx, name).Bool(name)
}

// lineageIsSet reports whether a flag was given on any context in the lineage,
// as opposed to carrying its declared default.
func lineageIsSet(ctx *cli.Context, name string) bool {
	for _, candidate := range ctx.Lineage() {
		if slices.Contains(candidate.LocalFlagNames(), name) {
			return true
		}
	}

	return false
}

func lineageInt(ctx *cli.Context, name string) int {
	return flagLineageContext(ctx, name).Int(name)
}

// candidateSavings renders the savings column. SavingsPercent is absent when the
// advisor published no usable figure, where this command has always printed 0 —
// the neutral absence must not become a blank cell in a rendering whose contract
// is recorded.
func candidateSavings(candidate *cloud.Candidate) int {
	if candidate.SavingsPercent == nil {
		return 0
	}

	return *candidate.SavingsPercent
}

// candidatePriceCell renders the price column.
//
// An absent observation is a machine the cloud published no price for — 600 of
// 19,353 rows on the committed AWS snapshot under --region all — and it prints
// absentValue, not 0.0000. The JSON form publishes null for exactly those rows,
// and a rendered zero would be the same silent claim of a free machine in a
// different font.
func candidatePriceCell(candidate *cloud.Candidate) string {
	if candidate.Spot == nil {
		return absentValue
	}

	return formatPrice(candidate.Spot.Amount.Float64(), candidate.Spot.Live)
}

// csvPriceCell keeps the CSV column numeric wherever there is a number to
// print, and prints absentValue where there is not. The column is deliberately
// mixed: a parser that read 0 there would read a price of zero for a machine
// nobody priced, which is the one value this must never publish.
func csvPriceCell(candidate *cloud.Candidate) any {
	if candidate.Spot == nil {
		return absentValue
	}

	return candidate.Spot.Amount.Float64()
}

// candidateRisk renders the risk column from the neutral observation.
//
// A cloud that publishes no risk figure prints its status — "unavailable" —
// never a blank cell and never a zero. A browse table that left the column empty
// reads as "no interruptions", and a zero reads as a measurement of none; both
// are claims nothing measured. A provider that somehow reports neither label nor
// status is still described as unavailable rather than as nothing at all.
func candidateRisk(candidate *cloud.Candidate) string {
	if candidate.Risk.Label != "" {
		return candidate.Risk.Label
	}

	if candidate.Risk.Status != "" {
		return string(candidate.Risk.Status)
	}

	return string(cloud.RiskStatusUnavailable)
}

func printAdvicesText(candidates []cloud.Candidate, region bool, output io.Writer) {
	for i := range candidates {
		candidate := &candidates[i]

		scoreStr := ""
		if len(candidate.Placements) > 0 || candidate.PlacementStatus == cloud.PlacementStatusUnavailable {
			scoreStr = fmt.Sprintf(", %s=%s", placementKeyOf(candidate), getScoreDisplayValue(candidate))
		}

		priceStr := candidatePriceCell(candidate)

		// machine= and risk= are the vocabulary names. The keys used to be type=
		// and interruption=, both of which name concepts this release retired:
		// --type became --machine, and the column is a neutral risk observation
		// rather than an AWS interruption bucket.
		if region {
			fmt.Fprintf(output, "region=%s, machine=%s, vCPU=%d, memory=%vGiB, saving=%d%%, risk='%s', price=%s%s\n", //nolint:errcheck
				candidate.Location.Region, candidate.Machine.ID, candidate.Machine.VCPU, candidate.Machine.MemoryGiB,
				candidateSavings(candidate), candidateRisk(candidate), priceStr, scoreStr)
		} else {
			fmt.Fprintf(output, "machine=%s, vCPU=%d, memory=%vGiB, saving=%d%%, risk='%s', price=%s%s\n", //nolint:errcheck
				candidate.Machine.ID, candidate.Machine.VCPU, candidate.Machine.MemoryGiB,
				candidateSavings(candidate), candidateRisk(candidate), priceStr, scoreStr)
		}
	}
}

// formatPrice formats a price value with an asterisk suffix for live-fetched prices.
func formatPrice(price float64, live bool) string {
	s := fmt.Sprintf("%.4f", price)
	if live {
		return s + "*"
	}
	return s
}

func printAdvicesNumber(candidates []cloud.Candidate, region bool, output io.Writer) {
	if len(candidates) == 1 {
		fmt.Fprintln(output, candidateSavings(&candidates[0])) //nolint:errcheck,gosec // G602: false positive, length checked above
		return
	}

	for i := range candidates {
		candidate := &candidates[i]
		if region {
			fmt.Fprintf(output, "%s/%s: %d\n", //nolint:errcheck
				candidate.Location.Region, candidate.Machine.ID, candidateSavings(candidate))
		} else {
			fmt.Fprintf(output, "%s: %d\n", candidate.Machine.ID, candidateSavings(candidate)) //nolint:errcheck
		}
	}
}

// getScoreIndicator returns an emoji indicator based on the score value.
func getScoreIndicator(score int) string {
	switch {
	case score >= excellentScoreThreshold:
		return "🟢" // Excellent (8-10)
	case score >= moderateScoreThreshold:
		return "🟡" // Moderate (5-7)
	case score >= poorScoreThreshold:
		return "🔴" // Poor (1-4)
	default:
		return "❓" // Unknown
	}
}

// formatScoreWithIndicator formats a score with its visual indicator.
func formatScoreWithIndicator(score int) string {
	return fmt.Sprintf("%s %d", getScoreIndicator(score), score)
}

// regionalPlacement returns the score measured for the whole region, if the
// provider published one. A regional observation carries no zone.
func regionalPlacement(candidate *cloud.Candidate) *cloud.PlacementObservation {
	for i := range candidate.Placements {
		if candidate.Placements[i].Location.Zone == "" {
			return &candidate.Placements[i]
		}
	}

	return nil
}

// zonalPlacements returns the per-zone scores in the order the provider
// published them, which is sorted by zone.
func zonalPlacements(candidate *cloud.Candidate) []cloud.PlacementObservation {
	zonal := make([]cloud.PlacementObservation, 0, len(candidate.Placements))
	for _, placement := range candidate.Placements {
		if placement.Location.Zone != "" {
			zonal = append(zonal, placement)
		}
	}

	return zonal
}

// placementKeyOf names the measurement in the text format, which prints
// key=value per row and has no header to carry the name.
//
// "score" is the fallback rather than a guess. It is the word every score flag
// already spells — --with-score, --min-score, --sort score — and the only value
// it labels without observations is "unavailable", which is on no scale at all.
func placementKeyOf(candidate *cloud.Candidate) string {
	if len(candidate.Placements) > 0 && candidate.Placements[0].Kind == cloud.PlacementKindObtainability {
		return string(cloud.PlacementKindObtainability)
	}

	return sortScore
}

// placementCell renders one observation on its own kind's scale.
//
// There is deliberately no shared number and no shared indicator. The emoji
// band exists for the AWS score, whose ten values a reader cannot rank at a
// glance; a probability already reads as one, and inventing green/amber/red
// boundaries for a figure Google publishes without them would be this tool
// stating a judgement no vendor made.
//
// A kind this build does not know renders as absent for the same reason the
// JSON form publishes nothing for it: the number is on a scale nothing here
// can name.
func placementCell(placement *cloud.PlacementObservation, decorate bool) string {
	switch placement.Kind {
	case cloud.PlacementKindPlacementScore:
		if decorate {
			return formatScoreWithIndicator(placement.Score)
		}

		return strconv.Itoa(placement.Score)
	case cloud.PlacementKindObtainability:
		if placement.Obtainability == nil {
			return absentValue
		}

		return strconv.FormatFloat(*placement.Obtainability, 'f', obtainabilityDecimals, 64)
	default:
		return absentValue
	}
}

// scoreValue renders the placement figures of one candidate. The regional
// figure wins when both are present, matching what the score column has always
// shown; decorate adds the visual indicator the table uses and CSV does not.
func scoreValue(candidate *cloud.Candidate, decorate bool) string {
	if regional := regionalPlacement(candidate); regional != nil {
		return addFreshnessInfo(placementCell(regional, decorate), regional.FetchedAt)
	}

	zonal := zonalPlacements(candidate)
	if len(zonal) == 0 {
		return placementAbsence(candidate)
	}

	scores := make([]string, 0, len(zonal))
	for i := range zonal {
		scores = append(scores, fmt.Sprintf("%s:%s",
			zonal[i].Location.Zone, addFreshnessInfo(placementCell(&zonal[i], decorate), zonal[i].FetchedAt)))
	}

	return strings.Join(scores, ",")
}

// placementAbsence says which kind of nothing this is.
//
// "-" is what every format prints for an observation the cloud did not publish,
// and it is the right answer when no placement lookup was asked for. It is the
// wrong answer when one was made and came back empty: that is a result about
// this run — no credentials, a timed-out call, a region AWS scored none of —
// and printing the same dash as an unasked question hides it.
func placementAbsence(candidate *cloud.Candidate) string {
	if candidate.PlacementStatus == cloud.PlacementStatusUnavailable {
		return string(cloud.PlacementStatusUnavailable)
	}

	return absentValue
}

// getScoreDataValue returns raw score data without visual formatting.
func getScoreDataValue(candidate *cloud.Candidate) string {
	return scoreValue(candidate, false)
}

// getScoreDisplayValue returns formatted score with visual indicators for table display.
func getScoreDisplayValue(candidate *cloud.Candidate) string {
	return scoreValue(candidate, true)
}

// addFreshnessInfo adds subtle freshness indicator to score display.
func addFreshnessInfo(scoreStr string, fetchedAt *time.Time) string {
	if fetchedAt == nil {
		return scoreStr
	}

	age := time.Since(*fetchedAt)
	if age > 30*time.Minute {
		// Only show indicator for stale data
		return scoreStr + "*"
	}
	return scoreStr
}

// scoreTypeInfo holds information about score types present in advices.
type scoreTypeInfo struct {
	// kind is the measurement this page carries. One query is answered by one
	// cloud, so one page carries at most one kind.
	kind              cloud.PlacementKind
	hasScores         bool
	hasRegionalScores bool
	hasAZScores       bool
}

// analyzeScoreTypes checks what types of scores are present in the candidates.
//
// A candidate whose lookup came back empty also opens the column: "unavailable"
// is an answer about this run, and dropping the column entirely would report it
// as though no lookup had been asked for.
func analyzeScoreTypes(candidates []cloud.Candidate) scoreTypeInfo {
	info := scoreTypeInfo{}
	for i := range candidates {
		if regionalPlacement(&candidates[i]) != nil {
			info.hasScores = true
			info.hasRegionalScores = true
		}
		if len(zonalPlacements(&candidates[i])) > 0 {
			info.hasScores = true
			info.hasAZScores = true
		}
		if candidates[i].PlacementStatus == cloud.PlacementStatusUnavailable {
			info.hasScores = true
		}
		if info.kind == "" && len(candidates[i].Placements) > 0 {
			info.kind = candidates[i].Placements[0].Kind
		}
	}
	return info
}

// placementHeaders names the column after the measurement it carries. The two
// kinds have separate names rather than a shared one because they are on
// separate scales: a reader who saw "Placement Score" over a 0.0-1.0
// probability would read it as an AWS score of zero.
type placementHeaderSet struct{ generic, regional, az string }

var placementHeaders = map[cloud.PlacementKind]placementHeaderSet{
	cloud.PlacementKindPlacementScore: {scoreHeaderGeneric, scoreHeaderRegional, scoreHeaderAZ},
	cloud.PlacementKindObtainability:  {obtainabilityHeaderGeneric, obtainabilityHeaderRegional, obtainabilityHeaderAZ},
}

// determineScoreHeader returns the appropriate score column header based on score types.
func determineScoreHeader(info scoreTypeInfo) string {
	headers, named := placementHeaders[info.kind]
	if !info.hasScores || !named {
		// Nothing was measured, so there is no measurement to name: a page whose
		// every lookup came back empty keeps the neutral word.
		return scoreColumn
	}
	if info.hasAZScores && !info.hasRegionalScores {
		return headers.az
	}
	if info.hasRegionalScores && !info.hasAZScores {
		return headers.regional
	}
	return headers.generic
}

// buildTableHeader creates the table header row.
func buildTableHeader(scoreInfo scoreTypeInfo, region, csv bool) table.Row {
	header := table.Row{machineColumn, vCPUColumn, memoryColumn, savingsColumn, riskColumn, priceColumn}
	if csv {
		header = append(header, priceSourceColumn)
	}
	if scoreInfo.hasScores {
		header = append(header, determineScoreHeader(scoreInfo))
	}
	if region {
		header = append(table.Row{regionColumn}, header...)
	}
	return header
}

// tableRowOptions configures how table rows are formatted.
type tableRowOptions struct {
	includeVisualFormatting bool
}

// TableRowOption defines a function type for configuring table row formatting.
type TableRowOption func(*tableRowOptions)

// WithVisualFormatting enables emoji indicators in table output.
func WithVisualFormatting() TableRowOption {
	return func(opts *tableRowOptions) {
		opts.includeVisualFormatting = true
	}
}

// buildTableRow creates a table row for a candidate with configurable formatting.
func buildTableRow(candidate *cloud.Candidate, scoreInfo scoreTypeInfo, region, csv bool, options ...TableRowOption) table.Row {
	opts := &tableRowOptions{}
	for _, opt := range options {
		opt(opts)
	}

	var priceValue any
	if csv {
		priceValue = csvPriceCell(candidate)
	} else {
		priceValue = candidatePriceCell(candidate)
	}
	row := table.Row{
		string(candidate.Machine.ID), candidate.Machine.VCPU, candidate.Machine.MemoryGiB,
		candidateSavings(candidate), candidateRisk(candidate), priceValue,
	}
	if csv {
		row = append(row, priceSource(candidate))
	}
	if scoreInfo.hasScores {
		var score string
		if opts.includeVisualFormatting {
			score = getScoreDisplayValue(candidate)
		} else {
			score = getScoreDataValue(candidate)
		}
		row = append(row, score)
	}
	if region {
		row = append(table.Row{string(candidate.Location.Region)}, row...)
	}
	return row
}

// priceSource labels where the CSV row's price came from. A machine with no
// price observation has no source either: "static" there would name the feed a
// price was read from for a price that was never read.
func priceSource(candidate *cloud.Candidate) string {
	if candidate.Spot == nil {
		return absentValue
	}

	if candidate.Spot.Live {
		return "live"
	}

	return "static"
}

// zonePriceAt returns the price observed for one zone of a candidate's region.
func zonePriceAt(candidate *cloud.Candidate, zone string) *cloud.PriceObservation {
	for i := range candidate.ZonePrices {
		if candidate.ZonePrices[i].Location.Zone == zone {
			return &candidate.ZonePrices[i]
		}
	}

	return nil
}

// expandAZ converts candidates with multiple zone scores into separate rows per
// AZ, each carrying that zone's own price when the provider published one.
func expandAZ(candidates []cloud.Candidate) []cloud.Candidate {
	var result []cloud.Candidate

	for i := range candidates {
		candidate := candidates[i]

		zonal := zonalPlacements(&candidate)
		if len(zonal) <= 1 {
			// No expansion needed - keep as-is
			result = append(result, candidate)
			continue
		}

		for _, placement := range zonal {
			// One zone's score replaces the whole placement set, so the regional
			// score does not shadow it in the score column.
			zoneRow := candidate
			zoneRow.Placements = []cloud.PlacementObservation{placement}

			if price := zonePriceAt(&candidate, placement.Location.Zone); price != nil {
				zonePrice := *price
				zoneRow.Spot = &zonePrice
				zoneRow.ZonePrices = []cloud.PriceObservation{zonePrice}
			}

			result = append(result, zoneRow)
		}
	}

	return result
}

func printAdvicesTable(candidates []cloud.Candidate, csv, region bool, output io.Writer) {
	tbl := table.NewWriter()
	tbl.SetOutputMirror(output)

	// Expand AZ scores to separate rows for better display
	candidates = expandAZ(candidates)

	// Analyze score types and build header
	scoreInfo := analyzeScoreTypes(candidates)
	header := buildTableHeader(scoreInfo, region, csv)
	tbl.AppendHeader(header)

	// Build rows with appropriate formatting
	for i := range candidates {
		var row table.Row
		if csv {
			// CSV output: data only, no visual formatting, with price source column
			row = buildTableRow(&candidates[i], scoreInfo, region, csv)
		} else {
			// Table output: include visual formatting
			row = buildTableRow(&candidates[i], scoreInfo, region, csv, WithVisualFormatting())
		}
		tbl.AppendRow(row)
	}
	// render as CSV
	if csv {
		tbl.RenderCSV()
	} else { // render as pretty table
		tbl.SetColumnConfigs([]table.ColumnConfig{{
			Name:        savingsColumn,
			Transformer: text.NewNumberTransformer("%d%%"),
		}})
		tbl.SetStyle(table.StyleLight)
		tbl.Style().Options.SeparateRows = true
		tbl.Render()
	}
}

func init() {
	// Initialize logger with default level
	log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)
	// handle termination signal
	mainCtx = handleSignals()

	// Colour is a terminal affordance. go-pretty honours NO_COLOR but does not
	// look at the writer, so `spotinfo --type m5.large > out.txt` wrote
	// "\x1b[92m59%\x1b[0m" into the file and every pipe into grep or awk
	// carried escapes. The v1 golden test already records the uncoloured form,
	// so this aligns the shipped output with the contract rather than changing it.
	if !isTerminal(os.Stdout) {
		text.DisableColors()
	}
}

// isTerminal reports whether f is attached to a character device. A pipe, a
// file and /dev/null are all not.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}

	return info.Mode()&os.ModeCharDevice != 0
}

func handleSignals() context.Context {
	// Graceful shut-down on SIGINT/SIGTERM
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	// create cancelable context
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		defer cancel()

		sid := <-sig

		log.Info("received signal", slog.String("signal", sid.String()))
		log.Info("canceling main command")
	}()

	return ctx
}

func defaultRecommendAction(ctx *cli.Context) error {
	client := newSpotClient(ctx)

	registry, err := newProviderRegistryFor(client, fetchPolicy(ctx))
	if err != nil {
		return err
	}

	return execRecommendCmd(ctx, mainCtx, registry, os.Stdout)
}

//nolint:funlen // CLI flag declarations are intentionally kept together.
func recommendCommand(action cli.ActionFunc) *cli.Command {
	return &cli.Command{
		Name: recommendCommandName,
		Usage: "rank the best Spot machines for a stated requirement " +
			"(requires --" + flagArchitecture + ", --" + flagMinVCPU + " and --" + flagMinMemoryGiB + ")",
		Action:       action,
		OnUsageError: renameHint,
		Flags: append([]cli.Flag{
			cloudFlag(),
			// Required, but checked in execRecommendCmd rather than declared to
			// urfave/cli. Its own check prints the whole help to *stdout* before
			// returning the error, so `spotinfo recommend > out.json` wrote a help
			// page into the file. See requireRecommendFlags.
			&cli.StringFlag{Name: flagArchitecture, Usage: "required machine architecture: x86_64|arm64"},
			&cli.StringFlag{
				Name:  flagMachine,
				Usage: "machine type RE2 regexp (combined with architecture)",
			},
			&cli.StringSliceFlag{
				Name:  flagRegion,
				Usage: regionFlagUsage,
				Value: cli.NewStringSlice(defaultRegion),
			},
			// Required too, like the MCP tool, which declares all three in its
			// input schema. They used to be optional flags with a default of 0
			// that the validator then rejected — so the help said "(default: 0)"
			// while `--min-vcpu 4` alone failed deep in the request vocabulary
			// with "min_memory_gib must be a positive number", naming a wire
			// field rather than the flag the caller had omitted.
			// DefaultText because urfave/cli renders an int flag's zero Value as
			// "(default: 0)" with no way to suppress it, which read as "optional,
			// defaults to no minimum" beside a Usage line saying "required".
			&cli.IntFlag{
				Name:  flagMinVCPU,
				Usage: "required minimum vCPU cores", DefaultText: requiredFlagDefaultText,
			},
			&cli.IntFlag{
				Name:  flagMinMemoryGiB,
				Usage: "required minimum memory GiB", DefaultText: requiredFlagDefaultText,
			},
			&cli.Float64Flag{
				Name:        flagMaxPrice,
				Usage:       "positive maximum USD per candidate machine-hour",
				DefaultText: unfilteredDefaultText,
			},
			&cli.StringFlag{Name: flagOS, Usage: osFlagUsage, Value: defaultOS},
			// cost by default, on every cloud: it is the only policy a provider
			// without risk data can serve honestly, and one default on both
			// surfaces is what makes the same question return the same document.
			&cli.StringFlag{
				Name:  flagWorkload,
				Usage: "ranking policy: cost|web|ci|batch (web, ci and batch cap interruption and need a cloud that publishes it)",
				Value: defaultWorkload,
			},
			&cli.IntFlag{Name: flagTop, Usage: "maximum recommendations to return", Value: defaultTop},
			&cli.StringFlag{
				Name:        flagSort,
				Usage:       "print the ranked page ordered by " + strings.Join(sortKeyNames(), "|"),
				DefaultText: "the ranking policy's own order",
			},
			&cli.StringFlag{
				Name:        flagOrder,
				Usage:       "sort order " + orderAsc + "|" + orderDesc,
				DefaultText: orderAsc,
			},
			&cli.BoolFlag{
				Name:  flagOffline,
				Usage: offlineFlagUsage,
			},
			&cli.BoolFlag{
				Name:  flagRefresh,
				Usage: refreshFlagUsage,
			},
			&cli.StringFlag{Name: flagOutput, Usage: "format output: table|json", Value: defaultOutput},
		}, append(liveRiskFlags(), scoreFlags()...)...),
	}
}

// newSpotinfoApp assembles the production command wiring: a root that selects a
// mode, and the two commands that ask a question. The command actions are
// injected so assembly tests exercise the production flags and commands without
// opening network clients or writing to process stdout.
//
// The root carries only the mode and logging flags. Every flag that describes a
// question lives on the command that answers it — a query flag on the root could
// be given before a subcommand that never reads it, which is how `--machine`
// once parsed cleanly into a value `recommend` ignored.
func newSpotinfoApp(listAction, recommendationAction cli.ActionFunc) *cli.App {
	return &cli.App{
		Before: func(ctx *cli.Context) error {
			// Update logger based on flags
			logLevel := slog.LevelInfo
			if ctx.Bool(flagDebug) {
				logLevel = slog.LevelDebug
			} else if ctx.Bool(flagQuiet) {
				logLevel = slog.LevelError
			}

			opts := &slog.HandlerOptions{Level: logLevel}
			structuredLogging = ctx.Bool(flagJSONLog) || ctx.Bool(flagDebug)
			if ctx.Bool(flagJSONLog) {
				log = slog.New(slog.NewJSONHandler(os.Stderr, opts))
			} else {
				log = slog.New(slog.NewTextHandler(os.Stderr, opts))
			}
			// internal/spot logs through the package default logger, which was
			// never replaced: --quiet, --debug and --json-log configured `log`
			// and nothing else, so live-price warnings ignored all three and
			// printed in the standard library's format while this command's own
			// errors printed in slog's. One binary, two log formats, and
			// --quiet that did not quiet.
			slog.SetDefault(log)

			return nil
		},
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:  flagMCP,
				Usage: "run as MCP server instead of CLI",
			},
			&cli.BoolFlag{
				Name:  flagDebug,
				Usage: "enable debug logging",
			},
			&cli.BoolFlag{
				Name:  flagQuiet,
				Usage: "quiet mode (errors only)",
			},
			&cli.BoolFlag{
				Name:  flagJSONLog,
				Usage: "output logs in JSON format",
			},
		},
		OnUsageError: renameHint,
		Name:         appName,
		Usage:        "explore Spot machine prices across AWS, GCP and Azure",
		Action:       rootCmd,
		Commands: []*cli.Command{
			listCommand(listAction),
			recommendCommand(recommendationAction),
		},
		Version: Version,
	}
}

func printVersion(_ *cli.Context) {
	fmt.Printf("spotinfo %s\n", Version)

	if GitHubRelease != "" {
		fmt.Printf("  GitHub release: %s\n", GitHubRelease)
	}

	if BuildDate != "" && BuildDate != unknownBuildValue {
		fmt.Printf("  Build date: %s\n", BuildDate)
	}

	if GitCommit != "" {
		fmt.Printf("  Git commit: %s\n", GitCommit)
	}

	if GitBranch != "" {
		fmt.Printf("  Git branch: %s\n", GitBranch)
	}

	fmt.Printf("  Built with: %s\n", runtime.Version())
}

func main() {
	app := newSpotinfoApp(listCmd, defaultRecommendAction)
	cli.VersionPrinter = printVersion

	if err := app.Run(os.Args); err != nil {
		reportFatal(err)
		os.Exit(1)
	}
}

// reportFatal writes the one line a person needs.
//
// Every failure used to print as a log record — a timestamp, level=ERROR and
// the prefix "application failed" — around what is usually a plain mistake in
// the command line. The structured record is kept for the two flags that ask
// for machine-readable logs, since something is parsing stderr there; everyone
// else gets `spotinfo: <message>`.
func reportFatal(err error) {
	if structuredLogging {
		log.Error("application failed", slog.Any("error", err))

		return
	}

	fmt.Fprintf(os.Stderr, "%s: %v\n", appName, err) //nolint:errcheck // nothing useful to do if stderr is gone
}
