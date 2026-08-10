package snapshot

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"

	"spotinfo/internal/cloud"
)

// Catalog is the part of a committed provider catalogue this package needs to
// verify it, independent of what the catalogue actually holds.
type Catalog interface {
	// Verify checks the catalogue against the contract that approved its sources.
	Verify(contract *SourceContract) error
	// PriceRecords flattens every published amount for coverage validation.
	PriceRecords() []PriceRecord
}

// EmbeddedSnapshot is a committed catalogue together with the contract that
// approved its sources and the manifest that records where the bytes came from.
type EmbeddedSnapshot[C Catalog] struct {
	Catalog  C
	Manifest *Manifest
	Contract *SourceContract
}

// EmbeddedInput describes one provider's committed snapshot. Everything that
// differs between providers is a field here; the order of the checks is not,
// because that order is the gate.
type EmbeddedInput[C Catalog] struct { //nolint:govet // grouped by role, not by field size
	ContractJSON []byte
	ManifestJSON []byte
	Payload      []byte

	// ErrCatalog is the provider's sentinel for an unusable catalogue.
	ErrCatalog error
	// Decode turns the decompressed bytes into the provider's own catalogue type.
	Decode func([]byte) (C, error)

	// Provider is the cloud this snapshot must belong to.
	Provider cloud.ProviderID
	// ParserVersion is the parser this build speaks. A contract approving a
	// different one is a mismatch, not a snapshot to read anyway.
	ParserVersion string
	// MaxBytes bounds the decompressed catalogue, keeping a corrupted or hostile
	// archive from expanding without limit.
	MaxBytes int
}

// LoadEmbedded reads and fully verifies a committed snapshot.
//
// Every check the update gate ran is repeated here, in the same order: the
// contract must be approved and name this provider and this parser, the manifest
// must be valid, the payload must hash to what the manifest declares, the
// catalogue must stay inside the approved support matrix, and its coverage must
// still meet the reviewed floor. Verification costs one gunzip, which is worth
// paying so a provider is disabled rather than serving a snapshot nobody
// approved.
//
// It lives here rather than in each provider because the order above is the
// gate: when it was written out once per cloud, hardening it meant editing every
// copy and a third provider would have added a third.
func LoadEmbedded[C Catalog](in EmbeddedInput[C]) (*EmbeddedSnapshot[C], error) {
	contract, err := ParseSourceContract(in.ContractJSON)
	if err != nil {
		return nil, fmt.Errorf("%s source contract: %w", in.Provider, err)
	}
	if contract.Provider != in.Provider {
		return nil, fmt.Errorf("%w: contract is for %s, not %s",
			ErrInvalidSourceContract, contract.Provider, in.Provider)
	}
	if contract.ParserVersion != in.ParserVersion {
		return nil, fmt.Errorf("%w: contract approves parser %q, this build is %q",
			ErrContractMismatch, contract.ParserVersion, in.ParserVersion)
	}

	manifest, err := ParseManifest(in.ManifestJSON)
	if err != nil {
		return nil, fmt.Errorf("%s snapshot manifest: %w", in.Provider, err)
	}
	if hashErr := manifest.VerifyPayload(in.Payload); hashErr != nil {
		return nil, hashErr
	}
	if contractErr := contract.VerifySnapshot(manifest, len(in.Payload)); contractErr != nil {
		return nil, contractErr
	}

	data, err := decompress(in.Payload, in.MaxBytes, in.ErrCatalog)
	if err != nil {
		return nil, err
	}

	catalog, err := in.Decode(data)
	if err != nil {
		return nil, err
	}
	if catalogErr := catalog.Verify(contract); catalogErr != nil {
		return nil, catalogErr
	}
	if coverageErr := ValidatePrices(catalog.PriceRecords(), manifest); coverageErr != nil {
		return nil, coverageErr
	}

	return &EmbeddedSnapshot[C]{Catalog: catalog, Manifest: manifest, Contract: contract}, nil
}

// decompress expands a gzipped payload under a ceiling. Hitting the ceiling
// exactly is treated as an overflow: the reader stops there, so the archive may
// hold more and a truncated catalogue must not be verified as a whole one.
func decompress(payload []byte, maxBytes int, errCatalog error) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errCatalog, err)
	}
	defer func() { _ = reader.Close() }()

	data, err := io.ReadAll(io.LimitReader(reader, int64(maxBytes)))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errCatalog, err)
	}
	if len(data) == maxBytes {
		return nil, fmt.Errorf("%w: catalogue hit the %d byte ceiling", errCatalog, maxBytes)
	}

	return data, nil
}
