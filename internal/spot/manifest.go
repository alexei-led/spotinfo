package spot

import (
	"embed"
	"fmt"
	"sync"

	"spotinfo/internal/cloud"
	"spotinfo/internal/snapshot"
)

//go:embed data/spot-advisor-manifest.json data/spot-price-manifest.json data/architecture-manifest.json
var manifestFS embed.FS

// Sidecar manifest names. Each one describes the data file next to it and is
// the only place a source URL, fetch time, hash, or coverage floor is recorded.
const (
	advisorManifestFile      = "spot-advisor-manifest.json"
	priceManifestFile        = "spot-price-manifest.json"
	architectureManifestFile = "architecture-manifest.json"
)

// Manifests are parsed once per process: they are small, immutable, and read on
// every client construction.
var (
	advisorManifest      = sync.OnceValues(func() (*snapshot.Manifest, error) { return loadManifest(advisorManifestFile) })
	priceManifest        = sync.OnceValues(func() (*snapshot.Manifest, error) { return loadManifest(priceManifestFile) })
	architectureManifest = sync.OnceValues(func() (*snapshot.Manifest, error) { return loadManifest(architectureManifestFile) })
)

// EmbeddedSourceRefs returns the provenance of every committed AWS snapshot, so
// a result can report which document it came from and when it was fetched.
func EmbeddedSourceRefs() ([]cloud.SourceRef, error) {
	refs := make([]cloud.SourceRef, 0, 3) //nolint:mnd // one per embedded snapshot, listed below.

	for _, load := range []func() (*snapshot.Manifest, error){advisorManifest, priceManifest, architectureManifest} {
		manifest, err := load()
		if err != nil {
			return nil, err
		}

		refs = append(refs, manifest.SourceRefs()...)
	}

	return refs, nil
}

func loadManifest(name string) (*snapshot.Manifest, error) {
	contents, err := manifestFS.ReadFile("data/" + name)
	if err != nil {
		return nil, fmt.Errorf("read embedded manifest %s: %w", name, err)
	}

	manifest, err := snapshot.ParseManifest(contents)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}

	return manifest, nil
}

// advisorCoverage counts what an advisor payload actually carries. Prices stay
// zero: the advisor feed publishes interruption rates and savings percentages,
// not amounts.
func advisorCoverage(data *advisorData) snapshot.Coverage {
	return snapshot.Coverage{Regions: len(data.Regions), Machines: len(data.InstanceTypes)}
}

// priceCoverage counts only usable prices. The feed publishes "N/A*" and 0 for
// machines it does not price, and counting those would let a feed that lost
// every real price still clear its floor. Every dimension is counted distinct,
// matching snapshot.ValidatePrices: the feed already repeats region/size pairs,
// and counting raw cells would let one region's rows repeated enough times clear
// a floor meant to describe the whole matrix.
func priceCoverage(data *rawPriceData) snapshot.Coverage {
	type priceKey struct{ region, machine, os string }

	regions := make(map[string]struct{})
	machines := make(map[string]struct{})
	priced := make(map[priceKey]struct{})

	for _, region := range data.Config.Regions {
		regions[region.Region] = struct{}{}

		for _, instanceType := range region.InstanceTypes {
			for _, size := range instanceType.Sizes {
				machines[size.Size] = struct{}{}

				for _, column := range size.ValueColumns {
					if price, ok := parsePrice(column.Prices.USD); ok && price > 0 {
						priced[priceKey{region: region.Region, machine: size.Size, os: column.Name}] = struct{}{}
					}
				}
			}
		}
	}

	return snapshot.Coverage{Regions: len(regions), Machines: len(machines), Prices: len(priced)}
}

// validateAdvisorCoverage rejects an advisor payload that parsed but lost
// coverage. A live feed returning a truncated document must not replace a
// complete embedded snapshot.
func validateAdvisorCoverage(data *advisorData) error {
	manifest, err := advisorManifest()
	if err != nil {
		return err
	}

	return snapshot.ValidateCoverage(advisorCoverage(data), manifest.MinRecords)
}

func validatePriceCoverage(data *rawPriceData) error {
	manifest, err := priceManifest()
	if err != nil {
		return err
	}

	return snapshot.ValidateCoverage(priceCoverage(data), manifest.MinRecords)
}
