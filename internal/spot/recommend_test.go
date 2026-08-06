package spot

import (
	"encoding/json"
	"errors"
	"math"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadEmbeddedArchitectureLookup_ClassifiesAdvisorFamiliesAndReviewedRegressions(t *testing.T) {
	lookup, err := LoadEmbeddedArchitectureLookup()
	require.NoError(t, err)

	advisor, err := loadEmbeddedAdvisorData()
	require.NoError(t, err)
	for instance := range advisor.InstanceTypes {
		_, ok := lookup.ArchitectureForInstance(instance)
		assert.Truef(t, ok, "instance %q must have a reviewed family architecture", instance)
	}

	for _, family := range []string{"g6", "g6e", "g6f", "hpc8a", "m6i"} {
		architecture, ok := lookup.ArchitectureForInstance(family + ".large")
		assert.True(t, ok)
		assert.Equalf(t, ArchitectureX8664, architecture, "%s is x86_64", family)
	}
	for _, family := range []string{"a1", "m6g", "c7g", "g5g", "hpc7g"} {
		architecture, ok := lookup.ArchitectureForInstance(family + ".large")
		assert.True(t, ok)
		assert.Equalf(t, ArchitectureARM64, architecture, "%s is arm64", family)
	}
	_, ok := lookup.ArchitectureForInstance("not-reviewed.large")
	assert.False(t, ok)
}

func TestParseArchitectureSnapshotRejectsInvalidData(t *testing.T) {
	validMetadata := `"provenance":"reviewed manually","reviewed_at":"2026-08-06",`

	_, err := parseArchitectureSnapshot([]byte(`{` + validMetadata + `"families":{"m5":"riscv"}}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid architecture")

	_, err = parseArchitectureSnapshot([]byte(`{` + validMetadata + `"families":{}}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no families")

	for _, contents := range [][]byte{
		[]byte(`{"reviewed_at":"2026-08-06","families":{"m5":"x86_64"}}`),
		[]byte(`{"provenance":"reviewed manually","families":{"m5":"x86_64"}}`),
		[]byte(`{"provenance":"reviewed manually","reviewed_at":"not-a-date","families":{"m5":"x86_64"}}`),
	} {
		_, err = parseArchitectureSnapshot(contents)
		require.Error(t, err)
	}
}

func recommendationOptions() *RecommendationOptions {
	return &RecommendationOptions{
		Architecture: ArchitectureX8664,
		OS:           OperatingSystemLinux,
		CPU:          2,
		Memory:       8,
		Workload:     WorkloadWeb,
		Top:          10,
	}
}

func testLookup() *ArchitectureLookup {
	return &ArchitectureLookup{families: map[string]Architecture{
		"m6i": ArchitectureX8664,
		"m6g": ArchitectureARM64,
	}}
}

func advice(instance string, price float64, cpu int, memory float32, interruption int) Advice {
	return Advice{
		Region: "us-east-1", Instance: instance, Price: price, Savings: 70,
		Info: TypeInfo{Cores: cpu, RAM: memory}, Range: Range{Label: "test", Max: interruption},
	}
}

func TestRecommend_NoCandidatesReturnsSentinel(t *testing.T) {
	recommendations, err := Recommend(nil, recommendationOptions(), testLookup())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNoRecommendationCandidates))
	assert.Nil(t, recommendations)
}

func TestRecommend_ArchitectureAndInstanceUseANDSemantics(t *testing.T) {
	opts := recommendationOptions()
	opts.Instance = `^m6i\.large$`
	advices := []Advice{
		advice("m6i.large", 0.04, 2, 8, 5),
		advice("m6i.xlarge", 0.03, 4, 16, 5),
		advice("m6g.large", 0.01, 2, 8, 5),
	}

	recommendations, err := Recommend(advices, opts, testLookup())
	require.NoError(t, err)
	require.Len(t, recommendations, 1)
	assert.Equal(t, "m6i.large", recommendations[0].Instance)
	assert.Equal(t, []string{"ARCHITECTURE_MATCH", "KNOWN_POSITIVE_PRICE", "MEMORY_MINIMUM_MET", "VCPU_MINIMUM_MET", "WORKLOAD_WEB_CAP_MET"}, recommendations[0].RationaleCodes)
}

func TestRecommend_EnforcesExactResourceAndBudgetBoundariesAndKnownPrices(t *testing.T) {
	opts := recommendationOptions()
	opts.Budget = 0.1
	advices := []Advice{
		advice("m6i.exact", 0.1, 2, 8, 5),
		advice("m6i.over-budget", 0.100001, 2, 8, 5),
		advice("m6i.cpu-low", 0.1, 1, 8, 5),
		advice("m6i.memory-low", 0.1, 2, 7.99, 5),
		advice("m6i.zero", 0, 2, 8, 5),
		advice("m6i.nan", math.NaN(), 2, 8, 5),
		advice("m6i.inf", math.Inf(1), 2, 8, 5),
		advice("m6g.arm", 0.01, 2, 8, 5),
		advice("unknown.large", 0.01, 2, 8, 5),
	}

	recommendations, err := Recommend(advices, opts, testLookup())
	require.NoError(t, err)
	require.Len(t, recommendations, 1)
	assert.Equal(t, "m6i.exact", recommendations[0].Instance)
	assert.Contains(t, recommendations[0].RationaleCodes, "BUDGET_CAP_MET")
}

func TestRecommend_WorkloadBucketBoundaries(t *testing.T) {
	for _, test := range []struct {
		name               string
		workload           Workload
		interruption       int
		wantRecommendation bool
	}{
		{name: "ci accepts Advisor 10-15 bucket", workload: WorkloadCI, interruption: 16, wantRecommendation: true},
		{name: "ci rejects Advisor 15-20 bucket", workload: WorkloadCI, interruption: 22, wantRecommendation: false},
		{name: "batch accepts Advisor 15-20 bucket", workload: WorkloadBatch, interruption: 22, wantRecommendation: true},
		{name: "batch rejects Advisor over-20 bucket", workload: WorkloadBatch, interruption: 100, wantRecommendation: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			opts := recommendationOptions()
			opts.Workload = test.workload
			recommendations, err := Recommend([]Advice{
				advice("m6i.boundary", 0.01, 2, 8, test.interruption),
			}, opts, testLookup())
			if test.wantRecommendation {
				require.NoError(t, err)
				require.Len(t, recommendations, 1)
				assert.Equal(t, "m6i.boundary", recommendations[0].Instance)
				return
			}

			require.ErrorIs(t, err, ErrNoRecommendationCandidates)
			assert.Nil(t, recommendations)
		})
	}
}

