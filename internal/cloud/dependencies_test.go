package cloud

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// repoRoot is this package's directory relative to the module root, inverted.
const repoRoot = "../.."

// importPolicy forbids a set of import prefixes for every package under
// dirPrefix. Prefix matching is deliberate: it covers subpackages that do not
// exist yet, such as future provider packages.
type importPolicy struct {
	name      string
	dirPrefix string
	reason    string
	forbidden []string
}

var importPolicies = []importPolicy{
	{
		name:      "neutral domain",
		dirPrefix: "internal/cloud",
		reason:    "the neutral domain must stay free of provider SDKs, the legacy AWS package, the CLI, and MCP",
		forbidden: []string{
			"spotinfo/internal/spot",
			"spotinfo/internal/mcp",
			"spotinfo/internal/providers",
			"spotinfo/cmd/",
			"github.com/aws/",
			"cloud.google.com/",
			"github.com/Azure/",
			"github.com/mark3labs/mcp-go",
			"github.com/urfave/cli",
		},
	},
	{
		name:      "providers",
		dirPrefix: "internal/providers",
		reason:    "a provider must not depend on a delivery mechanism",
		forbidden: []string{
			"spotinfo/cmd/",
			"spotinfo/internal/mcp",
		},
	},
}

func TestPackagesRespectImportPolicies(t *testing.T) {
	t.Parallel()

	imports, scanned := moduleImports(t)
	// Guard against a silent pass: a walk that reached nothing, or only the test
	// files that live beside this one, would satisfy every policy vacuously.
	require.Contains(t, scanned, "internal/cloud/provider.go")
	require.Contains(t, scanned, "internal/providers/aws/provider.go")

	for dir, paths := range imports {
		for _, policy := range importPolicies {
			if !strings.HasPrefix(dir, policy.dirPrefix) {
				continue
			}
			assert.Empty(t, policy.violations(paths), "%s: %s", dir, policy.reason)
		}
	}
}

func TestForbiddenImportIsDetected(t *testing.T) {
	t.Parallel()

	paths := fileImports(t, filepath.Join("testdata", "forbidden-import.go.txt"))
	require.Contains(t, paths, "spotinfo/internal/spot")

	policy := policyNamed(t, "neutral domain")
	assert.Equal(t, []string{"spotinfo/internal/spot"}, policy.violations(paths))
	assert.Empty(t, policyNamed(t, "providers").violations(paths),
		"internal/spot is legal for a provider adapter and must not be reported there")
}

func (p importPolicy) violations(paths []string) []string {
	var found []string
	for _, path := range paths {
		for _, prefix := range p.forbidden {
			if strings.HasPrefix(path, prefix) {
				found = append(found, path)

				break
			}
		}
	}

	return found
}

func policyNamed(t *testing.T, name string) importPolicy {
	t.Helper()

	for _, policy := range importPolicies {
		if policy.name == name {
			return policy
		}
	}
	t.Fatalf("no import policy named %q", name)

	return importPolicy{}
}

// moduleImports maps every module package directory, relative to the module
// root and slash-separated, to the import paths its Go files declare. It also
// returns every scanned file, so a caller can prove the walk reached the files
// it means to police.
func moduleImports(t *testing.T) (map[string][]string, []string) {
	t.Helper()

	imports := make(map[string][]string)

	var scanned []string

	err := filepath.WalkDir(repoRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path == repoRoot {
				return nil
			}
			if name := entry.Name(); name == "testdata" || strings.HasPrefix(name, ".") {
				return fs.SkipDir
			}

			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}

		relative, err := filepath.Rel(repoRoot, path)
		if err != nil {
			return err
		}
		file := filepath.ToSlash(relative)
		dir := filepath.ToSlash(filepath.Dir(relative))
		scanned = append(scanned, file)
		imports[dir] = append(imports[dir], fileImports(t, path)...)

		return nil
	})
	require.NoError(t, err)

	return imports, scanned
}

func fileImports(t *testing.T, path string) []string {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	require.NoError(t, err)

	paths := make([]string, 0, len(file.Imports))
	for _, spec := range file.Imports {
		unquoted, err := strconv.Unquote(spec.Path.Value)
		require.NoError(t, err)
		paths = append(paths, unquoted)
	}

	return paths
}
