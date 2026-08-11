package main

import (
	"io"
	"log/slog"
	"maps"
	"slices"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spotinfo/internal/mcp"
)

// The MCP surface is held to the same vocabulary as the CLI, and this file is
// where the two are compared. It lives in cmd/spotinfo because mcpArgName is in
// package main and internal/mcp cannot import it — internal/cloud forbids a CLI
// import and dependencies_test.go enforces that — so the derivation is asserted
// from the side that owns the vocabulary, against the tools the server actually
// registered rather than against a second list that could drift from them.

// The published tool names. They are spelled here rather than imported: pinning
// them from outside internal/mcp is what makes a rename a reviewed change.
const (
	listToolName      = "list_spot_machines"
	recommendToolName = "recommend_spot_machines"
	regionsToolName   = "list_cloud_regions"
)

// mcpToolScope binds one tool to the vocabulary it carries.
type mcpToolScope struct {
	tool string
	// commands is the CLI command the tool mirrors.
	commands commandSet
	// flags names the concepts a tool that mirrors no command carries.
	flags []string
}

var mcpToolScopes = []mcpToolScope{
	{tool: listToolName, commands: onList},
	{tool: recommendToolName, commands: onRecommend},
	// It enumerates a cloud rather than querying one, so the cloud is the only
	// concept it takes — and it takes it under the same name as the other two.
	{tool: regionsToolName, flags: []string{flagCloud}},
}

// vocabularyFlagsOf lists the flags this tool's scope covers.
func (s mcpToolScope) vocabularyFlagsOf() []string {
	if s.commands != 0 {
		return vocabularyFlags(s.commands)
	}

	return s.flags
}

// mcpArgumentGaps records every concept the vocabulary marks for a tool's
// command that the tool does not declare as an argument, and why.
//
// The test asserts the difference EQUALS this table, not that it is contained
// in it, so an argument that lands while its row stays fails as loudly as one
// that never lands. Every row states a surface decision or a recorded blocker;
// a row that merely describes an omission belongs in the code, as an argument.
var mcpArgumentGaps = map[string]map[string]string{
	recommendToolName: {
		flagSort: "a ranked page publishes each row's rank, so a client that wants another order " +
			"sorts the array it was handed; on the CLI the flag reorders a rendered page",
		flagOrder:    "the sort direction goes with the sort key",
		flagLiveRisk: "no task in this plan puts the opt-in GCP preemption lookup on the MCP surface",
		// The same four rows vocabularyGaps carries for the recommend command:
		// a recommendation publishes no placement score yet, so there is nothing
		// for these to ask for on either surface.
		flagWithScore:    "task 11 puts the score flags on recommend",
		flagMinScore:     "task 11 puts the score flags on recommend",
		flagAZ:           "task 11 puts the score flags on recommend",
		flagScoreTimeout: "task 11 puts the score flags on recommend",
	},
}

