package azure

import (
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"

	"spotinfo/internal/cloud"
)

// The exact header cells a Microsoft Learn size page publishes for its
// specification table. A renamed header matches nothing, which empties the parse
// and trips the coverage floor — the intended failure, rather than a guess at the
// new layout.
const (
	headerSizeName = "Size Name"
	headerVCPU     = "vCPUs (Qty.)"
	// headerMemoryGiB and headerMemoryGB are the same column under two labels.
	// The general-purpose and compute-optimized pages write "GiB"; every
	// memory-optimized page writes "GB". The figures are identical for equivalent
	// sizes — Standard_E2_v5 reads 16 on the "GB" page and Standard_E2s_v5 reads
	// 16 on the "GiB" one — so this is Microsoft labelling one quantity two ways,
	// not two units. Both are read as gibibytes; a third label is a contract
	// failure rather than a third guess.
	headerMemoryGiB = "Memory (GiB)"
	headerMemoryGB  = "Memory (GB)"
)

// specColumns is the width of the specification table: name, vCPUs, memory.
const specColumns = 3

// partProcessor is the row label of the parts table that carries the processor
// models, and with them the architecture marker.
const partProcessor = "Processor"

// seriesSuffix is the trailing path segment every approved size page ends with.
const seriesSuffix = "-series"

// sizeNamePrefix is what makes a specification-table row a size row, whether or
// not this parser can read the rest of the name.
const sizeNamePrefix = "Standard_"

var (
	// sizeName is the exact shape of an Azure VM size identifier. It is the same
	// string the Retail Prices API publishes as armSkuName, which is what lets the
	// two sources be joined at all.
	sizeName = regexp.MustCompile(`^Standard_[A-Za-z0-9]+(?:_[A-Za-z0-9]+)*$`)
	// architectureMarker matches the bracketed processor architecture Learn
	// publishes next to each processor model. The spelling is not stable across
	// pages — "[x86-64]", "[Arm64]" and "[ARM-64]" are all in use today — so the
	// marker is normalised rather than compared literally.
	architectureMarker = regexp.MustCompile(`(?i)\[(x86[-_]?64|arm[-_]?64)]`)
	// seriesPath pulls "dv5" out of ".../general-purpose/dv5-series".
	seriesPath = regexp.MustCompile(`^[a-z][a-z0-9]*$`)
)

// SeriesSpec is one machine series as its Learn page publishes it: a processor
// architecture shared by every size, and the sizes themselves.
type SeriesSpec struct {
	Series       string
	Architecture cloud.Architecture
	Sizes        []SizeSpec
}

// SizeSpec is one VM size's specification.
type SizeSpec struct {
	ID        cloud.MachineID
	MemoryGiB float64
	VCPU      int
}

// ParseSeriesPage reads the architecture and size specifications from one
// approved Microsoft Learn size page.
//
// Architecture comes from the page's own processor row, never from the size
// name. An Arm size published as x86_64 would pass every other gate in this
// repository — coverage, price sanity, schema — and silently return a machine
// that cannot run the caller's binaries, so an unmarked or self-contradicting
// page fails the parse instead of defaulting.
func ParseSeriesPage(document io.Reader, series string) (*SeriesSpec, error) {
	if !seriesPath.MatchString(series) {
		return nil, fmt.Errorf("%w: %q is not a machine series", ErrSourceContract, series)
	}

	root, err := html.Parse(document)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSourceContract, err)
	}

	var (
		markers []string
		sizes   []SizeSpec
		rowErr  error
	)

	walk(root, func(node *html.Node) {
		if node.Data != "table" || rowErr != nil {
			return
		}

		rows := tableRows(node)
		markers = append(markers, processorMarkers(rows)...)

		if len(rows) < 2 || !isSpecHeader(rows[0]) {
			return
		}

		parsed, parseErr := sizeRows(series, rows[1:])
		if parseErr != nil {
			rowErr = parseErr

			return
		}
		sizes = append(sizes, parsed...)
	})

	if rowErr != nil {
		return nil, rowErr
	}

	architecture, err := resolveArchitecture(series, markers)
	if err != nil {
		return nil, err
	}

	if len(sizes) == 0 {
		return nil, fmt.Errorf("%w: %s page publishes no size table", ErrSourceContract, series)
	}

	return &SeriesSpec{Series: series, Architecture: architecture, Sizes: sizes}, nil
}

// isSpecHeader recognises the specification table by its exact first three
// header cells. Later columns differ between series and are not read.
func isSpecHeader(header []string) bool {
	return len(header) >= specColumns &&
		header[0] == headerSizeName && header[1] == headerVCPU &&
		(header[2] == headerMemoryGiB || header[2] == headerMemoryGB)
}

// processorMarkers collects the architecture markers from a parts table's
// processor row. A series with several processor models lists a marker per
// model.
func processorMarkers(rows [][]string) []string {
	var markers []string

	for _, cells := range rows {
		if len(cells) == 0 || cells[0] != partProcessor {
			continue
		}

		for _, match := range architectureMarker.FindAllStringSubmatch(strings.Join(cells, " "), -1) {
			markers = append(markers, normalizeArchitecture(match[1]))
		}
	}

	return markers
}

