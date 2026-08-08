package cloud

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeProvider answers with fixed candidates. It is deliberately dumb: it
// applies no filter, which is what proves the ranker re-applies every
// constraint rather than trusting the provider.
type fakeProvider struct {
	err          error
	id           ProviderID
	capabilities Capabilities
	sources      []SourceRef
	candidates   []Candidate
	queries      []Query
}

func (p *fakeProvider) ID() ProviderID             { return p.id }
func (p *fakeProvider) Capabilities() Capabilities { return p.capabilities }

func (p *fakeProvider) Query(_ context.Context, query *Query) (Result, error) {
	p.queries = append(p.queries, *query)
	if p.err != nil {
		return Result{}, p.err
	}

	return Result{
		Provider:   p.id,
		Mode:       DataModeEmbeddedSnapshot,
		Sources:    p.sources,
		Candidates: p.candidates,
	}, nil
}

func riskyProvider(candidates ...Candidate) *fakeProvider {
	return &fakeProvider{
		id: ProviderAWS,
		capabilities: Capabilities{
			OperatingSystems: []OperatingSystem{OSLinux, OSWindows},
			Architectures:    []Architecture{ArchitectureX8664, ArchitectureARM64},
			SpotPrice:        true,
			MachineSpec:      true,
			Risk:             true,
		},
		sources:    testSources(),
		candidates: candidates,
	}
}

func riskFreeProvider(candidates ...Candidate) *fakeProvider {
	return &fakeProvider{
		id: ProviderGCP,
		capabilities: Capabilities{
			OperatingSystems: []OperatingSystem{OSLinux},
			Architectures:    []Architecture{ArchitectureX8664, ArchitectureARM64},
			SpotPrice:        true,
			MachineSpec:      true,
		},
		sources:    testSources(),
		candidates: candidates,
	}
}

func testSources() []SourceRef {
	return []SourceRef{{
		FetchedAt:     time.Date(2026, time.August, 6, 8, 58, 27, 0, time.UTC),
		URL:           "https://example.invalid/spot.json",
		ContentSHA256: "f42df66cd52c9dc3ac28b6bb7e525627696eec60692d5cf56658c679f0012393",
		ParserVersion: "test-parser/1",
		SchemaVersion: "test-feed/v1",
	}}
}

// fixture describes one candidate compactly. Absent price and absent risk are
// expressed by leaving them out, which is how the domain models "unknown".
type fixture struct { //nolint:govet // fixture readability beats field packing
	Region       string
	Machine      string
	Architecture Architecture
	OS           OperatingSystem
	Price        string
	VCPU         int
	MemoryGiB    float64
	RiskMax      float64
	RiskKnown    bool
}

func (f fixture) build() Candidate {
	instanceOS := f.OS
	if instanceOS == "" {
		instanceOS = OSLinux
	}
	architecture := f.Architecture
	if architecture == "" {
		architecture = ArchitectureX8664
	}

	candidate := Candidate{
		Provider: ProviderAWS,
		OS:       instanceOS,
		Location: Location{Region: Region(f.Region)},
		Machine: MachineSpec{
			ID: MachineID(f.Machine), Architecture: architecture, MemoryGiB: f.MemoryGiB, VCPU: f.VCPU,
		},
		Risk: UnavailableRisk(),
	}
	if f.Price != "" {
		amount, err := ParseMoney(f.Price)
		if err != nil {
			panic(err)
		}
		candidate.Spot = &PriceObservation{
			Location: candidate.Location, Class: PriceClassSpot,
			Currency: CurrencyUSD, Unit: BillingUnitInstanceHour, Amount: amount,
		}
	}
	if f.RiskKnown {
		low, high := 0.0, f.RiskMax
		candidate.Risk = RiskObservation{
			MinPercent: &low, MaxPercent: &high, Status: RiskStatusAvailable,
			Kind: RiskKindInterruptionFrequencyRange, Label: "test",
			Window: &HistoryWindow{Days: 30},
		}
	}

	return candidate
}

func candidatesOf(fixtures ...fixture) []Candidate {
	candidates := make([]Candidate, 0, len(fixtures))
	for _, f := range fixtures {
		candidates = append(candidates, f.build())
	}

	return candidates
}

