package mcp

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spotinfo/internal/cloud"
	"spotinfo/internal/spot"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func listTool(providers providerRegistry) *ListSpotMachinesTool {
	return NewListSpotMachinesTool(providers, testLogger())
}

func regionsTool(providers providerRegistry) *ListCloudRegionsTool {
	return NewListCloudRegionsTool(providers, testLogger())
}

func callTool(t *testing.T,
	handle func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error), args map[string]any,
) *mcp.CallToolResult {
	t.Helper()

	result, err := handle(t.Context(), mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: args}})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, result.Content, 1)

	return result
}

func decodeListReport(t *testing.T, result *mcp.CallToolResult) cloud.ListReport {
	t.Helper()

	require.False(t, result.IsError, "expected an answer, got %s", payloadOf(t, result))

	var report cloud.ListReport
	require.NoError(t, json.Unmarshal(payloadOf(t, result), &report))

	return report
}

func decodeRegionsReport(t *testing.T, result *mcp.CallToolResult) cloud.RegionsReport {
	t.Helper()

	require.False(t, result.IsError, "expected an answer, got %s", payloadOf(t, result))

	var report cloud.RegionsReport
	require.NoError(t, json.Unmarshal(payloadOf(t, result), &report))

	return report
}

func registryOf(fixtures ...testCandidate) providerRegistry {
	_, registry := awsStub(buildCandidates(fixtures...)...)

	return registry
}

func scorePtr(score int) *int { return &score }

// Every acquisition-shaping argument must reach the provider. Asserting the
// answer alone would pass even if the score, zone, ordering and price arguments
// were dropped, because a fixture can carry scores the request never asked for.
func TestListArgumentsReachTheProviderQuery(t *testing.T) {
	t.Parallel()

	provider, registry := awsStub()

	result := callTool(t, listTool(registry).Handle, map[string]any{
		argRegions:      []any{"us-east-1", "eu-west-1"},
		argMachine:      "m5.*",
		argArchitecture: "arm64",
		argOS:           "windows",
		argMinVCPU:      4,
		argMinMemoryGiB: 16.0,
		argMaxPrice:     0.25,
		argSort:         "savings",
		argOrder:        cloud.OrderDesc,
		argWithScore:    true,
		argMinScore:     7,
		argAZ:           true,
		argScoreTimeout: 60,
	})
	require.False(t, result.IsError, string(payloadOf(t, result)))

	query := provider.lastQuery()
	assert.Equal(t, []cloud.Region{"us-east-1", "eu-west-1"}, query.Regions)
	assert.Equal(t, "m5.*", query.MachinePattern)
	assert.Equal(t, cloud.ArchitectureARM64, query.Architecture)
	assert.Equal(t, cloud.OSWindows, query.OS)
	assert.Equal(t, 4, query.MinVCPU)
	assert.InDelta(t, 16.0, query.MinMemoryGiB, 0)
	require.NotNil(t, query.MaxPrice)
	assert.Equal(t, "0.250000000", query.MaxPrice.String())
	assert.Equal(t, cloud.SortOrder{Key: cloud.SortBySavings, Descending: true}, query.Sort)
	assert.Equal(t, cloud.PlacementRequest{
		Timeout:    60 * time.Second,
		MinScore:   7,
		SingleZone: true,
		Enabled:    true,
	}, query.Placement)
}

// Every documented default is applied by the handler, so a minimal request is
// answered exactly as the advertised schema describes — and with the same
// values the CLI uses, because both read them from internal/cloud.
func TestListAppliesTheDocumentedDefaults(t *testing.T) {
	t.Parallel()

	provider, registry := awsStub(buildCandidates(testCandidate{
		Region: "us-east-1", Machine: "m6i.large", Price: 0.0416, Savings: 72,
		RiskLabel: "<5%", RiskMin: 0, RiskMax: 5, VCPU: 2, MemoryGiB: 8,
	})...)

	report := decodeListReport(t, callTool(t, listTool(registry).Handle, map[string]any{}))

	assert.Equal(t, cloud.SchemaVersionListV1, report.SchemaVersion)
	assert.Equal(t, cloud.ProviderAWS, report.Request.Cloud, "cloud defaults to aws")
	assert.Equal(t, []cloud.Region{cloud.RegionAll}, report.Request.Regions, "regions default to all")
	assert.Equal(t, cloud.OSLinux, report.Request.OS)
	assert.Equal(t, cloud.OrderAsc, report.Request.Order)
	assert.Empty(t, report.Request.Sort, "an omitted sort leaves the order to the provider")
	assert.Empty(t, report.Request.Machine)
	assert.Nil(t, report.Request.MaxPrice, "an omitted ceiling is null, not zero")
	require.Len(t, report.Candidates, 1)

	query := provider.lastQuery()
	assert.Equal(t, cloud.PlacementRequest{}, query.Placement, "scores are off unless asked for")
}

