// Package gcp serves Google Cloud Spot VM candidates from a committed snapshot
// of Google's public, server-rendered pricing pages.
//
// The pages render prices for one selected region only — Google switches the
// rest in with JavaScript — so the committed catalogue covers exactly the region
// named in internal/providers/gcp/data/source-contract.json. Every table this
// parser reads is matched to the region its own selector reports; a table
// rendered for another region is skipped rather than relabelled.
//
// Risk is deliberately absent. GCP publishes preemption history only through an
// authenticated beta API, and presenting silence as low risk would let a
// consumer rank it against an AWS interruption bucket.
package gcp

import (
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"

	"spotinfo/internal/cloud"
)

const (
	// ParserVersion identifies the HTML contract below. Bump it whenever a
	// header, a column position, or the region rule changes, so a committed
	// snapshot can never be read as the product of a different parser.
	ParserVersion = "gcp-pricing-html/1"
	// CatalogSchemaVersion versions the committed catalogue shape.
	CatalogSchemaVersion = "spotinfo.gcp-catalog/v1"
)

// The exact header cells the approved pages publish. A renamed header matches
// nothing, which empties the parse and trips the coverage floor — the intended
// failure, rather than a best-effort guess at the new layout.
const (
	headerMachineType = "Machine type"
	headerVCPU        = "Virtual CPUs"
	headerMemory      = "Memory"
	headerSpotPrice   = "Current Spot pricing (USD)"
	// headerOnDemandPrefix is a prefix because the cell carries a trailing
	// consumption-model tooltip whose identifier changes independently of the
	// column it labels.
	headerOnDemandPrefix = "Default* (USD)"
)

// priceColumn is the index of the price cell in both accepted table shapes.
const priceColumn = 3

// Attribute contract of the region selector. Google renders it as an ARIA
// listbox; the selected option carries the human-readable region in data-value.
const (
	attrRole         = "role"
	attrAriaSelected = "aria-selected"
	attrDataValue    = "data-value"
	roleOption       = "option"
	ariaSelectedTrue = "true"
)

// Errors a caller distinguishes. Everything else is an invalid catalogue.
var (
	// ErrSourceContract reports a page that no longer matches the approved
	// parser contract: no region selector, no recognised table, or a row whose
	// cells cannot be read.
	ErrSourceContract = errors.New("gcp pricing page does not match its parser contract")
	// ErrCatalog reports a committed catalogue that contradicts the contract
	// that approved it.
	ErrCatalog = errors.New("invalid gcp catalogue")
)

