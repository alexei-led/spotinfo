package main

import (
	"maps"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"
)

// rootCommandPath is the application itself, which declares the root flags.
const rootCommandPath = ""

// globalFlags are declared on the root command and are not vocabulary entries:
// they select how the binary runs or how it logs, not what it is asked.
var globalFlags = []string{flagMCP, flagDebug, flagQuiet, flagJSONLog}

// autoFlags are the names urfave/cli adds by itself. They are not declared by
// this repository and are excluded from every comparison below.
var autoFlags = []string{"help", "h", "version", "v"}

// commandScope binds one command in the built tree to the vocabulary it must
// carry.
//
// The root carries the mode and logging flags and nothing else: every flag that
// describes a question lives on the command that answers it.
type commandScope struct {
	path     string
	commands commandSet
	globals  bool
}

var commandScopes = []commandScope{
	{path: rootCommandPath, globals: true},
	{path: listCommandName, commands: onList},
	{path: recommendCommandName, commands: onRecommend},
}

// flagGap records exactly where the command tree built today differs from the
// vocabulary, and the plan task that closes each difference.
//
// The command-tree test asserts the difference EQUALS this table rather than
// being contained in it, so a task that lands a flag and leaves its row here
// fails just as loudly as one that never lands it. When the table is empty the
// assertion is plain equality between the tree and the vocabulary, which is what
// acceptance criterion 3 asks for. Never add a row you cannot trace to a task:
// that turns a migration record into a blessed snapshot of whatever the tree
// happens to declare.
type flagGap struct {
	// missing is a vocabulary entry the command does not declare yet.
	missing map[string]string
	// extra is a name the command still declares and the vocabulary retired.
	extra map[string]string
}

var vocabularyGaps = map[string]flagGap{
	listCommandName: {
		missing: map[string]string{
			flagGCPBillingKey: "task 13 declares the flag",
		},
	},
	recommendCommandName: {
		missing: map[string]string{
			flagWithScore:     "task 11 puts the score flags on recommend",
			flagMinScore:      "task 11 puts the score flags on recommend",
			flagAZ:            "task 11 puts the score flags on recommend",
			flagScoreTimeout:  "task 11 puts the score flags on recommend",
			flagGCPBillingKey: "task 13 declares the flag",
		},
	},
}

// TestTheCommandTreeDeclaresExactlyTheVocabulary is what makes acceptance
// criterion 3 mechanical: every concept has one flag name, and the built tree
// declares that name and no other.
//
// Serial on purpose: it builds a cli.App, and urfave/cli writes to its
// package-level HelpFlag while a command is applied.
func TestTheCommandTreeDeclaresExactlyTheVocabulary(t *testing.T) {
	noop := func(*cli.Context) error { return nil }
	tree := commandFlagNames(newSpotinfoApp(noop, noop))

	paths := make([]string, 0, len(commandScopes))
	for _, scope := range commandScopes {
		paths = append(paths, scope.path)
	}

	require.ElementsMatch(t, paths, slices.Collect(maps.Keys(tree)),
		"every command in the tree must carry a declared vocabulary; add it to commandScopes")

	for _, scope := range commandScopes {
		t.Run(commandLabel(scope.path), func(t *testing.T) {
			declared := tree[scope.path]
			expected := expectedFlags(scope)
			gap := vocabularyGaps[scope.path]

			assert.ElementsMatch(t, sortedKeys(gap.missing), missingFrom(expected, declared),
				"%s declares neither these vocabulary flags nor the gap recorded for them; "+
					"land the flag, then delete its row from vocabularyGaps in cmd/spotinfo/vocabulary_test.go",
				commandLabel(scope.path))
			assert.ElementsMatch(t, sortedKeys(gap.extra), missingFrom(declared, expected),
				"%s declares names the vocabulary does not know; a name outside the vocabulary is the drift "+
					"this test exists to catch, and a retired one belongs in vocabularyGaps until it is removed",
				commandLabel(scope.path))

			// The unified defaults carry their own Commands column, and it has
			// to agree with the one above: a default declared for a command
			// that has no such flag is a default nothing can apply.
			for _, row := range unifiedDefaults {
				if row.commands.has(scope.commands) {
					assert.Contains(t, expected, row.flag,
						"--%s is defaulted on %s but the vocabulary does not mark it for that command",
						row.flag, commandLabel(scope.path))
				}
			}
		})
	}
}