// The list tool and `spotinfo list --output json` publish the same document:
// same schema, same candidate block, same provenance. That is what makes the
// two surfaces answer one question one way.
func TestListToolPublishesTheSameDocumentAsTheCLI(t *testing.T) {
	t.Parallel()

	candidates := buildCandidates(testCandidate{
		Region: "us-east-1", Machine: "m6i.large", Price: 0.0416, Savings: 72, Live: true,
		RiskLabel: "<5%", RiskMin: 0, RiskMax: 5, VCPU: 2, MemoryGiB: 8,
	})

	provider, registry := awsStub(candidates...)

	report := decodeListReport(t, callTool(t, listTool(registry).Handle, map[string]any{
		argRegions: []any{"us-east-1"},
	}))

	expected, err := cloud.NewListReport(ptr(provider.lastQuery()), &cloud.Result{
		Provider:   cloud.ProviderAWS,
		Mode:       cloud.DataModeEmbeddedSnapshot,
		Sources:    testSources(),
		Candidates: candidates,
	})
	require.NoError(t, err)
	assert.Equal(t, *expected, report)
}

func ptr[T any](value T) *T { return &value }

// A cloud that publishes no interruption figure reports that absence. Reading
// its silence as zero would rank an unmeasured machine as the safest one.
func TestListReportsUnavailableRiskExplicitly(t *testing.T) {
	t.Parallel()

	registry := newStubRegistry(offlineProvider(cloud.ProviderGCP, gcpCandidates()))

	report := decodeListReport(t, callTool(t, listTool(registry).Handle, map[string]any{argCloud: "gcp"}))
	require.NotEmpty(t, report.Candidates)

	risk := report.Candidates[0].Risk
	assert.Equal(t, cloud.RiskStatusUnavailable, risk.Status)
	assert.Nil(t, risk.Kind)
	assert.Nil(t, risk.MinPercent)
	assert.Nil(t, risk.MaxPercent)
}

// The AWS adapter is driven for real here, over fixed spot.Advice: the whole
// spot.Advice -> cloud.Candidate conversion sits between what a caller asks and
// what this tool publishes, and a stub provider bypasses all of it.
func TestListToolAnswersThroughTheProductionAWSAdapter(t *testing.T) {
	t.Parallel()

	regionScore := 8
	registry := awsAdapterRegistry(t, []spot.Advice{
		{
			Region: "us-east-1", Instance: "m6i.large", Price: 0.0416, Savings: 72,
			Info:  spot.TypeInfo{Cores: 2, RAM: 8},
			Range: spot.Range{Label: "<5%", Min: 0, Max: 5},
		},
		{
			Region: "us-west-2", Instance: "m5.xlarge", Price: 0.1234, Savings: 65, LivePrice: true,
			Info:        spot.TypeInfo{Cores: 4, RAM: 16},
			Range:       spot.Range{Label: "5-10%", Min: 5, Max: 11},
			RegionScore: &regionScore,
		},
	})

	report := decodeListReport(t, callTool(t, listTool(registry).Handle, map[string]any{}))
	require.Len(t, report.Candidates, 2)

	first := report.Candidates[0]
	assert.Equal(t, cloud.MachineID("m6i.large"), first.Machine)
	require.NotNil(t, first.SpotUSDPerHour)
	assert.Equal(t, "0.041600000", *first.SpotUSDPerHour)
	require.NotNil(t, first.SavingsPercent)
	assert.InDelta(t, 72.0, *first.SavingsPercent, 0)
	assert.False(t, first.LivePrice)

	second := report.Candidates[1]
	assert.True(t, second.LivePrice, "a price the EC2 API supplied must say so")
	require.NotNil(t, second.RegionScore)
	assert.Equal(t, regionScore, *second.RegionScore)
}

