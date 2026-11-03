package mcp_test

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	tgmcp "github.com/gruntwork-io/terragrunt/internal/cli/commands/mcp"
	"github.com/gruntwork-io/terragrunt/test/helpers/venvtest"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// catalogToken is the credential in [catalogRemoteURL].
const catalogToken = "s3cr3t-token"

// catalogRemoteURL is a repository the catalog can reach only by running git.
const catalogRemoteURL = "git::https://user:" + catalogToken + "@example.invalid/org/modules.git"

const catalogAlphaReadme = `---
name: Alpha
description: The first module.
---

Alpha body.
`

type catalogComponent struct {
	Kind            string `json:"kind"`
	Title           string `json:"title"`
	Description     string `json:"description"`
	Dir             string `json:"dir"`
	ComponentSource string `json:"component_source"`
	Doc             string `json:"doc"`
}

type catalogFailure struct {
	Source string `json:"source"`
	Error  string `json:"error"`
}

type catalogOutput struct {
	Components     []catalogComponent `json:"components"`
	Failures       []catalogFailure   `json:"failures"`
	Degraded       []string           `json:"degraded"`
	ComponentCount int                `json:"component_count"`
}

// catalogTree returns a server root whose root.hcl names urls in its catalog
// block, beside a checked-out repository at ./repo holding the modules alpha
// and bravo, with alphaReadme as alpha's README.
func catalogTree(t *testing.T, alphaReadme string, urls ...string) string {
	t.Helper()

	return writeTree(t, map[string]string{
		"root.hcl":                     "catalog {\n  urls = [\"" + strings.Join(urls, "\", \"") + "\"]\n}\n",
		"repo/.git/HEAD":               "ref: refs/heads/main\n",
		"repo/.git/config":             "[remote \"origin\"]\n\turl = github.com/acme/repo\n",
		"repo/modules/alpha/main.tf":   "\n",
		"repo/modules/alpha/README.md": alphaReadme,
		"repo/modules/bravo/main.tf":   "\n",
	})
}

// newCatalogSession is [newTestSession] with real temp and cache dirs, which a
// catalog load creates its clone and its CAS store in.
func newCatalogSession(t *testing.T, dir string) *mcp.ClientSession {
	t.Helper()

	scratch := t.TempDir()
	v := venvtest.NewWithOSFS().
		WithTempDir(func() string { return scratch }).
		WithUserCacheDir(func() (string, error) { return filepath.Join(scratch, "cache"), nil })

	return newTestSessionWith(t, dir, v, func(*tgmcp.Options) {}, nil)
}

// catalogDirs returns the dir of each component, sorted: repositories load
// concurrently, so the order components arrive in varies between runs.
func catalogDirs(out *catalogOutput) []string {
	dirs := make([]string, 0, len(out.Components))

	for _, c := range out.Components {
		dirs = append(dirs, c.Dir)
	}

	slices.Sort(dirs)

	return dirs
}

// TestCatalogListsTheConfiguredCatalog pins that the catalog tool returns the
// components of the repository the catalog block names, with every capability
// denied, and leaves out README bodies until a call asks for them.
func TestCatalogListsTheConfiguredCatalog(t *testing.T) {
	t.Parallel()

	session := newCatalogSession(t, catalogTree(t, catalogAlphaReadme, "./repo"))

	var out catalogOutput

	callTool(t, session, "catalog", map[string]any{}, &out)

	assert.Equal(t, []string{"modules/alpha", "modules/bravo"}, catalogDirs(&out))
	assert.Equal(t, 2, out.ComponentCount)
	assert.Empty(t, out.Failures)
	assert.Empty(t, out.Degraded)

	for _, c := range out.Components {
		assert.Equal(t, "module", c.Kind, c.Dir)
		assert.Empty(t, c.Doc, c.Dir)
	}

	var withDocs catalogOutput

	callTool(t, session, "catalog", map[string]any{"include_docs": true}, &withDocs)

	i := slices.IndexFunc(withDocs.Components, func(c catalogComponent) bool { return c.Dir == "modules/alpha" })
	require.GreaterOrEqual(t, i, 0, "alpha is missing: %v", withDocs.Components)

	alpha := withDocs.Components[i]
	assert.Equal(t, "Alpha", alpha.Title)
	assert.Equal(t, "The first module.", alpha.Description)
	assert.Equal(t, "Alpha body.\n", alpha.Doc)
}