// baseRequest is a valid cost-policy request: two vCPU, 4 GiB, x86_64, linux.
func baseRequest() *RecommendRequest {
	return &RecommendRequest{
		Cloud:        ProviderAWS,
		Architecture: ArchitectureX8664,
		OS:           OSLinux,
		Workload:     WorkloadCost,
		Regions:      []Region{RegionAll},
		MinMemoryGiB: 4,
		MinVCPU:      2,
		Top:          DefaultTop,
	}
}

func machines(report *RecommendReport) []string {
	names := make([]string, 0, len(report.Recommendations))
	for _, recommendation := range report.Recommendations {
		names = append(names, string(recommendation.Machine))
	}

	return names
}

func TestValidateRejectsEveryInputOutsideTheContract(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		mutate func(*RecommendRequest)
		want   string
	}{
		{name: "unknown cloud", mutate: func(r *RecommendRequest) { r.Cloud = "ibm" }, want: "unknown cloud provider"},
		{name: "unknown architecture", mutate: func(r *RecommendRequest) { r.Architecture = "riscv64" }, want: "architecture must be"},
		{name: "unknown os", mutate: func(r *RecommendRequest) { r.OS = "plan9" }, want: "os must be"},
		{name: "unknown workload", mutate: func(r *RecommendRequest) { r.Workload = "gaming" }, want: "workload must be"},
		{name: "zero vcpu", mutate: func(r *RecommendRequest) { r.MinVCPU = 0 }, want: "min_vcpu"},
		{name: "zero memory", mutate: func(r *RecommendRequest) { r.MinMemoryGiB = 0 }, want: "min_memory_gib"},
		{name: "zero top", mutate: func(r *RecommendRequest) { r.Top = 0 }, want: "top must be"},
		{name: "top above the ceiling", mutate: func(r *RecommendRequest) { r.Top = MaxTop + 1 }, want: "top must be"},
		{name: "zero budget", mutate: func(r *RecommendRequest) { r.MaxPrice = &Money{} }, want: "max_price_per_hour"},
		{name: "no regions", mutate: func(r *RecommendRequest) { r.Regions = nil }, want: "at least one region"},
		{name: "empty region", mutate: func(r *RecommendRequest) { r.Regions = []Region{" "} }, want: "region must not be empty"},
		{
			name:   "duplicate regions",
			mutate: func(r *RecommendRequest) { r.Regions = []Region{"us-east-1", "us-east-1"} },
			want:   "listed twice",
		},
		{
			name:   "all mixed with an explicit region",
			mutate: func(r *RecommendRequest) { r.Regions = []Region{RegionAll, "us-east-1"} },
			want:   "cannot be combined",
		},
		{name: "invalid machine pattern", mutate: func(r *RecommendRequest) { r.Machine = "m5.[" }, want: "machine pattern"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			request := baseRequest()
			test.mutate(request)

			err := request.Validate()
			require.ErrorIs(t, err, ErrInvalidArgument)
			assert.Contains(t, err.Error(), test.want)
			assert.Equal(t, CodeInvalidArgument, CodeOf(err))
		})
	}
}

// Validation runs before acquisition: an invalid request must never reach a
// provider.
func TestRecommendValidatesBeforeQueryingTheProvider(t *testing.T) {
	t.Parallel()

	provider := riskyProvider()
	request := baseRequest()
	request.MinVCPU = 0

	_, err := Recommend(t.Context(), provider, request)
	require.ErrorIs(t, err, ErrInvalidArgument)
	assert.Empty(t, provider.queries, "an invalid request must cost no acquisition")
}

// A risk-aware workload needs a provider that publishes risk. The shortfall is
// reported before acquisition.
func TestRecommendRejectsARiskAwareWorkloadOnARiskFreeProvider(t *testing.T) {
	t.Parallel()

	for _, workload := range []Workload{WorkloadWeb, WorkloadCI, WorkloadBatch} {
		t.Run(string(workload), func(t *testing.T) {
			t.Parallel()

			provider := riskFreeProvider()
			request := baseRequest()
			request.Cloud = ProviderGCP
			request.Workload = workload

			_, err := Recommend(t.Context(), provider, request)
			require.ErrorIs(t, err, ErrUnsupportedCapability)
			assert.Contains(t, err.Error(), string(CapabilityRisk))
			assert.Empty(t, provider.queries, "a capability shortfall must cost no acquisition")
		})
	}
}

