package main

import "strings"

// The flag vocabulary: one name per concept, carried by both commands unless the
// concept belongs to only one of them. Every name is decided here and nowhere
// else, and the tables below are data the tests read — vocabulary_test.go walks
// the command tree built by newSpotinfoApp and asserts each command declares
// exactly the entries marked for it.
//
// Derivation rule, MCP argument from CLI flag: strip the leading "--" and
// replace "-" with "_". The one exception — a repeatable CLI flag becomes a
// plural JSON array — lives in pluralMCPArgs as data rather than as a branch in
// mcpArgName, so a second repeatable flag is one line here instead of a second
// special case.

// CLI flag names. One per concept.
const (
	flagCloud         = "cloud"
	flagRegion        = "region"
	flagMachine       = "machine"
	flagArchitecture  = "architecture"
	flagOS            = "os"
	flagMinVCPU       = "min-vcpu"
	flagMinMemoryGiB  = "min-memory-gib"
	flagMaxPrice      = "max-price"
	flagWorkload      = "workload"
	flagTop           = "top"
	flagSort          = "sort"
	flagOrder         = "order"
	flagOutput        = "output"
	flagOffline       = "offline"
	flagRefresh       = "refresh"
	flagWithScore     = "with-score"
	flagMinScore      = "min-score"
	flagAZ            = "az"
	flagScoreTimeout  = "score-timeout"
	flagLiveRisk      = "live-risk"
	flagGCPProject    = "gcp-project"
	flagGCPBillingKey = "gcp-billing-key"
)

// Retired names. Each one expressed a concept another name already expresses,
// and each maps to exactly one surviving flag through renamedFlags. They are
// still declared on the command tree while the migration runs; a caller who
// passes one gets a rename hint, never "flag provided but not defined".
const (
	flagType      = "type"
	flagInstance  = "instance"
	flagVCPU      = "vcpu"
	flagCPU       = "cpu"
	flagMemory    = "memory"
	flagMemoryGiB = "memory-gib"
	flagPrice     = "price"
	flagBudget    = "budget"
)

// Unified defaults. One value per flag, identical on the CLI and over MCP: the
// same question asked either way must return the same document, and a default
// that differs by surface is the one way that stops being true.
const (
	// defaultWorkload is the only ranking policy every cloud accepts, and it
	// makes no interruption claim.
	defaultWorkload = "cost"
	// defaultRegion is every published region: cross-region comparison is the
	// tool's value, and it is already the GCP, Azure and MCP default.
	defaultRegion = allRegions
	defaultTop    = 3
	defaultOS     = "linux"
	defaultOutput = outputTable
)

// commandSet marks which of the two commands carry a concept.
type commandSet uint8

const (
	onList commandSet = 1 << iota
	onRecommend

	onBoth = onList | onRecommend
)

// has reports whether this set includes any of the given commands.
func (s commandSet) has(other commandSet) bool { return s&other != 0 }

// vocabularyEntry is one row of the vocabulary table.
type vocabularyEntry struct {
	// concept is what the flag names, stated once so two flags cannot drift
	// into meaning the same thing.
	concept string
	flag    string
	// mcpArg is the MCP argument name, empty when the concept has none. It is
	// written out rather than computed so mcpArgName can be checked against the
	// reviewed table instead of against itself.
	mcpArg     string
	commands   commandSet
	repeatable bool
}

