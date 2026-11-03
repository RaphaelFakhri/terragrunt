//go:build tf

package test_test

import (
	"encoding/json"
	"maps"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gruntwork-io/terragrunt/internal/runner/run"
	"github.com/gruntwork-io/terragrunt/pkg/config"
	"github.com/gruntwork-io/terragrunt/test/helpers"
)

func TestTFVariablesBlockSetsVariables(t *testing.T) {
	t.Parallel()

	tmpEnvPath := helpers.CopyEnvironment(t, testFixtureVariablesBlock)
	unitPath := filepath.Join(tmpEnvPath, testFixtureVariablesBlock, "unit")

	helpers.RunTerragrunt(
		t,
		"terragrunt apply -auto-approve --non-interactive --experiment variables-block --working-dir "+unitPath,
	)

	stdout, _, err := helpers.RunTerragruntCommandWithOutput(
		t,
		"terragrunt output -json values --non-interactive --experiment variables-block --working-dir "+unitPath,
	)
	require.NoError(t, err)

	var values map[string]string

	require.NoError(t, json.Unmarshal([]byte(stdout), &values))

	assert.Equal(t, map[string]string{
		"from_attr": "attr",
		"from_root": "root",
		"from_env":  "env",
		"shared":    "child",
		"template":  "${literal}",
		"nulled":    "null",
		"dropped":   "default",
	}, values)

	cacheDirs, err := filepath.Glob(filepath.Join(unitPath, ".terragrunt-cache", "*", "*", "main.tf"))
	require.NoError(t, err)
	require.Len(t, cacheDirs, 1)

	workingDir := filepath.Dir(cacheDirs[0])
	assert.FileExists(t, filepath.Join(workingDir, run.TFVarsFile))
	assert.FileExists(t, filepath.Join(workingDir, run.JSONVarsFile))
}

func TestTFVariablesBlockFilesOverrideTFVarEnv(t *testing.T) {
	t.Parallel()

	tmpEnvPath := helpers.CopyEnvironment(t, testFixtureVariablesBlock)
	unitPath := filepath.Join(tmpEnvPath, testFixtureVariablesBlock, "unit")

	v := helpers.RunVenv(t)
	env := maps.Clone(v.Env)
	env["TF_VAR_from_root"] = "ambient"
	env["TF_VAR_shared"] = "ambient"
	env["TF_VAR_from_env"] = "ambient"
	v = v.WithEnv(env)

	_, _, err := helpers.RunTerragruntCommandWithOutputWithVenv(
		t,
		v,
		"terragrunt apply -auto-approve --non-interactive --experiment variables-block --working-dir "+unitPath,
	)
	require.NoError(t, err)

	stdout, _, err := helpers.RunTerragruntCommandWithOutputWithVenv(
		t,
		v,
		"terragrunt output -json values --non-interactive --experiment variables-block --working-dir "+unitPath,
	)
	require.NoError(t, err)

	var values map[string]string

	require.NoError(t, json.Unmarshal([]byte(stdout), &values))

	assert.Equal(t, "root", values["from_root"], "a tfvar input must override TF_VAR_from_root")
	assert.Equal(t, "child", values["shared"], "a json input must override TF_VAR_shared")
	assert.Equal(t, "ambient", values["from_env"], "TF_VAR_from_env must override an env input")
}

func TestTFVariablesBlockValidateRejectsUndeclaredVariables(t *testing.T) {
	t.Parallel()

	tmpEnvPath := helpers.CopyEnvironment(t, testFixtureVariablesBlock)
	unitPath := filepath.Join(tmpEnvPath, testFixtureVariablesBlock, "undeclared")

	_, _, err := helpers.RunTerragruntCommandWithOutput(
		t,
		"terragrunt plan --non-interactive --experiment variables-block --working-dir "+unitPath,
	)

	var undeclaredErr run.UndeclaredInputsError

	require.ErrorAs(t, err, &undeclaredErr)
	assert.Equal(t, []string{"undeclared"}, undeclaredErr.Names)
}

func TestTFVariablesBlockRequiresExperiment(t *testing.T) {
	t.Parallel()

	tmpEnvPath := helpers.CopyEnvironment(t, testFixtureVariablesBlock)
	unitPath := filepath.Join(tmpEnvPath, testFixtureVariablesBlock, "unit")

	_, _, err := helpers.RunTerragruntCommandWithOutput(
		t,
		"terragrunt plan --non-interactive --working-dir "+unitPath,
	)

	var experimentErr config.VariablesBlockRequiresExperimentError

	require.ErrorAs(t, err, &experimentErr)
}
