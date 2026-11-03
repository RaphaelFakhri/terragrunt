package config_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gruntwork-io/terragrunt/internal/experiment"
	"github.com/gruntwork-io/terragrunt/pkg/config"
	"github.com/gruntwork-io/terragrunt/test/helpers/logger"
	"github.com/gruntwork-io/terragrunt/test/helpers/venvtest"
)

func TestVariablesBlockRequiresExperiment(t *testing.T) {
	t.Parallel()

	_, err := parseVariablesFixture(t, map[string]string{
		config.DefaultTerragruntConfigPath: `
variables {
  inputs = { name = "value" }
}
`,
	}, variablesExperimentOff)

	var experimentErr config.VariablesBlockRequiresExperimentError

	require.ErrorAs(t, err, &experimentErr)
}

func TestVariablesBlockInvalidKind(t *testing.T) {
	t.Parallel()

	_, err := parseVariablesFixture(t, map[string]string{
		config.DefaultTerragruntConfigPath: `
variables {
  kind   = "yaml"
  inputs = { name = "value" }
}
`,
	}, variablesExperimentOn)

	var kindErr config.InvalidVariablesKindError

	require.ErrorAs(t, err, &kindErr)
	assert.Equal(t, "yaml", kindErr.Kind)
}

func TestVariablesBlockResolveInputs(t *testing.T) {
	t.Parallel()

	rootConfig := `
variables {
  kind     = "tfvar"
  validate = true
  inputs   = { from_root = "root", shared = "root" }
}
`

	testCases := []struct {
		files    map[string]string
		expected map[string]config.ResolvedInput
		name     string
	}{
		{
			name: "defaults to env and drops null values",
			files: map[string]string{
				config.DefaultTerragruntConfigPath: `
variables {
  inputs = { name = "value", unset = null }
}
`,
			},
			expected: map[string]config.ResolvedInput{
				"name": {Value: "value", Kind: config.VariablesKindEnv},
			},
		},
		{
			name: "nullable keeps null values",
			files: map[string]string{
				config.DefaultTerragruntConfigPath: `
variables {
  kind     = "json"
  nullable = true
  inputs   = { unset = null }
}
`,
			},
			expected: map[string]config.ResolvedInput{
				"unset": {Kind: config.VariablesKindJSON},
			},
		},
		{
			name: "a null value that is dropped leaves an earlier definition in place",
			files: map[string]string{
				config.DefaultTerragruntConfigPath: `
variables {
  kind   = "tfvar"
  inputs = { name = "first" }
}

variables {
  inputs = { name = null }
}
`,
			},
			expected: map[string]config.ResolvedInput{
				"name": {Value: "first", Kind: config.VariablesKindTFVar},
			},
		},
		{
			name: "the inputs attribute and blocks combine",
			files: map[string]string{
				config.DefaultTerragruntConfigPath: `
inputs = { attr_only = "attr", nulled = null }

variables {
  kind     = "tfvar"
  validate = true
  inputs   = { number = 1 }
}

variables {
  kind   = "json"
  inputs = { name = "second" }
}
`,
			},
			expected: map[string]config.ResolvedInput{
				"attr_only": {Value: "attr", Kind: config.VariablesKindEnv},
				"nulled":    {Kind: config.VariablesKindEnv},
				"name":      {Value: "second", Kind: config.VariablesKindJSON},
				"number":    {Value: json.Number("1"), Kind: config.VariablesKindTFVar, Validate: true},
			},
		},
		{
			name: "included blocks combine with a shallow merge",
			files: map[string]string{
				"root.hcl": rootConfig,
				config.DefaultTerragruntConfigPath: `
include "root" {
  path = find_in_parent_folders("root.hcl")
}

variables {
  inputs = { from_child = "child" }
}
`,
			},
			expected: map[string]config.ResolvedInput{
				"from_root":  {Value: "root", Kind: config.VariablesKindTFVar, Validate: true},
				"shared":     {Value: "root", Kind: config.VariablesKindTFVar, Validate: true},
				"from_child": {Value: "child", Kind: config.VariablesKindEnv},
			},
		},
		{
			name: "included blocks combine with a deep merge",
			files: map[string]string{
				"root.hcl": rootConfig,
				config.DefaultTerragruntConfigPath: `
include "root" {
  path           = find_in_parent_folders("root.hcl")
  merge_strategy = "deep"
}

variables {
  kind   = "json"
  inputs = { from_child = "child" }
}
`,
			},
			expected: map[string]config.ResolvedInput{
				"from_root":  {Value: "root", Kind: config.VariablesKindTFVar, Validate: true},
				"shared":     {Value: "root", Kind: config.VariablesKindTFVar, Validate: true},
				"from_child": {Value: "child", Kind: config.VariablesKindJSON},
			},
		},
		{
			name: "the inputs attribute still merges through an include",
			files: map[string]string{
				"root.hcl": `inputs = { name = "root" }`,
				config.DefaultTerragruntConfigPath: `
include "root" {
  path = find_in_parent_folders("root.hcl")
}

inputs = { name = "child" }

variables {
  inputs = { other = "block" }
}
`,
			},
			expected: map[string]config.ResolvedInput{
				"name":  {Value: "child", Kind: config.VariablesKindEnv},
				"other": {Value: "block", Kind: config.VariablesKindEnv},
			},
		},
		{
			name: "included blocks are left out without a merge",
			files: map[string]string{
				"root.hcl": rootConfig,
				config.DefaultTerragruntConfigPath: `
include "root" {
  path           = find_in_parent_folders("root.hcl")
  merge_strategy = "no_merge"
}

variables {
  inputs = { shared = "child" }
}
`,
			},
			expected: map[string]config.ResolvedInput{
				"shared": {Value: "child", Kind: config.VariablesKindEnv},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg, err := parseVariablesFixture(t, tc.files, variablesExperimentOn)
			require.NoError(t, err)

			assert.Equal(t, tc.expected, cfg.ResolveInputs())
		})
	}
}