// TestCatalogLimitCapsComponentsButNotTheCount pins that limit bounds the
// components returned while component_count still counts all of them, so the
// agent can tell the list was cut.
func TestCatalogLimitCapsComponentsButNotTheCount(t *testing.T) {
	t.Parallel()

	session := newCatalogSession(t, catalogTree(t, catalogAlphaReadme, "./repo"))

	var out catalogOutput

	callTool(t, session, "catalog", map[string]any{"limit": 1}, &out)

	assert.Len(t, out.Components, 1)
	assert.Equal(t, 2, out.ComponentCount)
}

func TestCatalogRejectsALimitOutOfRange(t *testing.T) {
	t.Parallel()

	session := newCatalogSession(t, catalogTree(t, catalogAlphaReadme, "./repo"))

	for _, limit := range []int{-1, 1_000_000} {
		res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
			Name:      "catalog",
			Arguments: map[string]any{"limit": limit},
		})
		require.NoError(t, err)
		assert.True(t, res.IsError, "limit %d must be refused", limit)
	}
}

// TestCatalogCutsALongReadme pins that a README body comes back bounded, since
// the repository decides how long it is.
func TestCatalogCutsALongReadme(t *testing.T) {
	t.Parallel()

	body := strings.Repeat("x", 100_000)
	session := newCatalogSession(t, catalogTree(t, "---\nname: Alpha\n---\n\n"+body+"\n", "./repo"))

	var out catalogOutput

	callTool(t, session, "catalog", map[string]any{"include_docs": true}, &out)

	i := slices.IndexFunc(out.Components, func(c catalogComponent) bool { return c.Dir == "modules/alpha" })
	require.GreaterOrEqual(t, i, 0, "alpha is missing: %v", out.Components)

	doc := out.Components[i].Doc
	assert.Less(t, utf8.RuneCountInString(doc), len(body))
	assert.True(t, strings.HasSuffix(doc, "…"), "a cut README must say it was cut")
}

// TestCatalogReportsARepositoryItCannotCloneWithoutAllowExec pins that a
// remote repository is not cloned when the server was not started with
// --allow=exec. The refusal is reported without the credential the URL
// holds, and the components of the local repository still come back.
func TestCatalogReportsARepositoryItCannotCloneWithoutAllowExec(t *testing.T) {
	t.Parallel()

	session := newCatalogSession(t, catalogTree(t, catalogAlphaReadme, "./repo", catalogRemoteURL))

	var out catalogOutput

	callTool(t, session, "catalog", map[string]any{}, &out)

	assert.Equal(t, []string{"modules/alpha", "modules/bravo"}, catalogDirs(&out))

	require.Len(t, out.Failures, 1)
	assert.Contains(t, out.Failures[0].Source, "example.invalid/org/modules.git")
	assert.NotContains(t, out.Failures[0].Source, catalogToken)
	assert.NotContains(t, out.Failures[0].Error, catalogToken)

	assert.True(t, slices.ContainsFunc(out.Degraded, func(note string) bool {
		return strings.HasPrefix(note, `denied "git `) &&
			strings.HasSuffix(note, `": sources that need it were not downloaded`)
	}), "no note reports the refused clone: %v", out.Degraded)

	for _, note := range out.Degraded {
		assert.NotContains(t, note, catalogToken)
	}
}

func TestCatalogIsConfinedToTheServerRoot(t *testing.T) {
	t.Parallel()

	session := newCatalogSession(t, catalogTree(t, catalogAlphaReadme, "./repo"))

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "catalog",
		Arguments: map[string]any{"working_dir": ".."},
	})
	require.NoError(t, err)
	assert.True(t, res.IsError, "a catalog read outside the server root must be refused")
}