// The capability gate runs before acquisition, so a provider that cannot answer
// the request costs no I/O. The error alone would not prove that — the stub
// records every call, so a zero call count is the proof.
func TestListRejectsAnUnsupportedCapabilityBeforeAcquisition(t *testing.T) {
	t.Parallel()

	// An offline Linux-only provider is the shape GCP and Azure have: no risk,
	// no scores, no zone detail.
	provider := &stubProvider{id: cloud.ProviderGCP, capabilities: offlineLinuxCapabilities()}
	registry := newStubRegistry(provider)

	for name, args := range map[string]map[string]any{
		"sorting by an unpublished risk figure": {argCloud: "gcp", argSort: "risk"},
		"asking for placement scores":           {argCloud: "gcp", argWithScore: true},
		"asking for zone detail":                {argCloud: "gcp", argWithScore: true, argAZ: true},
		"an operating system it does not price": {argCloud: "gcp", argOS: "windows"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			result := callTool(t, listTool(registry).Handle, args)
			assert.Equal(t, cloud.CodeUnsupportedCapability, decodeError(t, result).Code)
			assert.Zero(t, provider.callCount(), "the capability check must run before acquisition")
		})
	}
}

// Every failure is a published spotinfo.error/v1 body with a stable code, and
// no failure carries candidates.
func TestListErrorContract(t *testing.T) {
	t.Parallel()

	riskFree := newStubRegistry(offlineProvider(cloud.ProviderGCP, gcpCandidates()))

	for _, test := range []struct {
		name      string
		registry  providerRegistry
		args      map[string]any
		wantCode  cloud.ErrorCode
		wantCloud *string
	}{
		{
			name: "unknown cloud", registry: riskFree, wantCode: cloud.CodeInvalidArgument,
			args: map[string]any{argCloud: "ibm"}, wantCloud: stringPtr("ibm"),
		},
		{
			name: "unparsable cloud argument", registry: riskFree, wantCode: cloud.CodeInvalidArgument,
			args: map[string]any{argCloud: 42},
		},
		{
			name: "explicit empty cloud", registry: riskFree, wantCode: cloud.CodeInvalidArgument,
			args: map[string]any{argCloud: ""},
		},
		{
			// Misspelled, it would return machines with no ceiling applied and
			// status ok, which only a client diffing the echoed request would see.
			name: "undeclared argument", registry: riskFree, wantCode: cloud.CodeInvalidArgument,
			args: map[string]any{argCloud: "gcp", "budget": 0.05}, wantCloud: stringPtr("gcp"),
		},
		{
			// The retired v1 names have no successor on this tool and must not be
			// silently accepted either.
			name: "retired v1 argument", registry: riskFree, wantCode: cloud.CodeInvalidArgument,
			args: map[string]any{argCloud: "gcp", "instance_types": "n2-*"}, wantCloud: stringPtr("gcp"),
		},
		{
			name: "unknown sort key", registry: riskFree, wantCode: cloud.CodeInvalidArgument,
			args: map[string]any{argCloud: "gcp", argSort: "interruption"}, wantCloud: stringPtr("gcp"),
		},
		{
			name: "unknown order", registry: riskFree, wantCode: cloud.CodeInvalidArgument,
			args: map[string]any{argCloud: "gcp", argOrder: "descending"}, wantCloud: stringPtr("gcp"),
		},
		{
			name: "unknown architecture", registry: riskFree, wantCode: cloud.CodeInvalidArgument,
			args: map[string]any{argCloud: "gcp", argArchitecture: "riscv64"}, wantCloud: stringPtr("gcp"),
		},
		{
			name: "negative minimum vcpu", registry: riskFree, wantCode: cloud.CodeInvalidArgument,
			args: map[string]any{argCloud: "gcp", argMinVCPU: -1}, wantCloud: stringPtr("gcp"),
		},
		{
			name: "negative minimum memory", registry: riskFree, wantCode: cloud.CodeInvalidArgument,
			args: map[string]any{argCloud: "gcp", argMinMemoryGiB: -1.0}, wantCloud: stringPtr("gcp"),
		},
		{
			name: "zero price ceiling", registry: riskFree, wantCode: cloud.CodeInvalidArgument,
			args: map[string]any{argCloud: "gcp", argMaxPrice: 0}, wantCloud: stringPtr("gcp"),
		},
		{
			// ParseFloat accepts these from a quoted argument, and neither is a
			// ceiling: a NaN comparison drops every candidate, an infinite one
			// filters nothing while the caller believes it did.
			name: "quoted non-finite price ceiling", registry: riskFree, wantCode: cloud.CodeInvalidArgument,
			args: map[string]any{argCloud: "gcp", argMaxPrice: "NaN"}, wantCloud: stringPtr("gcp"),
		},
		{
			name: "minimum score without with_score", registry: riskFree, wantCode: cloud.CodeInvalidArgument,
			args: map[string]any{argCloud: "gcp", argMinScore: 7}, wantCloud: stringPtr("gcp"),
		},
		{
			name: "score timeout out of range", registry: riskFree, wantCode: cloud.CodeInvalidArgument,
			args: map[string]any{argCloud: "gcp", argScoreTimeout: 0}, wantCloud: stringPtr("gcp"),
		},
		{
			name: "with_score given as a string", registry: riskFree, wantCode: cloud.CodeInvalidArgument,
			args: map[string]any{argCloud: "gcp", argWithScore: "true"}, wantCloud: stringPtr("gcp"),
		},
		{
			name: "regions given as a bare string", registry: riskFree, wantCode: cloud.CodeInvalidArgument,
			args: map[string]any{argCloud: "gcp", argRegions: "us-central1 us-east1"}, wantCloud: stringPtr("gcp"),
		},
		{
			name: "empty regions array", registry: riskFree, wantCode: cloud.CodeInvalidArgument,
			args: map[string]any{argCloud: "gcp", argRegions: []any{}}, wantCloud: stringPtr("gcp"),
		},
		{
			name: "unregistered cloud", registry: riskFree, wantCode: cloud.CodeDataUnavailable,
			args: map[string]any{argCloud: "azure"}, wantCloud: stringPtr("azure"),
		},
		{
			name: "acquisition failure", registry: failingAWSStub(errAcquisition),
			wantCode: cloud.CodeInternal, args: map[string]any{}, wantCloud: stringPtr("aws"),
		},
		{
			// A binary composed without a registry still registers every tool.
			// The handler must report that, not dereference nil: a panic takes
			// the stdio process down for every connected client.
			name: "no provider registry", registry: nil, wantCode: cloud.CodeDataUnavailable,
			args: map[string]any{}, wantCloud: stringPtr("aws"),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			result := callTool(t, listTool(test.registry).Handle, test.args)

			report := decodeError(t, result)
			assert.Equal(t, cloud.SchemaVersionErrorV1, report.SchemaVersion)
			assert.Equal(t, test.wantCode, report.Code)
			assert.NotEmpty(t, report.Message)
			assert.Equal(t, test.wantCloud, report.Cloud)
			assert.NotContains(t, string(payloadOf(t, result)), "candidates")
		})
	}
}