func TestVariablesBlockDuplicateInput(t *testing.T) {
	t.Parallel()

	rootBlock := `
variables {
  kind   = "tfvar"
  inputs = { name = "root" }
}
`

	testCases := []struct {
		files   map[string]string
		name    string
		first   string
		subject string
	}{
		{
			name: "two blocks of the same kind",
			files: map[string]string{
				config.DefaultTerragruntConfigPath: `
variables {
  inputs = { name = "first" }
}

variables {
  inputs = { name = "second" }
}
`,
			},
			first:   "unit/terragrunt.hcl:3,14-18",
			subject: "unit/terragrunt.hcl:7,14-18",
		},
		{
			name: "two blocks of different kinds with multiline maps",
			files: map[string]string{
				config.DefaultTerragruntConfigPath: `
variables {
  kind = "tfvar"

  inputs = {
    other = 1
    name  = "first"
  }
}

variables {
  kind = "json"

  inputs = {
    "name" = "second"
  }
}
`,
			},
			first:   "unit/terragrunt.hcl:7,5-9",
			subject: "unit/terragrunt.hcl:15,5-11",
		},
		{
			name: "the inputs attribute and a block",
			files: map[string]string{
				config.DefaultTerragruntConfigPath: `
inputs = { name = "attr" }

variables {
  inputs = { name = "block" }
}
`,
			},
			first:   "unit/terragrunt.hcl:2,12-16",
			subject: "unit/terragrunt.hcl:5,14-18",
		},
		{
			name: "a computed inputs expression points at the expression",
			files: map[string]string{
				config.DefaultTerragruntConfigPath: `
variables {
  inputs = { name = "first" }
}

variables {
  inputs = merge({ name = "second" })
}
`,
			},
			first:   "unit/terragrunt.hcl:3,14-18",
			subject: "unit/terragrunt.hcl:7,12-38",
		},
		{
			name: "an included block and a child block with a shallow merge",
			files: map[string]string{
				"root.hcl": rootBlock,
				config.DefaultTerragruntConfigPath: `
include "root" {
  path = find_in_parent_folders("root.hcl")
}

variables {
  inputs = { name = "child" }
}
`,
			},
			first:   "root.hcl:4,14-18",
			subject: "unit/terragrunt.hcl:7,14-18",
		},
		{
			name: "an included block and a child block with a deep merge",
			files: map[string]string{
				"root.hcl": rootBlock,
				config.DefaultTerragruntConfigPath: `
include "root" {
  path           = find_in_parent_folders("root.hcl")
  merge_strategy = "deep"
}

variables {
  inputs = { name = "child" }
}
`,
			},
			first:   "root.hcl:4,14-18",
			subject: "unit/terragrunt.hcl:8,14-18",
		},
		{
			name: "an included block and a child inputs attribute",
			files: map[string]string{
				"root.hcl": rootBlock,
				config.DefaultTerragruntConfigPath: `
include "root" {
  path = find_in_parent_folders("root.hcl")
}

inputs = { name = "child" }
`,
			},
			first:   "unit/terragrunt.hcl:6,12-16",
			subject: "root.hcl:4,14-18",
		},
		{
			name: "an included inputs attribute and a child block",
			files: map[string]string{
				"root.hcl": `inputs = { name = "root" }`,
				config.DefaultTerragruntConfigPath: `
include "root" {
  path = find_in_parent_folders("root.hcl")
}

variables {
  inputs = { name = "child" }
}
`,
			},
			first:   "root.hcl:1,12-16",
			subject: "unit/terragrunt.hcl:7,14-18",
		},
		{
			name: "two included files",
			files: map[string]string{
				"a.hcl": rootBlock,
				"b.hcl": rootBlock,
				config.DefaultTerragruntConfigPath: `
include "a" {
  path = find_in_parent_folders("a.hcl")
}

include "b" {
  path = find_in_parent_folders("b.hcl")
}
`,
			},
			first:   "a.hcl:4,14-18",
			subject: "b.hcl:4,14-18",
		},
		{
			name: "a unit block and an autoinclude block",
			files: map[string]string{
				filepath.Join("unit", "terragrunt.autoinclude.hcl"): `
variables {
  inputs = { name = "autoinclude" }
}
`,
				config.DefaultTerragruntConfigPath: `
variables {
  inputs = { name = "unit" }
}
`,
			},
			first:   "unit/terragrunt.hcl:3,14-18",
			subject: "unit/terragrunt.autoinclude.hcl:3,14-18",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			diags, err := parseDuplicateInputFixture(t, tc.files, config.DefaultTerragruntConfigPath)
			require.Error(t, err)
			require.Len(t, diags, 1)

			root := venvtest.Root("/fixture")

			assert.Equal(t, hcl.DiagError, diags[0].Severity)
			assert.Equal(t, filepath.Join(root, filepath.FromSlash(tc.subject)), diags[0].Subject.String())
			assert.Contains(t, diags[0].Detail, filepath.Join(root, filepath.FromSlash(tc.first)))
		})
	}
}