// The cost policy is the one a provider without risk data can serve honestly,
// and it never claims interruption safety.
func TestCostPolicyRanksWithoutRiskAndSaysSo(t *testing.T) {
	t.Parallel()

	provider := riskFreeProvider(candidatesOf(
		fixture{Region: "europe-west1", Machine: "n2-standard-2", Price: "0.021", VCPU: 2, MemoryGiB: 8},
	)...)
	request := baseRequest()
	request.Cloud = ProviderGCP

	report, err := Recommend(t.Context(), provider, request)
	require.NoError(t, err)
	require.Len(t, report.Recommendations, 1)

	recommendation := report.Recommendations[0]
	assert.Equal(t, RiskStatusUnavailable, recommendation.Risk.Status)
	assert.Nil(t, recommendation.Risk.Kind)
	assert.Nil(t, recommendation.Risk.MaxPercent)
	assert.Contains(t, recommendation.RationaleCodes, rationaleCostPolicy)
	for _, code := range recommendation.RationaleCodes {
		assert.NotContains(t, code, "WORKLOAD_", "the cost policy must not claim an interruption cap")
	}
}

// A risk-aware workload drops a candidate whose risk is unknown: ranking it
// would present an unmeasured machine as if it had cleared the cap.
func TestRiskAwareWorkloadsCapInterruptionAndDropUnknownRisk(t *testing.T) {
	t.Parallel()

	candidates := candidatesOf(
		fixture{Region: "us-east-1", Machine: "safe", Price: "0.030", VCPU: 2, MemoryGiB: 8, RiskKnown: true, RiskMax: 5},
		fixture{Region: "us-east-1", Machine: "risky", Price: "0.010", VCPU: 2, MemoryGiB: 8, RiskKnown: true, RiskMax: 20},
		fixture{Region: "us-east-1", Machine: "unmeasured", Price: "0.001", VCPU: 2, MemoryGiB: 8},
	)

	for _, test := range []struct {
		workload Workload
		want     []string
	}{
		{workload: WorkloadWeb, want: []string{"safe"}},
		{workload: WorkloadCI, want: []string{"safe"}},
		{workload: WorkloadBatch, want: []string{"risky", "safe"}},
		{workload: WorkloadCost, want: []string{"unmeasured", "risky", "safe"}},
	} {
		t.Run(string(test.workload), func(t *testing.T) {
			t.Parallel()

			request := baseRequest()
			request.Workload = test.workload

			report, err := Recommend(t.Context(), riskyProvider(candidates...), request)
			require.NoError(t, err)
			assert.Equal(t, test.want, machines(report))
		})
	}
}

// The ranker re-applies every constraint. A provider that ignores a filter must
// not produce a recommendation that violates it.
func TestRankerReappliesEveryConstraint(t *testing.T) {
	t.Parallel()

	kept := fixture{Region: "us-east-1", Machine: "keep.me", Price: "0.030", VCPU: 2, MemoryGiB: 4}

	for _, test := range []struct {
		name    string
		mutate  func(*RecommendRequest)
		dropped fixture
	}{
		{
			name:    "unknown price",
			mutate:  func(*RecommendRequest) {},
			dropped: fixture{Region: "us-east-1", Machine: "drop.me", VCPU: 8, MemoryGiB: 32},
		},
		{
			name:    "architecture",
			mutate:  func(*RecommendRequest) {},
			dropped: fixture{Region: "us-east-1", Machine: "drop.me", Price: "0.001", VCPU: 2, MemoryGiB: 4, Architecture: ArchitectureARM64},
		},
		{
			name:    "operating system",
			mutate:  func(*RecommendRequest) {},
			dropped: fixture{Region: "us-east-1", Machine: "drop.me", Price: "0.001", VCPU: 2, MemoryGiB: 4, OS: OSWindows},
		},
		{
			name:    "minimum vcpu",
			mutate:  func(*RecommendRequest) {},
			dropped: fixture{Region: "us-east-1", Machine: "drop.me", Price: "0.001", VCPU: 1, MemoryGiB: 4},
		},
		{
			name:    "minimum memory",
			mutate:  func(*RecommendRequest) {},
			dropped: fixture{Region: "us-east-1", Machine: "drop.me", Price: "0.001", VCPU: 2, MemoryGiB: 3.9},
		},
		{
			name: "budget",
			mutate: func(r *RecommendRequest) {
				ceiling, err := ParseMoney("0.030")
				if err != nil {
					panic(err)
				}
				r.MaxPrice = &ceiling
			},
			dropped: fixture{Region: "us-east-1", Machine: "drop.me", Price: "0.031", VCPU: 2, MemoryGiB: 4},
		},
		{
			name:    "machine pattern",
			mutate:  func(r *RecommendRequest) { r.Machine = "^keep\\." },
			dropped: fixture{Region: "us-east-1", Machine: "drop.me", Price: "0.001", VCPU: 2, MemoryGiB: 4},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			request := baseRequest()
			test.mutate(request)

			report, err := Recommend(t.Context(), riskyProvider(candidatesOf(test.dropped, kept)...), request)
			require.NoError(t, err)
			assert.Equal(t, []string{"keep.me"}, machines(report))
		})
	}
}