// resolveArchitecture requires the page to say exactly one thing. No marker and
// two different markers are both failures: neither leaves a defensible answer.
func resolveArchitecture(series string, markers []string) (cloud.Architecture, error) {
	if len(markers) == 0 {
		return "", fmt.Errorf("%w: %s page publishes no processor architecture marker", ErrSourceContract, series)
	}

	for _, marker := range markers[1:] {
		if marker != markers[0] {
			return "", fmt.Errorf("%w: %s page mixes %s and %s processors",
				ErrSourceContract, series, markers[0], marker)
		}
	}

	switch markers[0] {
	case string(cloud.ArchitectureARM64):
		return cloud.ArchitectureARM64, nil
	case string(cloud.ArchitectureX8664):
		return cloud.ArchitectureX8664, nil
	default:
		return "", fmt.Errorf("%w: %s page publishes unknown architecture %q",
			ErrSourceContract, series, markers[0])
	}
}

// normalizeArchitecture folds the published spellings onto the neutral
// vocabulary: "x86-64" and "x86_64" both mean x86_64, "Arm64" and "ARM-64" both
// mean arm64.
func normalizeArchitecture(marker string) string {
	folded := strings.ToLower(marker)
	if strings.HasPrefix(folded, "arm") {
		return string(cloud.ArchitectureARM64)
	}

	return string(cloud.ArchitectureX8664)
}

// sizeRows reads the data rows of the specification table. A row whose first
// cell is not a size identifier is not a size row and is skipped; a row that is
// one but whose numbers cannot be read is a contract failure.
func sizeRows(series string, rows [][]string) ([]SizeSpec, error) {
	parsed := make([]SizeSpec, 0, len(rows))

	for _, cells := range rows {
		if len(cells) == 0 || !strings.HasPrefix(cells[0], sizeNamePrefix) {
			continue
		}

		// The prefix decides that this is a size row, so a short row is a page
		// that dropped or reordered a contracted column — not a row to skip, which
		// would quietly publish a catalogue missing those sizes.
		if len(cells) < specColumns {
			return nil, fmt.Errorf("%w: %s in %s has %d columns, not the %d this parser reads",
				ErrSourceContract, cells[0], series, len(cells), specColumns)
		}

		// A cell that starts with the size prefix is a size row, so a name this
		// parser cannot read is a contract failure rather than a row to skip.
		// Azure's constrained-vCPU names carry a hyphen ("Standard_E32-8as_v5");
		// no contracted page lists one today, and if one starts to, the refresh
		// must say so rather than quietly publishing a shorter catalogue.
		if !sizeName.MatchString(cells[0]) {
			return nil, fmt.Errorf("%w: %q in %s is not a size name this parser reads",
				ErrSourceContract, cells[0], series)
		}

		vcpu, err := strconv.Atoi(cells[1])
		if err != nil || vcpu <= 0 {
			return nil, fmt.Errorf("%w: %s in %s has unreadable vCPU count %q",
				ErrSourceContract, cells[0], series, cells[1])
		}

		// ParseFloat accepts "NaN" and "Inf" without error, and `memoryGiB <= 0`
		// catches neither: every comparison against NaN is false, and Inf is
		// positive. Both would reach the encoder and fail the refresh there,
		// naming the JSON value instead of the page cell that produced it.
		memoryGiB, err := strconv.ParseFloat(cells[2], 64)
		if err != nil || math.IsNaN(memoryGiB) || math.IsInf(memoryGiB, 0) || memoryGiB <= 0 {
			return nil, fmt.Errorf("%w: %s in %s has unreadable memory %q",
				ErrSourceContract, cells[0], series, cells[2])
		}

		parsed = append(parsed, SizeSpec{ID: cloud.MachineID(cells[0]), VCPU: vcpu, MemoryGiB: memoryGiB})
	}

	return parsed, nil
}

// SeriesFromURL is the series a contracted size page documents, taken from its
// last path segment: "dv5" for ".../general-purpose/dv5-series".
func SeriesFromURL(pageURL string) (string, error) {
	trimmed := strings.TrimSuffix(pageURL, "/")
	segment := trimmed[strings.LastIndex(trimmed, "/")+1:]

	series, found := strings.CutSuffix(segment, seriesSuffix)
	if !found || !seriesPath.MatchString(series) {
		return "", fmt.Errorf("%w: %q is not a %s page", ErrSourceContract, pageURL, seriesSuffix)
	}

	return series, nil
}

// The HTML walking helpers below are deliberately duplicated from
// internal/providers/gcp: two provider packages reading unrelated table
// contracts share only these three functions, and a package created to hold them
// would add a dependency edge for forty lines. Extract them the first time a
// third provider parses HTML.

// tableRows flattens one table into rows of collapsed cell text.
func tableRows(table *html.Node) [][]string {
	var rows [][]string

	walk(table, func(node *html.Node) {
		if node.Data != "tr" {
			return
		}

		var cells []string
		walk(node, func(cell *html.Node) {
			if cell.Data == "th" || cell.Data == "td" {
				cells = append(cells, cellText(cell))
			}
		})
		rows = append(rows, cells)
	})

	return rows
}

// cellText joins a cell's text nodes without separators and collapses runs of
// whitespace, which is how the published header and value strings read.
func cellText(node *html.Node) string {
	var builder strings.Builder

	var collect func(*html.Node)
	collect = func(current *html.Node) {
		if current.Type == html.TextNode {
			builder.WriteString(current.Data)
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			collect(child)
		}
	}
	collect(node)

	return strings.Join(strings.Fields(builder.String()), " ")
}

// walk visits every element in document order.
func walk(node *html.Node, visit func(*html.Node)) {
	if node.Type == html.ElementNode {
		visit(node)
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		walk(child, visit)
	}
}