// offline and refresh must reach the acquisition client, not merely be
// accepted. A tool that takes a data policy and drops it is indistinguishable
// from one that honours it — which is exactly the defect the retired
// include_names parameter was, and the reason the stub registry records what
// each lookup asked for.
func TestTheDataPolicyReachesAcquisition(t *testing.T) {
	t.Parallel()

	candidates := buildCandidates(testCandidate{
		Region: "us-east-1", Machine: "m6i.large", Price: 0.0416, VCPU: 2, MemoryGiB: 8,
	})

	for name, test := range map[string]struct {
		args map[string]any
		want cloud.FetchPolicy
	}{
		"omitted": {args: map[string]any{}, want: cloud.FetchPolicy{}},
		"offline": {args: map[string]any{argOffline: true}, want: cloud.FetchPolicy{Offline: true}},
		"refresh": {args: map[string]any{argRefresh: true}, want: cloud.FetchPolicy{Refresh: true}},
		"both":    {args: map[string]any{argOffline: true, argRefresh: true}, want: cloud.FetchPolicy{Offline: true, Refresh: true}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			t.Run(listToolName, func(t *testing.T) {
				t.Parallel()

				_, registry := awsStub(candidates...)
				require.False(t, callTool(t, listTool(registry).Handle, test.args).IsError)
				assert.Equal(t, test.want, registry.lastPolicy())
			})

			t.Run(recommendToolName, func(t *testing.T) {
				t.Parallel()

				_, registry := awsStub(candidates...)

				args := map[string]any{argArchitecture: "x86_64", argMinVCPU: 2, argMinMemoryGiB: 8.0}
				for key, value := range test.args {
					args[key] = value
				}

				require.False(t, callTool(t, recommendTool(registry).Handle, args).IsError)
				assert.Equal(t, test.want, registry.lastPolicy())
			})
		})
	}
}