// A machine sitting exactly on both minimums qualifies: the bounds are
// inclusive.
func TestExactResourceBoundsQualify(t *testing.T) {
	t.Parallel()

	report, err := Recommend(t.Context(), riskyProvider(candidatesOf(
		fixture{Region: "us-east-1", Machine: "exact", Price: "0.010", VCPU: 2, MemoryGiB: 4},
	)...), baseRequest())
	require.NoError(t, err)
	assert.Equal(t, []string{"exact"}, machines(report))
}

// A price exactly on the ceiling qualifies; one nano above does not.
func TestBudgetCeilingIsInclusiveToTheNano(t *testing.T) {
	t.Parallel()

	ceiling, err := ParseMoney("0.030")
	require.NoError(t, err)

	request := baseRequest()
	request.MaxPrice = &ceiling

	report, err := Recommend(t.Context(), riskyProvider(candidatesOf(
		fixture{Region: "us-east-1", Machine: "on.budget", Price: "0.030", VCPU: 2, MemoryGiB: 4},
		fixture{Region: "us-east-1", Machine: "over.budget", Price: "0.030000001", VCPU: 2, MemoryGiB: 4},
	)...), request)
	require.NoError(t, err)
	assert.Equal(t, []string{"on.budget"}, machines(report))
	assert.Contains(t, report.Recommendations[0].RationaleCodes, rationaleBudgetCapMet)
}

// The ordering policy in full: price, then right-sizing excess, then region,
// then machine. Every tier is exercised by a pair that ties on the ones above.
func TestRankingPolicyBreaksTiesInTheDocumentedOrder(t *testing.T) {
	t.Parallel()

	report, err := Recommend(t.Context(), riskyProvider(candidatesOf(
		fixture{Region: "us-west-2", Machine: "b.same", Price: "0.020", VCPU: 2, MemoryGiB: 4},
		fixture{Region: "us-west-2", Machine: "a.same", Price: "0.020", VCPU: 2, MemoryGiB: 4},
		fixture{Region: "eu-west-1", Machine: "c.region", Price: "0.020", VCPU: 2, MemoryGiB: 4},
		fixture{Region: "us-east-1", Machine: "d.memory", Price: "0.020", VCPU: 2, MemoryGiB: 8},
		fixture{Region: "us-east-1", Machine: "e.vcpu", Price: "0.020", VCPU: 4, MemoryGiB: 4},
		fixture{Region: "us-east-1", Machine: "f.cheap", Price: "0.010", VCPU: 8, MemoryGiB: 32},
	)...), topOf(baseRequest(), MaxTop))
	require.NoError(t, err)

	assert.Equal(t, []string{"f.cheap", "c.region", "a.same", "b.same", "d.memory", "e.vcpu"}, machines(report))
	assert.Equal(t, []string{
		"spot_price_ascending",
		"excess_vcpu_ascending",
		"excess_memory_gib_ascending",
		"region_ascending",
		"machine_ascending",
	}, report.RankingPolicy)
}