// TestTheRecordedVocabularyGapsAreTraceable keeps the gap table a migration
// record rather than a place to park a name. Every row must name a command in
// the tree, a flag the vocabulary knows, and the task that closes it.
func TestTheRecordedVocabularyGapsAreTraceable(t *testing.T) {
	t.Parallel()

	byPath := make(map[string]commandScope, len(commandScopes))
	for _, scope := range commandScopes {
		byPath[scope.path] = scope
	}

	for path, gap := range vocabularyGaps {
		scope, known := byPath[path]
		require.True(t, known, "vocabularyGaps names %q, which is not a command in the tree", path)

		for flag, task := range gap.missing {
			assert.Contains(t, vocabularyFlags(scope.commands), flag,
				"a gap may only withhold a flag the vocabulary marks for this command")
			assert.NotEmpty(t, task, "--%s must name the task that lands it", flag)
		}

		for flag, task := range gap.extra {
			assert.Contains(t, renamedFlags, flag,
				"--%s is declared but is neither vocabulary nor a retired name; nothing removes it", flag)
			assert.NotEmpty(t, task, "--%s must name the task that removes it", flag)
		}
	}
}

// TestMCPArgumentNamesAreDerivedFromTheirFlags proves the derivation rule holds
// for the whole vocabulary: strip the leading dashes, replace "-" with "_", and
// pluralise the one repeatable flag.
func TestMCPArgumentNamesAreDerivedFromTheirFlags(t *testing.T) {
	t.Parallel()

	var withoutArgument []string

	for _, entry := range vocabulary {
		if entry.mcpArg == "" {
			withoutArgument = append(withoutArgument, entry.flag)

			continue
		}

		t.Run(entry.flag, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, entry.mcpArg, mcpArgName(entry.flag))
			assert.Equal(t, entry.mcpArg, mcpArgName("--"+entry.flag), "the leading dashes are optional")
		})
	}

	// Pinned, not merely counted: an entry that silently loses its MCP argument
	// would leave a concept reachable on one surface only.
	assert.ElementsMatch(t, []string{flagOutput, flagGCPProject, flagGCPBillingKey}, withoutArgument,
		"--output is not an MCP argument because MCP is always JSON, and the two GCP credentials "+
			"are read from the environment on that surface")
}

// TestTheOnlyDerivationExceptionIsTheRepeatablePlural holds the exception to the
// derivation rule as data, in both directions, so it cannot become a branch.
func TestTheOnlyDerivationExceptionIsTheRepeatablePlural(t *testing.T) {
	t.Parallel()

	var repeatable []string

	for _, entry := range vocabulary {
		if entry.repeatable {
			repeatable = append(repeatable, entry.flag)

			assert.Equal(t, entry.mcpArg, pluralMCPArgs[entry.flag],
				"a repeatable flag becomes a plural JSON array; give --%s its plural in pluralMCPArgs", entry.flag)
		}
	}

	assert.ElementsMatch(t, repeatable, slices.Collect(maps.Keys(pluralMCPArgs)),
		"pluralMCPArgs may only pluralise a repeatable flag")
}

// TestEveryRetiredFlagNameMapsIntoTheVocabulary proves a removed name can always
// be answered with the one flag that replaces it, which is what a rename hint
// needs.
func TestEveryRetiredFlagNameMapsIntoTheVocabulary(t *testing.T) {
	t.Parallel()

	current := vocabularyFlags(onBoth)

	assert.ElementsMatch(t,
		[]string{flagType, flagInstance, flagVCPU, flagMemory, flagMemoryGiB, flagCPU, flagPrice, flagBudget},
		slices.Collect(maps.Keys(renamedFlags)),
		"the retired names are fixed by the plan; retiring another one is a vocabulary change")

	for retired, replacement := range renamedFlags {
		assert.Contains(t, current, replacement, "--%s must be replaced by a flag the vocabulary declares", retired)
		assert.NotContains(t, current, retired, "--%s is retired and must not also be a current flag", retired)
	}
}