// A mistyped data policy is refused rather than read as off: `offline: "true"`
// would otherwise fetch, which is the opposite of what the caller asked.
func TestAMistypedDataPolicyIsRefused(t *testing.T) {
	t.Parallel()

	provider, registry := awsStub()

	report := decodeError(t, callTool(t, listTool(registry).Handle, map[string]any{argOffline: "true"}))
	assert.Equal(t, cloud.CodeInvalidArgument, report.Code)
	assert.Contains(t, report.Message, argOffline)
	assert.Zero(t, provider.callCount(), "the argument check must run before acquisition")
}

// A price ceiling finer than the fixed-point scale still filters. A caller
// computing a budget — a monthly figure divided by 720 hours — sends one of
// these routinely, and dropping it would hand back machines above the ceiling
// with no error and no warning.
func TestFinerThanScalePriceCeilingStillFilters(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		price float64
		want  string
	}{
		"within the scale":       {price: 0.04, want: "0.040000000"},
		"exactly at the scale":   {price: 0.040000001, want: "0.040000001"},
		"one digit past":         {price: 0.0400000001, want: "0.040000000"},
		"monthly budget an hour": {price: 30.0 / 720.0, want: "0.041666666"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			provider, registry := awsStub()

			result := callTool(t, listTool(registry).Handle, map[string]any{argMaxPrice: test.price})
			require.False(t, result.IsError, string(payloadOf(t, result)))

			query := provider.lastQuery()
			require.NotNil(t, query.MaxPrice, "the caller's ceiling must reach the provider")
			assert.Equal(t, test.want, query.MaxPrice.String())
		})
	}
}

// An empty match is a real answer, not a failure: nothing there is what the
// caller asked about.
func TestListPublishesAnEmptyAnswerRatherThanAnError(t *testing.T) {
	t.Parallel()

	report := decodeListReport(t, callTool(t, listTool(registryOf()).Handle,
		map[string]any{argMachine: "z99.mega"}))

	assert.Empty(t, report.Candidates)
	assert.NotEmpty(t, report.DataSource.Sources, "an empty answer still says where it looked")
}

// list_cloud_regions answers for every registered cloud, from the same neutral
// seam — no cloud is baked into the tool.
func TestListCloudRegionsAnswersForEveryCloud(t *testing.T) {
	t.Parallel()

	registry := newStubRegistry(
		awsProviderStub(),
		offlineProvider(cloud.ProviderGCP, gcpCandidates()),
		offlineProvider(cloud.ProviderAzure, buildCandidates(
			testCandidate{Region: "westeurope", Machine: "Standard_D2s_v5", Price: 0.0192, VCPU: 2, MemoryGiB: 8},
			testCandidate{Region: "eastus", Machine: "Standard_D2s_v5", Price: 0.0180, VCPU: 2, MemoryGiB: 8},
		)),
	)

	for _, test := range []struct {
		cloudID cloud.ProviderID
		want    []cloud.Region
	}{
		{cloudID: cloud.ProviderAWS, want: []cloud.Region{"eu-west-1", "us-east-1", "us-west-2"}},
		{cloudID: cloud.ProviderGCP, want: []cloud.Region{"europe-west1", "us-central1"}},
		{cloudID: cloud.ProviderAzure, want: []cloud.Region{"eastus", "westeurope"}},
	} {
		t.Run(string(test.cloudID), func(t *testing.T) {
			t.Parallel()

			report := decodeRegionsReport(t, callTool(t, regionsTool(registry).Handle,
				map[string]any{argCloud: string(test.cloudID)}))

			assert.Equal(t, cloud.SchemaVersionRegionsV1, report.SchemaVersion)
			assert.Equal(t, test.cloudID, report.Cloud)
			assert.Equal(t, test.want, report.Regions, "regions are deduplicated and sorted")
		})
	}
}

