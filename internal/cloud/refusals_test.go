package cloud

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Three capabilities stay refused on GCP and Azure because no vendor publishes
// what would serve them: Windows on GCP, zone-level prices on either, and the
// risk-capped workloads on either. docs/reviews/multicloud-parity.md §2, §4 and
// §8 are the verdicts.
//
// What is pinned here is the wording, not the refusal — the per-cloud refusals
// are covered where each cloud is tested. A message naming only the capability
// reads as a feature nobody has built yet, and a caller cannot tell from it
// whether to wait for a vendor or to ask for the feature. Each message below
// has to say what the cloud publishes.

// The web, ci and batch ceilings are AWS Spot Advisor bucket boundaries. A kind
// added to this list would be compared against a scale it is not on, and would
// do it silently: the page would rank, the document would validate, and the
// numbers would be wrong. One kind, and a test rather than a consumer finds a
// second one.
//
// TestNoPlacementKindIsEverAcceptedAsInterruptionRisk covers the runtime half —
// feeding a placement kind in as a RiskKind and watching acceptsRisk refuse it.
// This is the declaration itself.
func TestInterruptionCappableKindsHoldsExactlyOneKind(t *testing.T) {
	t.Parallel()

	require.Len(t, interruptionCappableKinds, 1,
		"a second cappable kind means a workload ceiling is applied to a measurement it was not drawn from")
	assert.Equal(t, []RiskKind{RiskKindInterruptionFrequencyRange}, interruptionCappableKinds)
	assert.NotContains(t, interruptionCappableKinds, RiskKindPreemptionRate,
		"google divides preempted spots by spots that stopped running; aws publishes the fraction of running instances interrupted")
}

// Windows on GCP: Google's Spot pricing pages publish no Windows Spot line. The
// message names the operating systems the cloud is priced for, which is read
// from the declaration — so a cloud that gains one retires its own wording.
func TestARefusedOperatingSystemNamesWhatTheCloudPrices(t *testing.T) {
	t.Parallel()

	err := linuxSpotOnly().Require(CapabilityRequest{OS: OSWindows})

	require.ErrorIs(t, err, ErrUnsupportedCapability)
	assert.Equal(t, CodeUnsupportedCapability, CodeOf(err))
	assert.Contains(t, err.Error(), "os windows")
	assert.Contains(t, err.Error(), "publishes spot prices for linux only",
		"the message must say what is priced, not only that windows is not")
}

// Zone-level prices: the Azure Retail Prices API publishes region-level amounts
// and Google publishes Spot prices per region. Both vendors' placement APIs do
// accept a zone, though, so the message may not claim nothing is published per
// zone — it separates the vendor limit on prices from the build limit on the
// rest.
func TestARefusedZoneRequestNamesThePriceGranularity(t *testing.T) {
	t.Parallel()

	err := linuxSpotOnly().Require(CapabilityRequest{Needed: []Capability{CapabilityZoneDetail}})

	require.ErrorIs(t, err, ErrUnsupportedCapability)
	assert.Equal(t, CodeUnsupportedCapability, CodeOf(err))
	assert.Contains(t, err.Error(), string(CapabilityZoneDetail))
	assert.Contains(t, err.Error(), "prices per region, not per zone")
	assert.NotContains(t, err.Error(), "no zone",
		"both placement apis accept a zone; only the price granularity is a vendor limit")
}

// A risk-capped workload on a cloud that publishes no comparable figure. The
// bare capability name would read as missing data; what is missing is a
// measurement the ceiling can be applied to at all, which no vendor publishes
// and neither deferred Azure feature would supply.
func TestARiskCappedWorkloadIsRefusedWithTheMeasurementThatCapsIt(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		workload Workload
		ceiling  string
	}{
		{workload: WorkloadWeb, ceiling: "5%"},
		{workload: WorkloadCI, ceiling: "16%"},
		{workload: WorkloadBatch, ceiling: "22%"},
	} {
		t.Run(string(test.workload), func(t *testing.T) {
			t.Parallel()

			provider := riskFreeProvider()
			request := baseRequest()
			request.Cloud = ProviderGCP
			request.Workload = test.workload

			_, err := Recommend(t.Context(), provider, request)

			require.ErrorIs(t, err, ErrUnsupportedCapability)
			assert.Equal(t, CodeUnsupportedCapability, CodeOf(err))
			assert.Contains(t, err.Error(), string(ProviderGCP))
			assert.Contains(t, err.Error(), test.ceiling)
			assert.Contains(t, err.Error(), "AWS Spot Advisor bucket boundary",
				"the ceiling comes from one vendor's measurement; that is why it does not transfer")
			assert.Contains(t, err.Error(), "publishes no figure measured that way")
			assert.Contains(t, err.Error(), "workload "+string(WorkloadCost),
				"a refusal that names no way forward sends the reader to the source")
			assert.Empty(t, provider.queries, "a refusal must cost no acquisition")
		})
	}
}

// The cost policy caps nothing, so the refusal above must not fire for it — on
// any cloud, including one that publishes no risk at all.
func TestTheCostWorkloadIsNeverRefusedForRisk(t *testing.T) {
	t.Parallel()

	riskFree := riskFreeProvider().Capabilities()

	require.NoError(t, refuseUncappableWorkload(ProviderGCP, riskFree, WorkloadCost))
	require.Error(t, refuseUncappableWorkload(ProviderGCP, riskFree, WorkloadWeb))
	require.NoError(t, refuseUncappableWorkload(ProviderAWS, riskyProvider().Capabilities(), WorkloadWeb))
}

// Every capability has a reviewed limit to state. A new one added without one
// would fall back to a message that says nothing, which is the state this task
// removed.
func TestEveryCapabilityStatesItsLimit(t *testing.T) {
	t.Parallel()

	for _, capability := range []Capability{
		CapabilitySpotPrice, CapabilityOnDemandPrice, CapabilityMachineSpec, CapabilityRisk,
		CapabilityPlacementScore, CapabilityZoneDetail, CapabilityLiveEnrichment,
	} {
		assert.Contains(t, capabilityLimits, capability,
			"%s is refusable, so it needs a reviewed reason", capability)
	}

	assert.Equal(t, "this cloud does not declare it", capabilityLimit("teleportation"),
		"an unreviewed capability must not have a vendor fact invented for it")
}