func TestVariablesBlockDuplicateInputReportsEveryName(t *testing.T) {
	t.Parallel()

	diags, err := parseDuplicateInputFixture(t, map[string]string{
		config.DefaultTerragruntConfigPath: `
variables {
  inputs = { alpha = 1, beta = 2, gamma = 3 }
}

variables {
  inputs = { beta = 2, alpha = 1 }
}
`,
	}, config.DefaultTerragruntConfigPath)
	require.Error(t, err)

	file := filepath.Join(venvtest.Root("/fixture"), "unit", config.DefaultTerragruntConfigPath)

	subjects := make([]string, 0, len(diags))
	for _, diag := range diags {
		subjects = append(subjects, diag.Subject.String())
	}

	assert.Equal(t, []string{file + ":7,24-29", file + ":7,14-18"}, subjects)
}

func TestVariablesBlockDuplicateInputInJSONConfig(t *testing.T) {
	t.Parallel()

	jsonConfig := config.DefaultTerragruntConfigPath + ".json"

	diags, err := parseDuplicateInputFixture(t, map[string]string{
		jsonConfig: `{"variables":[{"inputs":{"name":"a"}},{"inputs":{"name":"b"}}]}`,
	}, jsonConfig)
	require.Error(t, err)
	require.Len(t, diags, 1)

	assert.Equal(
		t,
		filepath.Join(venvtest.Root("/fixture"), jsonConfig)+":1,50-56",
		diags[0].Subject.String(),
	)
}

func TestVariablesBlockToRunConfig(t *testing.T) {
	t.Parallel()

	cfg, err := parseVariablesFixture(t, map[string]string{
		config.DefaultTerragruntConfigPath: `
inputs = { from_attr = "attr" }

variables {
  kind     = "tfvar"
  validate = true
  inputs   = { from_tfvar = "tfvar" }
}

variables {
  kind     = "json"
  nullable = true
  inputs   = { from_json = null }
}
`,
	}, variablesExperimentOn)
	require.NoError(t, err)

	runCfg := cfg.ToRunConfig(logger.CreateLogger(), venvtest.New().FS)

	assert.Equal(t, map[string]any{"from_attr": "attr"}, runCfg.Inputs)
	assert.Equal(t, map[string]any{"from_tfvar": "tfvar"}, runCfg.TFVarsInputs)
	assert.Equal(t, map[string]any{"from_json": nil}, runCfg.JSONInputs)
	assert.Equal(t, map[string]struct{}{"from_tfvar": {}}, runCfg.ValidatedInputs)
}