// The whole point of the tool beside the regions: it publishes every source the
// cloud was read from, so the sources_omitted count a trimmed list or recommend
// answer carries is resolvable rather than merely visible.
func TestListCloudRegionsPublishesEverySourceUntrimmed(t *testing.T) {
	t.Parallel()

	// Two sources, each scoped to one region, so a trimmed answer would drop one.
	sources := []cloud.SourceRef{
		scopedSource("https://example.invalid/prices?region=us-east-1", cloud.Region("us-east-1")),
		scopedSource("https://example.invalid/prices?region=eu-west-1", cloud.Region("eu-west-1")),
	}

	provider := &stubProvider{
		id:           cloud.ProviderAWS,
		capabilities: awsCapabilities(),
		result: cloud.Result{
			Provider: cloud.ProviderAWS,
			Mode:     cloud.DataModeEmbeddedSnapshot,
			Sources:  sources,
			Candidates: buildCandidates(testCandidate{
				Region: "us-east-1", Machine: "m6i.large", Price: 0.0416, VCPU: 2, MemoryGiB: 8,
			}),
		},
	}
	registry := newStubRegistry(provider)

	// The browse answer carries only the us-east-1 row, so it publishes one
	// source and counts the other as omitted.
	listed := decodeListReport(t, callTool(t, listTool(registry).Handle, map[string]any{}))
	require.Len(t, listed.DataSource.Sources, 1)
	assert.Equal(t, 1, listed.DataSource.SourcesOmitted)

	// The region answer is about the cloud, not a row set, so nothing is trimmed
	// and the omitted entry is recoverable here.
	regions := decodeRegionsReport(t, callTool(t, regionsTool(registry).Handle, map[string]any{}))
	assert.Len(t, regions.DataSource.Sources, len(sources))
	assert.Equal(t, 0, regions.DataSource.SourcesOmitted)

	published := make([]string, 0, len(regions.DataSource.Sources))
	for _, source := range regions.DataSource.Sources {
		published = append(published, source.URL)
	}

	assert.ElementsMatch(t, []string{sources[0].URL, sources[1].URL}, published)
}

// list_cloud_regions reports its failures in the same structured body as the
// other two tools.
func TestListCloudRegionsErrorContract(t *testing.T) {
	t.Parallel()

	registry := newStubRegistry(offlineProvider(cloud.ProviderGCP, gcpCandidates()))

	for _, test := range []struct {
		name      string
		registry  providerRegistry
		args      map[string]any
		wantCode  cloud.ErrorCode
		wantCloud *string
	}{
		{
			name: "unknown cloud", registry: registry, wantCode: cloud.CodeInvalidArgument,
			args: map[string]any{argCloud: "ibm"}, wantCloud: stringPtr("ibm"),
		},
		{
			name: "undeclared argument", registry: registry, wantCode: cloud.CodeInvalidArgument,
			args: map[string]any{argCloud: "gcp", argRegions: []any{"us-central1"}}, wantCloud: stringPtr("gcp"),
		},
		{
			name: "unregistered cloud", registry: registry, wantCode: cloud.CodeDataUnavailable,
			args: map[string]any{argCloud: "azure"}, wantCloud: stringPtr("azure"),
		},
		{
			name: "acquisition failure", registry: failingAWSStub(errAcquisition),
			wantCode: cloud.CodeInternal, args: map[string]any{}, wantCloud: stringPtr("aws"),
		},
		{
			name: "no provider registry", registry: nil, wantCode: cloud.CodeDataUnavailable,
			args: map[string]any{}, wantCloud: stringPtr("aws"),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			report := decodeError(t, callTool(t, regionsTool(test.registry).Handle, test.args))
			assert.Equal(t, cloud.SchemaVersionErrorV1, report.SchemaVersion)
			assert.Equal(t, test.wantCode, report.Code)
			assert.NotEmpty(t, report.Message)
			assert.Equal(t, test.wantCloud, report.Cloud)
		})
	}
}