// publishedTools builds the server and indexes the tools it registered.
func publishedTools(t *testing.T) map[string]mcpgo.Tool {
	t.Helper()

	server, err := mcp.NewServer(mcp.Config{
		Version: "test",
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	require.NoError(t, err)

	tools := make(map[string]mcpgo.Tool)
	for _, tool := range server.Tools() {
		tools[tool.Name] = tool
	}

	return tools
}

// mcpArgumentsOf maps each argument name this scope may declare back to the
// flag it is derived from. A concept the vocabulary gives no MCP name — the
// output format, and the two GCP credentials, which are environment-only on
// that surface — is absent by design and never appears here.
func mcpArgumentsOf(scope mcpToolScope) map[string]string {
	byFlag := make(map[string]vocabularyEntry, len(vocabulary))
	for _, entry := range vocabulary {
		byFlag[entry.flag] = entry
	}

	arguments := make(map[string]string)

	for _, flag := range scope.vocabularyFlagsOf() {
		if byFlag[flag].mcpArg == "" {
			continue
		}
		arguments[mcpArgName(flag)] = flag
	}

	return arguments
}

// TestEveryMCPArgumentIsDerivedFromItsFlag is acceptance criterion 3 on the MCP
// side: no tool may name a concept differently from the flag that names it, and
// the derivation is mechanical rather than a table of exceptions.
func TestEveryMCPArgumentIsDerivedFromItsFlag(t *testing.T) {
	t.Parallel()

	tools := publishedTools(t)

	scoped := make([]string, 0, len(mcpToolScopes))
	for _, scope := range mcpToolScopes {
		scoped = append(scoped, scope.tool)
	}

	require.ElementsMatch(t, scoped, slices.Collect(maps.Keys(tools)),
		"every published tool must carry a declared vocabulary; add it to mcpToolScopes")

	for _, scope := range mcpToolScopes {
		t.Run(scope.tool, func(t *testing.T) {
			t.Parallel()

			declared := slices.Sorted(maps.Keys(tools[scope.tool].InputSchema.Properties))
			require.NotEmpty(t, declared)

			derivable := mcpArgumentsOf(scope)

			// Unconditional: a declared name the vocabulary cannot derive is
			// exactly the drift this test exists to catch.
			for _, argument := range declared {
				flag, known := derivable[argument]
				if !assert.True(t, known,
					"%s declares %q, which is not mcpArgName of any vocabulary flag marked for it",
					scope.tool, argument) {
					continue
				}

				assert.Equal(t, mcpArgName(flag), argument,
					"--%s must reach MCP as %q", flag, mcpArgName(flag))
			}

			// And the other direction, against the recorded table.
			absent := []string{}

			for argument, flag := range derivable {
				if !slices.Contains(declared, argument) {
					absent = append(absent, flag)
				}
			}

			slices.Sort(absent)
			assert.ElementsMatch(t, sortedKeys(mcpArgumentGaps[scope.tool]), absent,
				"%s declares neither these vocabulary concepts nor the gap recorded for them; "+
					"land the argument, then delete its row from mcpArgumentGaps", scope.tool)
		})
	}
}

// TestTheRecordedMCPArgumentGapsAreTraceable keeps the gap table a record of
// decisions rather than a place to park a name.
func TestTheRecordedMCPArgumentGapsAreTraceable(t *testing.T) {
	t.Parallel()

	byTool := make(map[string]mcpToolScope, len(mcpToolScopes))
	for _, scope := range mcpToolScopes {
		byTool[scope.tool] = scope
	}

	for tool, gap := range mcpArgumentGaps {
		scope, known := byTool[tool]
		require.True(t, known, "mcpArgumentGaps names %q, which is not a published tool", tool)

		for flag, reason := range gap {
			assert.Contains(t, scope.vocabularyFlagsOf(), flag,
				"a gap may only withhold a concept the vocabulary marks for this tool")
			assert.NotEmpty(t, reason, "--%s must state why it has no argument here", flag)
		}
	}
}

// TestTheMCPToolsCarryTheUnifiedDefaults reads the same table the CLI flags are
// built from, so a default cannot differ by surface: one value, advertised by
// both, or this fails.
func TestTheMCPToolsCarryTheUnifiedDefaults(t *testing.T) {
	t.Parallel()

	tools := publishedTools(t)

	byFlag := make(map[string]vocabularyEntry, len(vocabulary))
	for _, entry := range vocabulary {
		byFlag[entry.flag] = entry
	}

	for _, scope := range mcpToolScopes {
		for _, row := range unifiedDefaults {
			entry := byFlag[row.flag]
			if entry.mcpArg == "" || !row.commands.has(scope.commands) {
				continue
			}
			if _, withheld := mcpArgumentGaps[scope.tool][row.flag]; withheld {
				continue
			}

			t.Run(scope.tool+"/"+entry.mcpArg, func(t *testing.T) {
				t.Parallel()

				property, ok := tools[scope.tool].InputSchema.Properties[entry.mcpArg].(map[string]any)
				require.True(t, ok, "%s must declare %q", scope.tool, entry.mcpArg)

				want := row.value
				if entry.repeatable {
					// A repeatable flag becomes a JSON array, so its default is
					// a one-element array of the same value.
					want = []string{row.value.(string)} //nolint:forcetypeassert // a repeatable default is a string
				}

				assert.EqualValues(t, want, property["default"],
					"--%s defaults to %v on the CLI; %s must advertise the same value",
					row.flag, row.value, scope.tool)
			})
		}
	}
}