func TestVariablesBlockExposedThroughInclude(t *testing.T) {
	t.Parallel()

	cfg, err := parseVariablesFixture(t, map[string]string{
		"root.hcl": `
variables {
  kind   = "tfvar"
  inputs = { region = "us-east-1" }
}
`,
		config.DefaultTerragruntConfigPath: `
include "root" {
  path   = find_in_parent_folders("root.hcl")
  expose = true
}

inputs = {
  root_region = include.root.variables[0].inputs.region
  root_kind   = include.root.variables[0].kind
}
`,
	}, variablesExperimentOn)
	require.NoError(t, err)

	assert.Equal(t, map[string]any{"root_region": "us-east-1", "root_kind": "tfvar"}, cfg.Inputs)
}

func TestVariablesBlockRendersAsHCL(t *testing.T) {
	t.Parallel()

	files := map[string]string{
		config.DefaultTerragruntConfigPath: `
variables {
  kind     = "tfvar"
  validate = true
  inputs   = { name = "value", count = 2 }
}
`,
	}

	cfg, err := parseVariablesFixture(t, files, variablesExperimentOn)
	require.NoError(t, err)

	var rendered strings.Builder

	_, err = cfg.WriteTo(&rendered)
	require.NoError(t, err)

	reparsed, err := parseVariablesFixture(t, map[string]string{
		config.DefaultTerragruntConfigPath: rendered.String(),
	}, variablesExperimentOn)
	require.NoError(t, err)

	assert.Equal(t, cfg.Variables, reparsed.Variables)
}

// parseDuplicateInputFixture parses configName and returns the diagnostics it raises, each checked to reach the
// handler bound to the file its subject points into. A config named other than terragrunt.hcl sits at the fixture root.
func parseDuplicateInputFixture(
	t *testing.T,
	files map[string]string,
	configName string,
) (hcl.Diagnostics, error) {
	t.Helper()

	root := venvtest.Root("/fixture")

	tree := make(map[string]string, len(files))

	for name, contents := range files {
		if name == config.DefaultTerragruntConfigPath {
			name = filepath.Join("unit", name)
		}

		tree[name] = contents
	}

	cfgPath := filepath.Join(root, configName)
	if configName == config.DefaultTerragruntConfigPath {
		cfgPath = filepath.Join(root, "unit", configName)
	}

	v := venvtest.New().WithFS(venvtest.NewFS(t, root, tree))
	ctx, pctx := newTestParsingContext(t, cfgPath)
	require.NoError(t, pctx.Experiments.EnableExperiment(experiment.VariablesBlock))

	var collected hcl.Diagnostics

	pctx.Parser.DiagnosticsHandler = func(file *hcl.File, diags hcl.Diagnostics) (hcl.Diagnostics, error) {
		for _, diag := range diags {
			if diag.Summary != "Duplicate input" {
				continue
			}

			assert.Equal(t, diag.Subject.Filename, file.Body.MissingItemRange().Filename)

			collected = append(collected, diag)
		}

		return diags, nil
	}

	_, err := config.ParseConfigFile(ctx, logger.CreateLogger(), v, pctx, cfgPath, nil)

	return collected, err
}

// variablesExperiment is whether parseVariablesFixture enables the variables-block experiment.
type variablesExperiment int

const (
	variablesExperimentOff variablesExperiment = iota
	variablesExperimentOn
)

// parseVariablesFixture parses the unit config files[terragrunt.hcl]. The unit sits one directory below the other
// files, so find_in_parent_folders reaches them.
func parseVariablesFixture(
	t *testing.T,
	files map[string]string,
	exp variablesExperiment,
) (*config.TerragruntConfig, error) {
	t.Helper()

	root := venvtest.Root("/fixture")
	cfgPath := filepath.Join(root, "unit", config.DefaultTerragruntConfigPath)

	tree := make(map[string]string, len(files))

	for name, contents := range files {
		if name == config.DefaultTerragruntConfigPath {
			name = filepath.Join("unit", name)
		}

		tree[name] = contents
	}

	v := venvtest.New().WithFS(venvtest.NewFS(t, root, tree))
	ctx, pctx := newTestParsingContext(t, cfgPath)

	if exp == variablesExperimentOn {
		require.NoError(t, pctx.Experiments.EnableExperiment(experiment.VariablesBlock))
	}

	return config.ParseConfigFile(ctx, logger.CreateLogger(), v, pctx, cfgPath, nil)
}
