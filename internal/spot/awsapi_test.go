package spot

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeScoresAPI serves canned GetSpotPlacementScores pages, so the response
// handling in fetchScores can be exercised without reaching AWS.
type fakeScoresAPI struct {
	pages   []*ec2.GetSpotPlacementScoresOutput
	err     error
	calls   int
	regions []string
}

func (f *fakeScoresAPI) GetSpotPlacementScores(
	_ context.Context, in *ec2.GetSpotPlacementScoresInput, _ ...func(*ec2.Options),
) (*ec2.GetSpotPlacementScoresOutput, error) {
	if f.err != nil {
		return nil, f.err
	}

	f.calls++
	f.regions = append(f.regions, aws.ToString(in.NextToken))

	page := f.pages[min(f.calls-1, len(f.pages)-1)]

	return page, nil
}

func scoreProviderWith(api ec2.GetSpotPlacementScoresAPIClient) *awsScoreProvider {
	return &awsScoreProvider{
		newClient: func(string) ec2.GetSpotPlacementScoresAPIClient { return api },
	}
}

func TestAWSScoreProviderFetchScores(t *testing.T) {
	t.Parallel()

	types := []string{"m5.large", "c5.large"}

	t.Run("returns the score for the requested region", func(t *testing.T) {
		t.Parallel()

		api := &fakeScoresAPI{pages: []*ec2.GetSpotPlacementScoresOutput{{
			SpotPlacementScores: []ec2types.SpotPlacementScore{
				{Region: aws.String(testRegionUSEast1), Score: aws.Int32(9)},
			},
		}}}

		got, err := scoreProviderWith(api).fetchScores(t.Context(), testRegionUSEast1, types, false)

		require.NoError(t, err)
		assert.Equal(t, []placementScore{{placement: testRegionUSEast1, score: 9}}, got)
	})

	// Regression guard: unscored types used to be back-filled with a literal 5,
	// which is indistinguishable from a real AWS score.
	t.Run("returns nothing when AWS scores nothing", func(t *testing.T) {
		t.Parallel()

		api := &fakeScoresAPI{pages: []*ec2.GetSpotPlacementScoresOutput{{
			SpotPlacementScores: []ec2types.SpotPlacementScore{},
		}}}

		got, err := scoreProviderWith(api).fetchScores(t.Context(), testRegionUSEast1, types, false)

		require.NoError(t, err)
		assert.Empty(t, got, "unscored types must be absent, never defaulted")
	})

	// GetSpotPlacementScores can return a score per Region. Only the requested
	// region's score is meaningful here; taking whichever arrived first would
	// report another region's capacity under this region's name.
	t.Run("uses the requested region's score, not whichever arrived first", func(t *testing.T) {
		t.Parallel()

		api := &fakeScoresAPI{pages: []*ec2.GetSpotPlacementScoresOutput{{
			SpotPlacementScores: []ec2types.SpotPlacementScore{
				{Region: aws.String("us-west-2"), Score: aws.Int32(2)},
				{Region: aws.String(testRegionUSEast1), Score: aws.Int32(9)},
			},
		}}}

		got, err := scoreProviderWith(api).fetchScores(t.Context(), testRegionUSEast1, []string{"m5.large"}, false)

		require.NoError(t, err)
		assert.Equal(t, []placementScore{{placement: testRegionUSEast1, score: 9}}, got,
			"must report the us-east-1 score of 9, not us-west-2's 2")
	})

	t.Run("propagates API errors with the region named", func(t *testing.T) {
		t.Parallel()

		api := &fakeScoresAPI{err: errors.New("AccessDenied")}

		got, err := scoreProviderWith(api).fetchScores(t.Context(), testRegionUSEast1, types, false)

		require.Error(t, err)
		assert.Contains(t, err.Error(), testRegionUSEast1)
		assert.Nil(t, got)
	})

	t.Run("single-AZ requests are forwarded to the API", func(t *testing.T) {
		t.Parallel()

		var gotAZ bool

		api := &recordingScoresAPI{onInput: func(in *ec2.GetSpotPlacementScoresInput) {
			gotAZ = aws.ToBool(in.SingleAvailabilityZone)
		}}

		_, err := scoreProviderWith(api).fetchScores(t.Context(), testRegionUSEast1, types, true)

		require.NoError(t, err)
		assert.True(t, gotAZ, "singleAZ must reach the API call")
	})
}

type recordingScoresAPI struct {
	onInput func(*ec2.GetSpotPlacementScoresInput)
}

func (r *recordingScoresAPI) GetSpotPlacementScores(
	_ context.Context, in *ec2.GetSpotPlacementScoresInput, _ ...func(*ec2.Options),
) (*ec2.GetSpotPlacementScoresOutput, error) {
	r.onInput(in)

	return &ec2.GetSpotPlacementScoresOutput{}, nil
}

