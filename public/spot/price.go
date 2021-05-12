package spot

import (
	_ "embed" //nolint:gci
	"encoding/json"
	"io/ioutil"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"
)

var (
	loadPriceOnce sync.Once
	//go:embed data/spot-price-data.json
	embeddedPriceData string
	// spot pricing data
	spotPrice *spotPriceData
	// aws region map: map between non-standard codes in spot pricing JS and AWS region code
	awsSpotPricingRegions = map[string]string{
		"us-east":    "us-east-1",
		"us-west":    "us-west-1",
		"eu-ireland": "eu-west-1",
		"apac-sin":   "ap-southeast-1",
		"apac-syd":   "ap-southeast-2",
		"apac-tokyo": "ap-northeast-1",
	}
)

const (
	responsePrefix = "callback("
	responseSuffix = ");"
	spotPriceJsURL = "https://spot-price.s3.amazonaws.com/spot.js"
)

type rawPriceData struct {
	Embedded bool // true if loaded from embedded copy
	Config   struct {
		Rate         string   `json:"rate"`
		ValueColumns []string `json:"valueColumns"`
		Currencies   []string `json:"currencies"`
		Regions      []struct {
			Region        string `json:"region"`
			InstanceTypes []struct {
				Type  string `json:"type"`
				Sizes []struct {
					Size         string `json:"size"`
					ValueColumns []struct {
						Name   string `json:"name"`
						Prices struct {
							USD string `json:"USD"` //nolint:tagliatelle
						} `json:"prices"`
					} `json:"valueColumns"`
				} `json:"sizes"`
			} `json:"instanceTypes"`
		} `json:"regions"`
	} `json:"config"`
}

type instancePrice struct {
	linux   float64
	windows float64
}
type regionPrice struct {
	instance map[string]instancePrice
}
type spotPriceData struct {
	region map[string]regionPrice
}

func pricingLazyLoad(url string, timeout time.Duration, fallbackData string, embedded bool) (*rawPriceData, error) {
	var (
		result     rawPriceData
		bodyBytes  []byte
		bodyString string
		client     *http.Client
		resp       *http.Response
		err        error
	)
	// load embedded data if asked explicitly
	if embedded {
		goto fallback
	}
	// try to load new data
	client = &http.Client{Timeout: timeout}

	resp, err = client.Get(url)
	if err != nil {
		goto fallback
	}

	defer func() {
		err = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		goto fallback
	}

	// get response as text and trim JS code
	bodyBytes, err = ioutil.ReadAll(resp.Body)
	if err != nil {
		goto fallback
	}

	bodyString = strings.TrimPrefix(string(bodyBytes), responsePrefix)
	bodyString = strings.TrimSuffix(bodyString, responseSuffix)

	err = json.Unmarshal([]byte(bodyString), &result)
	if err != nil {
		goto fallback
	}

	goto process

fallback: // fallback to embedded load

	if err = json.Unmarshal([]byte(fallbackData), &result); err != nil {
		return nil, errors.Wrapf(err, "failed to parse embedded spot price data")
	}

	// set embedded loaded flag true
	result.Embedded = true

process: // process loaded result
	// replace non-standard Spot pricing region codes with AWS region codes
	for index, r := range result.Config.Regions {
		if awsRegion, ok := awsSpotPricingRegions[r.Region]; ok {
			result.Config.Regions[index].Region = awsRegion
		}
	}

	return &result, nil
}

func convertRawData(raw *rawPriceData) *spotPriceData {
	// fill priceData from rawPriceData
	var pricing spotPriceData
	pricing.region = make(map[string]regionPrice)

	for _, region := range raw.Config.Regions {
		var rp regionPrice
		rp.instance = make(map[string]instancePrice)

		for _, it := range region.InstanceTypes {
			for _, size := range it.Sizes {
				var ip instancePrice

				for _, os := range size.ValueColumns {
					price, err := strconv.ParseFloat(os.Prices.USD, 64)
					if err != nil {
						price = 0
					}

					if os.Name == "mswin" {
						ip.windows = price
					} else {
						ip.linux = price
					}
				}

				rp.instance[size.Size] = ip
			}
		}

		pricing.region[region.Region] = rp
	}

	return &pricing
}

func getSpotInstancePrice(instance, region, os string, embedded bool) (float64, error) {
	var (
		err  error
		data *rawPriceData
	)

	loadPriceOnce.Do(func() {
		const timeout = 10
		data, err = pricingLazyLoad(spotPriceJsURL, timeout*time.Second, embeddedPriceData, embedded)
		spotPrice = convertRawData(data)
	})

	if err != nil {
		return 0, errors.Wrap(err, "failed to load spot instance pricing")
	}

	rp, ok := spotPrice.region[region]
	if !ok {
		return 0, errors.Errorf("no pricind fata for region: %v", region)
	}

	price, ok := rp.instance[instance]
	if !ok {
		return 0, errors.Errorf("no pricind fata for instance: %v", instance)
	}

	if os == "windows" {
		return price.windows, nil
	}

	return price.linux, nil
}
