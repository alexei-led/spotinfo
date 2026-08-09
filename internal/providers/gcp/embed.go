package gcp

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"fmt"
	"io"

	"spotinfo/internal/cloud"
	"spotinfo/internal/snapshot"
)

// maxCatalogBytes bounds the decompressed catalogue. The committed payload is
// tens of kilobytes; the ceiling keeps a corrupted or hostile archive from
// expanding without limit.
const maxCatalogBytes = 8 << 20

//go:embed data/catalog.json.gz
var embeddedCatalog []byte

//go:embed data/manifest.json
var embeddedManifest []byte

//go:embed data/source-contract.json
var embeddedContract []byte

// Snapshot is a committed catalogue together with the contract that approved
// its sources and the manifest that records where the bytes came from.
type Snapshot struct {
	Catalog  *Catalog
	Manifest *snapshot.Manifest
	Contract *snapshot.SourceContract
}

// LoadEmbeddedSnapshot reads and fully verifies the committed GCP snapshot.
//
// Every check the update gate ran is repeated here, in the same order: the
// contract must be approved, the manifest valid, the payload must hash to what
// the manifest declares, the catalogue must stay inside the approved support
// matrix, and its coverage must still meet the reviewed floor. Verification
// costs a few kilobytes of gunzip, which is worth paying so a provider is
// disabled rather than serving a snapshot nobody approved.
func LoadEmbeddedSnapshot() (*Snapshot, error) {
	return loadSnapshot(embeddedContract, embeddedManifest, embeddedCatalog)
}

func loadSnapshot(contractJSON, manifestJSON, payload []byte) (*Snapshot, error) {
	contract, err := snapshot.ParseSourceContract(contractJSON)
	if err != nil {
		return nil, fmt.Errorf("gcp source contract: %w", err)
	}
	if contract.Provider != cloud.ProviderGCP {
		return nil, fmt.Errorf("%w: %s is not a gcp contract", snapshot.ErrInvalidSourceContract, contract.Provider)
	}
	if contract.ParserVersion != ParserVersion {
		return nil, fmt.Errorf("%w: contract approves parser %q, this build is %q",
			snapshot.ErrContractMismatch, contract.ParserVersion, ParserVersion)
	}

	manifest, err := snapshot.ParseManifest(manifestJSON)
	if err != nil {
		return nil, fmt.Errorf("gcp snapshot manifest: %w", err)
	}
	if hashErr := manifest.VerifyPayload(payload); hashErr != nil {
		return nil, hashErr
	}
	if contractErr := contract.VerifySnapshot(manifest, len(payload)); contractErr != nil {
		return nil, contractErr
	}

	data, err := decompress(payload)
	if err != nil {
		return nil, err
	}

	catalog, err := DecodeCatalog(data)
	if err != nil {
		return nil, err
	}
	if catalogErr := catalog.Verify(contract); catalogErr != nil {
		return nil, catalogErr
	}
	if coverageErr := snapshot.ValidatePrices(catalog.PriceRecords(), manifest); coverageErr != nil {
		return nil, coverageErr
	}

	return &Snapshot{Catalog: catalog, Manifest: manifest, Contract: contract}, nil
}

func decompress(payload []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrCatalog, err)
	}
	defer func() { _ = reader.Close() }()

	data, err := io.ReadAll(io.LimitReader(reader, maxCatalogBytes))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrCatalog, err)
	}
	if len(data) == maxCatalogBytes {
		return nil, fmt.Errorf("%w: catalogue hit the %d byte ceiling", ErrCatalog, maxCatalogBytes)
	}

	return data, nil
}