var (
	// priceCell matches "$0.058121 / 1 hour". The unit is part of the match so a
	// page that switches to monthly pricing fails instead of parsing.
	priceCell = regexp.MustCompile(`^\$(\d+(?:\.\d+)?) / 1 hour$`)
	// memoryCell matches "7 GiB". Only gibibytes are accepted, for the same
	// reason.
	memoryCell = regexp.MustCompile(`^(\d+(?:\.\d+)?) GiB$`)
	// regionSuffix pulls "us-central1" out of "Iowa (us-central1)".
	regionSuffix = regexp.MustCompile(`\(([a-z0-9-]+)\)$`)
	// machineIDPattern is the exact shape of a Compute Engine machine type. It
	// rejects the annotated rows the pages carry, such as
	// "n1-standard-96 Skylake Platform only", which name a platform variant
	// rather than a machine type a user can request.
	machineIDPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)+$`)
)

// armSeries are the Arm-based Compute Engine machine series. The pricing tables
// publish no architecture column, so it comes from this reviewed list — recorded
// as a source in the committed manifest — rather than from a guess about the
// machine name. An unlisted series is x86_64 only because every other series
// Google publishes is; a new Arm series must be added here before its prices
// can ship.
var armSeries = map[string]struct{}{seriesC4A: {}, seriesN4A: {}, seriesT2A: {}}

// The Arm machine series, named so the reviewed list reads as a decision rather
// than three loose strings.
const (
	seriesC4A = "c4a"
	seriesN4A = "n4a"
	seriesT2A = "t2a"
)

// MachineRow is one machine as a pricing page publishes it: identifier,
// specification, and the single price that page's contracted column carries.
type MachineRow struct {
	ID        cloud.MachineID
	Price     cloud.Money
	MemoryGiB float64
	VCPU      int
}

// ParseSpotPage reads Spot prices and machine specifications from the Spot VM
// pricing page, keeping only tables the page rendered for region.
func ParseSpotPage(document io.Reader, region cloud.Region) ([]MachineRow, error) {
	return parsePage(document, region, func(header []string) bool {
		return len(header) == priceColumn+1 &&
			header[0] == headerMachineType && header[1] == headerVCPU &&
			header[2] == headerMemory && header[priceColumn] == headerSpotPrice
	})
}

// ParseOnDemandPage reads the Default (on-demand) price column of a Compute
// pricing category page. Committed-use columns are ignored: they price a
// contract, not an hour of an interruptible machine.
func ParseOnDemandPage(document io.Reader, region cloud.Region) ([]MachineRow, error) {
	return parsePage(document, region, func(header []string) bool {
		return len(header) > priceColumn &&
			header[0] == headerMachineType && header[1] == headerVCPU &&
			header[2] == headerMemory && strings.HasPrefix(header[priceColumn], headerOnDemandPrefix)
	})
}

// parsePage walks a page in document order, tracking which region each table was
// rendered for, and reads the rows of every accepted table in the requested
// region.
func parsePage(document io.Reader, region cloud.Region, accept func([]string) bool) ([]MachineRow, error) {
	root, err := html.Parse(document)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSourceContract, err)
	}

	var (
		rendered  cloud.Region
		selectors int
		matched   int
		rows      []MachineRow
		rowErr    error
	)

	walk(root, func(node *html.Node) {
		if selected, ok := selectedRegion(node); ok {
			rendered, selectors = selected, selectors+1

			return
		}
		if node.Data != "table" || rendered != region || rowErr != nil {
			return
		}

		table := tableRows(node)
		if len(table) < 2 || !accept(table[0]) {
			return
		}
		matched++

		parsed, err := machineRows(table[1:])
		if err != nil {
			rowErr = err

			return
		}
		rows = append(rows, parsed...)
	})

	if rowErr != nil {
		return nil, rowErr
	}
	if selectors == 0 {
		return nil, fmt.Errorf("%w: no region selector, so no table can be attributed to a region", ErrSourceContract)
	}
	if matched == 0 {
		return nil, fmt.Errorf("%w: no machine-type table rendered for region %s", ErrSourceContract, region)
	}

	return rows, nil
}

// machineRows reads the data rows of one accepted table. A row whose first cell
// is not a machine identifier is not a machine row and is skipped; a row that is
// one but whose cells cannot be read is a contract failure.
func machineRows(rows [][]string) ([]MachineRow, error) {
	parsed := make([]MachineRow, 0, len(rows))

	for _, cells := range rows {
		if len(cells) <= priceColumn || !machineIDPattern.MatchString(cells[0]) {
			continue
		}

		row, whole, err := machineRow(cells)
		if err != nil {
			return nil, err
		}
		if !whole {
			continue
		}
		parsed = append(parsed, row)
	}

	return parsed, nil
}

// machineRow reads one machine row. The second result is false for a
// shared-core machine such as f1-micro, whose fraction of a vCPU cannot be
// expressed as the whole-core count every neutral consumer compares against;
// those are left out rather than rounded to a core the machine does not have.
func machineRow(cells []string) (MachineRow, bool, error) {
	// ParseFloat accepts "NaN" and "Inf" without error, and `cores <= 0` catches
	// neither: every comparison against NaN is false, and Inf is positive. Both
	// would then miss the whole-core test below and be dropped from the
	// catalogue as if Google had published a shared-core machine.
	cores, err := strconv.ParseFloat(cells[1], 64)
	if err != nil || math.IsNaN(cores) || math.IsInf(cores, 0) || cores <= 0 {
		return MachineRow{}, false, fmt.Errorf("%w: %s has unreadable vCPU count %q",
			ErrSourceContract, cells[0], cells[1])
	}

	vcpu := int(cores)
	if float64(vcpu) != cores {
		return MachineRow{}, false, nil
	}

	memory := memoryCell.FindStringSubmatch(cells[2])
	if memory == nil {
		return MachineRow{}, false, fmt.Errorf("%w: %s has unreadable memory %q",
			ErrSourceContract, cells[0], cells[2])
	}

	memoryGiB, err := strconv.ParseFloat(memory[1], 64)
	if err != nil || memoryGiB <= 0 {
		return MachineRow{}, false, fmt.Errorf("%w: %s has unreadable memory %q",
			ErrSourceContract, cells[0], cells[2])
	}

	amount := priceCell.FindStringSubmatch(cells[priceColumn])
	if amount == nil {
		return MachineRow{}, false, fmt.Errorf("%w: %s has unreadable hourly price %q",
			ErrSourceContract, cells[0], cells[priceColumn])
	}

	price, err := cloud.ParseMoney(amount[1])
	if err != nil {
		return MachineRow{}, false, fmt.Errorf("%w: %s: %w", ErrSourceContract, cells[0], err)
	}
	if price.IsZero() {
		return MachineRow{}, false, fmt.Errorf("%w: %s is priced at zero", ErrSourceContract, cells[0])
	}

	return MachineRow{ID: cloud.MachineID(cells[0]), VCPU: vcpu, MemoryGiB: memoryGiB, Price: price}, true, nil
}

// selectedRegion reports the region an ARIA listbox currently shows. The
// attribute triple is the contract: a page that stops publishing it fails the
// parse rather than letting every table inherit the wrong region.
func selectedRegion(node *html.Node) (cloud.Region, bool) {
	var role, selected, value string

	for _, attr := range node.Attr {
		switch attr.Key {
		case attrRole:
			role = attr.Val
		case attrAriaSelected:
			selected = attr.Val
		case attrDataValue:
			value = attr.Val
		}
	}

	if role != roleOption || selected != ariaSelectedTrue {
		return "", false
	}

	match := regionSuffix.FindStringSubmatch(value)
	if match == nil {
		return "", false
	}

	return cloud.Region(match[1]), true
}

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

// walk visits every element in document order, which is what lets a table be
// attributed to the region selector rendered above it.
func walk(node *html.Node, visit func(*html.Node)) {
	if node.Type == html.ElementNode {
		visit(node)
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		walk(child, visit)
	}
}

// SeriesOf is the machine series a machine type belongs to: "c4" for
// "c4-standard-2". The series is what the source contract enumerates and what
// the architecture list is keyed by.
func SeriesOf(id cloud.MachineID) string {
	series, _, _ := strings.Cut(string(id), "-")

	return series
}

// ArchitectureOf resolves a machine series to its processor architecture.
func ArchitectureOf(id cloud.MachineID) cloud.Architecture {
	if _, arm := armSeries[SeriesOf(id)]; arm {
		return cloud.ArchitectureARM64
	}

	return cloud.ArchitectureX8664
}