// The comparator is total, so the ranking does not depend on the order
// candidates arrived in.
func TestRankingIsIndependentOfCandidateOrder(t *testing.T) {
	t.Parallel()

	candidates := candidatesOf(
		fixture{Region: "us-east-1", Machine: "a", Price: "0.020", VCPU: 2, MemoryGiB: 4},
		fixture{Region: "us-east-1", Machine: "b", Price: "0.020", VCPU: 2, MemoryGiB: 8},
		fixture{Region: "eu-west-1", Machine: "c", Price: "0.020", VCPU: 2, MemoryGiB: 4},
		fixture{Region: "us-east-1", Machine: "d", Price: "0.010", VCPU: 4, MemoryGiB: 4},
	)

	baseline, err := Recommend(t.Context(), riskyProvider(candidates...), topOf(baseRequest(), MaxTop))
	require.NoError(t, err)

	for _, permutation := range permutations(candidates) {
		report, err := Recommend(t.Context(), riskyProvider(permutation...), topOf(baseRequest(), MaxTop))
		require.NoError(t, err)
		assert.Equal(t, machines(baseline), machines(report))
	}
}

func TestTopLimitsTheResultSetAfterRanking(t *testing.T) {
	t.Parallel()

	request := baseRequest()
	request.Top = 2

	report, err := Recommend(t.Context(), riskyProvider(candidatesOf(
		fixture{Region: "us-east-1", Machine: "third", Price: "0.030", VCPU: 2, MemoryGiB: 4},
		fixture{Region: "us-east-1", Machine: "first", Price: "0.010", VCPU: 2, MemoryGiB: 4},
		fixture{Region: "us-east-1", Machine: "second", Price: "0.020", VCPU: 2, MemoryGiB: 4},
	)...), request)
	require.NoError(t, err)
	assert.Equal(t, []string{"first", "second"}, machines(report))
	assert.Equal(t, []int{1, 2}, []int{report.Recommendations[0].Rank, report.Recommendations[1].Rank})
}

func TestNoMatchingCandidateReportsNoCandidates(t *testing.T) {
	t.Parallel()

	_, err := Recommend(t.Context(), riskyProvider(), baseRequest())
	require.ErrorIs(t, err, ErrNoCandidates)
	assert.Equal(t, CodeNoCandidates, CodeOf(err))
}

func TestRecommendRejectsAProviderThatDoesNotMatchTheRequest(t *testing.T) {
	t.Parallel()

	request := baseRequest()
	request.Cloud = ProviderGCP

	_, err := Recommend(t.Context(), riskyProvider(), request)
	require.ErrorIs(t, err, ErrInvalidArgument)
}

func TestRecommendPropagatesAnAcquisitionFailure(t *testing.T) {
	t.Parallel()

	acquisitionErr := errors.New("snapshot unreadable")
	provider := riskyProvider()
	provider.err = acquisitionErr

	_, err := Recommend(t.Context(), provider, baseRequest())
	require.ErrorIs(t, err, acquisitionErr)
}

// The acquisition query carries the request's own filters, so a provider can
// narrow before mapping.
func TestRequestQueryCarriesTheRequestFilters(t *testing.T) {
	t.Parallel()

	provider := riskyProvider(candidatesOf(
		fixture{Region: "us-east-1", Machine: "m5.large", Price: "0.020", VCPU: 2, MemoryGiB: 4},
	)...)
	request := baseRequest()
	request.Machine = "^m5\\."
	request.Regions = []Region{"us-east-1"}

	_, err := Recommend(t.Context(), provider, request)
	require.NoError(t, err)

	require.Len(t, provider.queries, 1)
	query := provider.queries[0]
	assert.Equal(t, "^m5\\.", query.MachinePattern)
	assert.Equal(t, []Region{"us-east-1"}, query.Regions)
	assert.Equal(t, ArchitectureX8664, query.Architecture)
	assert.Equal(t, OSLinux, query.OS)
	assert.Equal(t, 2, query.MinVCPU)
	assert.InDelta(t, 4.0, query.MinMemoryGiB, 0)
}

func TestRankingPolicyCannotBeMutatedByACaller(t *testing.T) {
	t.Parallel()

	policy := RankingPolicy()
	policy[0] = "mutated"

	assert.NotEqual(t, policy, RankingPolicy())
}

func topOf(request *RecommendRequest, top int) *RecommendRequest {
	request.Top = top

	return request
}

// permutations returns every ordering of a small candidate slice.
func permutations(candidates []Candidate) [][]Candidate {
	if len(candidates) <= 1 {
		return [][]Candidate{slices.Clone(candidates)}
	}

	var result [][]Candidate
	for i := range candidates {
		rest := slices.Concat(candidates[:i:i], candidates[i+1:])
		for _, tail := range permutations(rest) {
			result = append(result, append([]Candidate{candidates[i]}, tail...))
		}
	}

	return result
}