func TestRecommendationRankingPolicyIsCanonicalAndImmutable(t *testing.T) {
	assert.Equal(t, recommendationRankingPolicy, RecommendationRankingPolicy())

	policy := RecommendationRankingPolicy()
	policy[0] = "mutated"
	assert.Equal(t, recommendationRankingPolicy, RecommendationRankingPolicy())
}

func TestRecommend_RightsizesAndHasTotalDeterministicTieOrder(t *testing.T) {
	opts := recommendationOptions()
	advices := []Advice{
		advice("m6i.cpu-excess", 0.1, 4, 8, 5),
		advice("m6i.memory-excess", 0.1, 2, 16, 5),
		{Region: "us-west-2", Instance: "m6i.region", Price: 0.1, Info: TypeInfo{Cores: 2, RAM: 8}, Range: Range{Label: "test", Max: 5}},
		{Region: "us-east-1", Instance: "m6i.type-b", Price: 0.1, Info: TypeInfo{Cores: 2, RAM: 8}, Range: Range{Label: "test", Max: 5}},
		{Region: "us-east-1", Instance: "m6i.type-a", Price: 0.1, Info: TypeInfo{Cores: 2, RAM: 8}, Range: Range{Label: "test", Max: 5}},
	}

	recommendations, err := Recommend(advices, opts, testLookup())
	require.NoError(t, err)
	got := make([]string, len(recommendations))
	for i := range recommendations {
		got[i] = recommendations[i].Instance
	}
	assert.Equal(t, []string{"m6i.type-a", "m6i.type-b", "m6i.region", "m6i.memory-excess", "m6i.cpu-excess"}, got)

	opts.Top = 2
	recommendations, err = Recommend(advices, opts, testLookup())
	require.NoError(t, err)
	assert.Len(t, recommendations, 2)
}

func TestRecommend_IsPermutationDeterministic(t *testing.T) {
	opts := recommendationOptions()
	advices := []Advice{
		advice("m6i.b", 0.1, 2, 8, 5),
		advice("m6i.a", 0.1, 2, 8, 5),
	}
	forward, err := Recommend(advices, opts, testLookup())
	require.NoError(t, err)
	slices.Reverse(advices)
	reverse, err := Recommend(advices, opts, testLookup())
	require.NoError(t, err)
	forwardJSON, err := json.Marshal(forward)
	require.NoError(t, err)
	reverseJSON, err := json.Marshal(reverse)
	require.NoError(t, err)
	assert.JSONEq(t, string(forwardJSON), string(reverseJSON))
}

func TestValidateRecommendationOptionsRequiresPositiveSizeAndValidInputs(t *testing.T) {
	for _, mutate := range []func(*RecommendationOptions){
		func(opts *RecommendationOptions) { opts.CPU = 0 },
		func(opts *RecommendationOptions) { opts.Memory = 0 },
		func(opts *RecommendationOptions) { opts.Architecture = "other" },
		func(opts *RecommendationOptions) { opts.OS = "darwin" },
		func(opts *RecommendationOptions) { opts.Budget = -1 },
		func(opts *RecommendationOptions) { opts.Instance = "[" },
	} {
		opts := recommendationOptions()
		mutate(opts)
		err := ValidateRecommendationOptions(opts)
		assert.True(t, errors.Is(err, ErrInvalidRecommendationInput))
	}
}