// vocabulary is the single source for every flag name on both commands.
//
// constants on purpose: it is the reviewed answer that mcpArgName is checked
// against, and deriving it here would leave the derivation checked against
// itself.
//
//nolint:goconst // The MCP column is spelled out rather than built from the flag
var vocabulary = []vocabularyEntry{
	{concept: "Cloud", flag: flagCloud, mcpArg: "cloud", commands: onBoth},
	{concept: "Region", flag: flagRegion, mcpArg: "regions", commands: onBoth, repeatable: true},
	{concept: "Machine-name filter", flag: flagMachine, mcpArg: "machine", commands: onBoth},
	{concept: "Architecture", flag: flagArchitecture, mcpArg: "architecture", commands: onBoth},
	{concept: "Operating system", flag: flagOS, mcpArg: "os", commands: onBoth},
	{concept: "Minimum vCPU", flag: flagMinVCPU, mcpArg: "min_vcpu", commands: onBoth},
	{concept: "Minimum memory", flag: flagMinMemoryGiB, mcpArg: "min_memory_gib", commands: onBoth},
	{concept: "Price ceiling", flag: flagMaxPrice, mcpArg: "max_price", commands: onBoth},
	{concept: "Workload policy", flag: flagWorkload, mcpArg: "workload", commands: onRecommend},
	{concept: "Result count", flag: flagTop, mcpArg: "top", commands: onRecommend},
	{concept: "Sort key", flag: flagSort, mcpArg: "sort", commands: onBoth},
	{concept: "Sort order", flag: flagOrder, mcpArg: "order", commands: onBoth},
	// No MCP argument: MCP is always JSON.
	{concept: "Output format", flag: flagOutput, commands: onBoth},
	{concept: "Snapshot only", flag: flagOffline, mcpArg: "offline", commands: onBoth},
	{concept: "Ignore cache", flag: flagRefresh, mcpArg: "refresh", commands: onBoth},
	{concept: "Placement scores", flag: flagWithScore, mcpArg: "with_score", commands: onBoth},
	{concept: "Minimum score", flag: flagMinScore, mcpArg: "min_score", commands: onBoth},
	{concept: "Zone detail", flag: flagAZ, mcpArg: "az", commands: onBoth},
	{concept: "Score timeout", flag: flagScoreTimeout, mcpArg: "score_timeout", commands: onBoth},
	{concept: "Live risk", flag: flagLiveRisk, mcpArg: "live_risk", commands: onRecommend},
	// No MCP argument: credentials are read from the environment on that
	// surface, never taken from a tool argument.
	{concept: "GCP project", flag: flagGCPProject, commands: onBoth},
	{concept: "GCP billing key", flag: flagGCPBillingKey, commands: onBoth},
}

// defaultEntry is one row of the unified-defaults table. Its commands column
// must agree with the vocabulary entry for the same flag, and a test reads both
// so a disagreement fails here rather than shipping.
type defaultEntry struct {
	value    any
	flag     string
	commands commandSet
}

// unifiedDefaults holds every flag that has a default. A flag absent from this
// table has none: it is off, or unset, until the caller says otherwise.
var unifiedDefaults = []defaultEntry{
	{flag: flagWorkload, value: defaultWorkload, commands: onRecommend},
	{flag: flagRegion, value: defaultRegion, commands: onBoth},
	{flag: flagTop, value: defaultTop, commands: onRecommend},
	{flag: flagOS, value: defaultOS, commands: onBoth},
	{flag: flagOutput, value: defaultOutput, commands: onBoth},
}

// pluralMCPArgs is the derivation rule's one exception, held as data: a
// repeatable CLI flag becomes a plural JSON array. Every key must be a
// repeatable vocabulary entry and every repeatable entry must appear here — a
// test asserts both directions.
var pluralMCPArgs = map[string]string{
	flagRegion: "regions",
}

// renamedFlags maps every retired name to the one flag that replaces it. It is
// the source for the rename hint a caller gets instead of "flag provided but
// not defined".
var renamedFlags = map[string]string{
	flagType:      flagMachine,
	flagInstance:  flagMachine,
	flagVCPU:      flagMinVCPU,
	flagCPU:       flagMinVCPU,
	flagMemory:    flagMinMemoryGiB,
	flagMemoryGiB: flagMinMemoryGiB,
	flagPrice:     flagMaxPrice,
	flagBudget:    flagMaxPrice,
}

// mcpArgName derives the MCP argument name of a CLI flag. It accepts the flag
// with or without its leading dashes.
func mcpArgName(flag string) string {
	name := strings.TrimPrefix(flag, "--")
	if plural, ok := pluralMCPArgs[name]; ok {
		return plural
	}

	return strings.ReplaceAll(name, "-", "_")
}

// vocabularyFlags lists the flags marked for the given commands.
func vocabularyFlags(commands commandSet) []string {
	flags := make([]string, 0, len(vocabulary))

	for _, entry := range vocabulary {
		if entry.commands.has(commands) {
			flags = append(flags, entry.flag)
		}
	}

	return flags
}