// Placement scores survive the seam: with_score fetches them, az splits them
// per zone, and both shapes reach the published document.
func TestListPublishesPlacementScores(t *testing.T) {
	t.Parallel()

	fetchedAt := time.Date(2026, time.August, 6, 8, 58, 27, 0, time.UTC)
	registry := registryOf(testCandidate{
		Machine: "m5.large", Region: "us-west-2", Price: 0.096, Savings: 70,
		RiskLabel: "5-10%", RiskMin: 5, RiskMax: 10, VCPU: 2, MemoryGiB: 8,
		RegionScore: scorePtr(8), ZoneScores: map[string]int{"us-west-2a": 9, "us-west-2b": 7},
		ScoreFetchedAt: &fetchedAt,
	})

	report := decodeListReport(t, callTool(t, listTool(registry).Handle, map[string]any{
		argWithScore: true, argAZ: true, argMinScore: 5,
	}))
	require.Len(t, report.Candidates, 1)

	candidate := report.Candidates[0]
	require.NotNil(t, candidate.RegionScore)
	assert.Equal(t, 8, *candidate.RegionScore)
	assert.Equal(t, map[string]int{"us-west-2a": 9, "us-west-2b": 7}, candidate.ZoneScores)
	require.NotNil(t, candidate.ScoreFetchedAt)
	assert.Equal(t, "2026-08-06T08:58:27Z", *candidate.ScoreFetchedAt)
}

// min_score is an integer floor and only the AWS placement score is an integer
// scale. The MCP surface refuses it against another measurement for the same
// reason the CLI does: reading 8 as a 0.8 probability would be this server
// inventing a correspondence between two vendors' figures.
//
// The refusal runs before acquisition, beside the capability gate.
func TestAnIntegerScoreFloorIsRefusedAgainstAnotherPlacementKind(t *testing.T) {
	t.Parallel()

	capabilities := offlineLinuxCapabilities()
	capabilities.PlacementScore = true
	capabilities.PlacementKind = cloud.PlacementKindObtainability

	provider := stubFor(cloud.ProviderGCP, capabilities, []cloud.Candidate{})
	registry := newStubRegistry(provider)

	result := callTool(t, listTool(registry).Handle, map[string]any{
		argCloud: string(cloud.ProviderGCP), argWithScore: true, argMinScore: 5,
	})

	payload := decodeError(t, result)
	assert.Equal(t, cloud.CodeUnsupportedCapability, payload.Code)
	assert.Contains(t, payload.Message, string(cloud.PlacementKindObtainability),
		"the refusal names the measurement the floor cannot be applied to")
	assert.Zero(t, provider.callCount(), "a refusal costs no acquisition")
}

// scopedSource builds a region-scoped provenance entry.
func scopedSource(url string, region cloud.Region) cloud.SourceRef {
	return cloud.SourceRef{
		FetchedAt:     time.Date(2026, time.August, 6, 8, 58, 27, 0, time.UTC),
		URL:           url,
		ParserVersion: "test/1",
		SchemaVersion: "test/v1",
		Scope:         cloud.SourceScope{Region: region},
	}
}

// awsProviderStub is an AWS provider with candidates in three regions.
func awsProviderStub() *stubProvider {
	provider, _ := awsStub(buildCandidates(
		testCandidate{Region: "us-east-1", Machine: "t3.micro", Price: 0.0104, VCPU: 2, MemoryGiB: 1},
		testCandidate{Region: "us-west-2", Machine: "t3.small", Price: 0.0208, VCPU: 2, MemoryGiB: 2},
		testCandidate{Region: "us-east-1", Machine: "m5.large", Price: 0.0960, VCPU: 2, MemoryGiB: 8},
		testCandidate{Region: "eu-west-1", Machine: "t3.medium", Price: 0.0416, VCPU: 2, MemoryGiB: 4},
	)...)

	return provider
}