// fakeHistoryAPI serves canned DescribeSpotPriceHistory pages.
type fakeHistoryAPI struct {
	out     *ec2.DescribeSpotPriceHistoryOutput
	err     error
	lastIn  *ec2.DescribeSpotPriceHistoryInput
	callCnt int
}

func (f *fakeHistoryAPI) DescribeSpotPriceHistory(
	_ context.Context, in *ec2.DescribeSpotPriceHistoryInput, _ ...func(*ec2.Options),
) (*ec2.DescribeSpotPriceHistoryOutput, error) {
	if f.err != nil {
		return nil, f.err
	}

	f.callCnt++
	f.lastIn = in

	return f.out, nil
}

func livePriceProviderWith(api ec2.DescribeSpotPriceHistoryAPIClient) *awsLivePriceProvider {
	return &awsLivePriceProvider{
		newClient: func(string) ec2.DescribeSpotPriceHistoryAPIClient { return api },
	}
}

func priceEntry(instance, price string) ec2types.SpotPrice {
	return ec2types.SpotPrice{
		InstanceType: ec2types.InstanceType(instance),
		SpotPrice:    aws.String(price),
	}
}

func TestAWSLivePriceProviderFetchLivePrices(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		history []ec2types.SpotPrice
		want    map[string]float64
	}{
		{
			name:    "parses prices per instance type",
			history: []ec2types.SpotPrice{priceEntry("m8g.large", "0.0412"), priceEntry("r8g.large", "0.0567")},
			want:    map[string]float64{"m8g.large": 0.0412, "r8g.large": 0.0567},
		},
		{
			name:    "keeps the first (most recent) price per type",
			history: []ec2types.SpotPrice{priceEntry("m8g.large", "0.0412"), priceEntry("m8g.large", "0.9999")},
			want:    map[string]float64{"m8g.large": 0.0412},
		},
		{
			name:    "skips unparseable prices rather than recording zero",
			history: []ec2types.SpotPrice{priceEntry("m8g.large", "N/A*")},
			want:    map[string]float64{},
		},
		{
			name:    "skips zero prices",
			history: []ec2types.SpotPrice{priceEntry("m8g.large", "0.0")},
			want:    map[string]float64{},
		},
		{
			name:    "tolerates a nil price",
			history: []ec2types.SpotPrice{{InstanceType: "m8g.large"}},
			want:    map[string]float64{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			api := &fakeHistoryAPI{out: &ec2.DescribeSpotPriceHistoryOutput{SpotPriceHistory: tc.history}}

			got, err := livePriceProviderWith(api).fetchLivePrices(
				t.Context(), testRegionUSEast1, []string{"m8g.large", "r8g.large"}, osLinux)

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestAWSLivePriceProviderRequestShape(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		os          string
		wantProduct string
	}{
		{name: "linux maps to Linux/UNIX", os: osLinux, wantProduct: productDescLinux},
		{name: "windows maps to Windows", os: osWindows, wantProduct: productDescWindows},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			api := &fakeHistoryAPI{out: &ec2.DescribeSpotPriceHistoryOutput{}}

			_, err := livePriceProviderWith(api).fetchLivePrices(
				t.Context(), testRegionUSEast1, []string{"m8g.large"}, tc.os)

			require.NoError(t, err)
			require.NotNil(t, api.lastIn)
			assert.Equal(t, []string{tc.wantProduct}, api.lastIn.ProductDescriptions)
			assert.Equal(t, []ec2types.InstanceType{"m8g.large"}, api.lastIn.InstanceTypes)
			assert.NotNil(t, api.lastIn.StartTime, "a start time bounds the history window")
		})
	}
}

func TestAWSLivePriceProviderPropagatesErrors(t *testing.T) {
	t.Parallel()

	api := &fakeHistoryAPI{err: errors.New("UnauthorizedOperation")}

	got, err := livePriceProviderWith(api).fetchLivePrices(
		t.Context(), testRegionUSEast1, []string{"m8g.large"}, osLinux)

	require.Error(t, err)
	assert.Contains(t, err.Error(), testRegionUSEast1)
	assert.Nil(t, got)
}

// The AZ name used to be synthesised as region+"a", so any zone AWS actually
// scored was reported under the wrong name.
func TestAWSScoreProviderUsesAWSZoneIDs(t *testing.T) {
	t.Parallel()

	api := &fakeScoresAPI{pages: []*ec2.GetSpotPlacementScoresOutput{{
		SpotPlacementScores: []ec2types.SpotPlacementScore{
			{AvailabilityZoneId: aws.String("use1-az4"), Score: aws.Int32(7)},
			{AvailabilityZoneId: aws.String("use1-az6"), Score: aws.Int32(3)},
		},
	}}}

	got, err := scoreProviderWith(api).fetchScores(t.Context(), testRegionUSEast1, []string{"m5.large"}, true)

	require.NoError(t, err)
	assert.Equal(t, []placementScore{
		{placement: "use1-az4", score: 7},
		{placement: "use1-az6", score: 3},
	}, got, "zones must come from AWS, not be derived from the region name")
}
