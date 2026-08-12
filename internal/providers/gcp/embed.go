package gcp

import (
	_ "embed"

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
type Snapshot = snapshot.EmbeddedSnapshot[*Catalog]

// LoadEmbeddedSnapshot reads and fully verifies the committed GCP snapshot.
// snapshot.LoadEmbedded owns the order of those checks, which is the same for
// every cloud; only the inputs below are GCP's.
func LoadEmbeddedSnapshot() (*Snapshot, error) {
	return loadSnapshot(embeddedContract, embeddedManifest, embeddedCatalog)
}

func loadSnapshot(contractJSON, manifestJSON, payload []byte) (*Snapshot, error) {
	return snapshot.LoadEmbedded(snapshot.EmbeddedInput[*Catalog]{
		ContractJSON:  contractJSON,
		ManifestJSON:  manifestJSON,
		Payload:       payload,
		ErrCatalog:    ErrCatalog,
		Decode:        DecodeCatalog,
		Provider:      cloud.ProviderGCP,
		ParserVersion: ParserVersion,
		MaxBytes:      maxCatalogBytes,
	})
}
