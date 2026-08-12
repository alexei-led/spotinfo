package azure

import (
	"net/url"
	"path"
	"regexp"
	"strings"

	"spotinfo/internal/cloud"
)

// The Azure manifest records 81 sources: one Retail Prices query per covered
// region, and one Microsoft Learn page per machine series. A three-row answer
// draws its values from at most three of each, so publishing all 81 would make
// provenance the bulk of the payload and most of it provenance for rows the
// document does not contain.
//
// Nothing in the manifest says which is which — ParseManifest rejects unknown
// fields, so a declared scope would mean rebuilding every committed manifest
// from the network. The scope is derived here instead, from the URL, by the
// package that knows what those URLs mean.
//
// A URL that matches neither shape carries no scope, which covers every
// candidate and is therefore always published. That is the direction this must
// fail in: Microsoft changing a URL shape may cost a larger payload, never a
// row published without the provenance of its own price.

// retailRegion reads the region out of the Retail Prices OData filter. The
// query string spells it `armRegionName eq 'eastus'`, percent- and
// plus-encoded, which url.Values decodes back for us.
var retailRegion = regexp.MustCompile(`armRegionName\s+eq\s+'([^']+)'`)

const (
	retailPricesHost = "prices.azure.com"
	learnHost        = "learn.microsoft.com"
	// learnSizesSegment precedes the family and series segments of a size page.
	learnSizesSegment = "sizes"
	// learnSeriesSuffix ends every Learn size-page path.
	learnSeriesSuffix = "-series"
)

// scopedSources returns the manifest sources with a scope derived from each
// URL, so a published answer can carry the provenance of its own rows.
func scopedSources(sources []cloud.SourceRef, machines []CatalogMachine) []cloud.SourceRef {
	bySeries := make(map[string][]cloud.MachineID, len(machines))
	for i := range machines {
		series := machines[i].Series
		bySeries[series] = append(bySeries[series], machines[i].ID)
	}

	scoped := make([]cloud.SourceRef, len(sources))
	for i := range sources {
		scoped[i] = sources[i]
		scoped[i].Scope = sourceScope(sources[i].URL, bySeries)
	}

	return scoped
}

// sourceScope classifies one source URL. An unrecognised shape yields the zero
// scope, which covers every candidate.
func sourceScope(rawURL string, bySeries map[string][]cloud.MachineID) cloud.SourceScope {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return cloud.SourceScope{}
	}

	switch parsed.Host {
	case retailPricesHost:
		if region := filteredRegion(parsed); region != "" {
			return cloud.SourceScope{Region: cloud.Region(region)}
		}
	case learnHost:
		if machines, documented := bySeries[pageSeries(parsed)]; documented {
			return cloud.SourceScope{Machines: machines}
		}
	}

	return cloud.SourceScope{}
}

// filteredRegion reads the single region a retail query covers. A query that
// filters on no region, or on more than one, is not region-scoped.
func filteredRegion(parsed *url.URL) string {
	values, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return ""
	}

	matches := retailRegion.FindAllStringSubmatch(values.Get("$filter"), -1)
	if len(matches) != 1 {
		return ""
	}

	return matches[0][1]
}

// pageSeries reads the machine series a Learn size page documents. The path
// ends `/sizes/<family>/<series>-series`; anything else is not a size page.
func pageSeries(parsed *url.URL) string {
	family, page := path.Split(path.Clean(parsed.Path))
	if !strings.HasSuffix(page, learnSeriesSuffix) {
		return ""
	}
	if path.Base(path.Dir(strings.TrimSuffix(family, "/"))) != learnSizesSegment {
		return ""
	}

	return strings.TrimSuffix(page, learnSeriesSuffix)
}