// TestUnifiedDefaultsAgreeWithTheVocabulary reads both tables in the plan's
// vocabulary section, so a defaults row that claims the wrong command fails here
// rather than shipping a default on a command that has no such flag.
func TestUnifiedDefaultsAgreeWithTheVocabulary(t *testing.T) {
	t.Parallel()

	byFlag := make(map[string]vocabularyEntry, len(vocabulary))
	for _, entry := range vocabulary {
		byFlag[entry.flag] = entry
	}

	defaulted := make([]string, 0, len(unifiedDefaults))

	for _, row := range unifiedDefaults {
		entry, known := byFlag[row.flag]
		require.True(t, known, "--%s has a default but is not in the vocabulary", row.flag)
		assert.Equal(t, entry.commands, row.commands,
			"--%s is defaulted on different commands than the vocabulary marks it for", row.flag)
		assert.NotNil(t, row.value, "--%s must state its default value", row.flag)

		defaulted = append(defaulted, row.flag)
	}

	assert.ElementsMatch(t, []string{flagWorkload, flagRegion, flagTop, flagOS, flagOutput}, defaulted,
		"these five flags carry a default; every other flag is off or unset until the caller says otherwise")
	assert.Equal(t, []any{defaultWorkload, defaultRegion, defaultTop, defaultOS, defaultOutput},
		[]any{"cost", "all", 3, "linux", "table"},
		"the defaults are the same value on both surfaces and are stated in the plan")
}

// TestTheVocabularyNamesEachConceptOnce is the property the whole file exists
// for: one concept, one flag, one MCP argument.
func TestTheVocabularyNamesEachConceptOnce(t *testing.T) {
	t.Parallel()

	// Pinned against the plan's vocabulary table. Without this the command tree
	// and the vocabulary could be renamed together and every other test here
	// would still pass — which is exactly the "one name per concept, decided
	// once" property the table exists to hold. Changing this list is a plan
	// change, not a refactor.
	names := make([]string, 0, len(vocabulary))
	for _, entry := range vocabulary {
		names = append(names, entry.flag)
	}

	assert.ElementsMatch(t, []string{
		flagCloud, flagRegion, flagMachine, flagArchitecture, flagOS,
		flagMinVCPU, flagMinMemoryGiB, flagMaxPrice, flagWorkload, flagTop,
		flagSort, flagOrder, flagOutput, flagOffline, flagRefresh,
		flagWithScore, flagMinScore, flagAZ, flagScoreTimeout, flagLiveRisk,
		flagGCPProject, flagGCPBillingKey,
	}, names, "the vocabulary is the plan's table; adding or renaming a concept is a plan change")

	seen := map[string]string{}

	for _, entry := range vocabulary {
		require.NotEmpty(t, entry.concept, "--%s must name the concept it expresses", entry.flag)
		require.NotZero(t, entry.commands, "--%s must be marked for at least one command", entry.flag)

		for _, name := range []string{"concept " + entry.concept, "flag " + entry.flag} {
			assert.NotContains(t, seen, name, "%s is declared twice", name)
			seen[name] = entry.flag
		}

		if entry.mcpArg != "" {
			assert.NotContains(t, seen, "argument "+entry.mcpArg, "MCP argument %q is declared twice", entry.mcpArg)
			seen["argument "+entry.mcpArg] = entry.flag
		}
	}
}

// commandFlagNames walks the built tree and reports the flag names each command
// declares, aliases included: an alias is a name a caller can pass, so it counts
// against "one concept, one name" like any other.
func commandFlagNames(app *cli.App) map[string][]string {
	tree := map[string][]string{rootCommandPath: declaredNames(app.Flags)}

	var visit func(prefix string, commands []*cli.Command)

	visit = func(prefix string, commands []*cli.Command) {
		for _, command := range commands {
			path := command.Name
			if prefix != rootCommandPath {
				path = prefix + " " + command.Name
			}

			tree[path] = declaredNames(command.Flags)
			visit(path, command.Subcommands)
		}
	}

	visit(rootCommandPath, app.Commands)

	return tree
}

func declaredNames(flags []cli.Flag) []string {
	names := make([]string, 0, len(flags))

	for _, flag := range flags {
		for _, name := range flag.Names() {
			if !slices.Contains(autoFlags, name) {
				names = append(names, name)
			}
		}
	}

	return names
}

func expectedFlags(scope commandScope) []string {
	expected := vocabularyFlags(scope.commands)
	if scope.globals {
		expected = append(expected, globalFlags...)
	}

	return expected
}

// missingFrom reports the names in want that are absent from have.
func missingFrom(want, have []string) []string {
	absent := []string{}

	for _, name := range want {
		if !slices.Contains(have, name) {
			absent = append(absent, name)
		}
	}

	slices.Sort(absent)

	return absent
}

func sortedKeys(m map[string]string) []string {
	keys := slices.Sorted(maps.Keys(m))
	if keys == nil {
		return []string{}
	}

	return keys
}

func commandLabel(path string) string {
	if path == rootCommandPath {
		return appName
	}

	return appName + " " + path
}
