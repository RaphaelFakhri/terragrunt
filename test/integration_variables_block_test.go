package test_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gruntwork-io/terragrunt/internal/view/diagnostic"
	"github.com/gruntwork-io/terragrunt/test/helpers"
)

const testFixtureVariablesBlock = "fixtures/variables-block"

func TestHclValidateVariablesBlockDuplicateInput(t *testing.T) {
	t.Parallel()

	tmpEnvPath := helpers.CopyEnvironment(t, testFixtureVariablesBlock)
	fixturePath := filepath.Join(tmpEnvPath, testFixtureVariablesBlock)
	unitPath := filepath.Join(fixturePath, "duplicate")

	stdout, _, err := helpers.RunTerragruntCommandWithOutput(
		t,
		"terragrunt hcl validate --json --experiment variables-block --working-dir "+unitPath,
	)
	require.Error(t, err)

	var diags diagnostic.Diagnostics

	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(stdout)), &diags))
	require.Len(t, diags, 1)

	diag := diags[0]

	assert.Equal(t, "Duplicate input", diag.Summary)
	assert.Contains(t, diag.Detail, filepath.Join(fixturePath, "root.hcl")+":3,14-23")

	require.NotNil(t, diag.Range)
	assert.Equal(t, filepath.Join(unitPath, "terragrunt.hcl"), diag.Range.Filename)
	assert.Equal(t, 9, diag.Range.Start.Line)
	assert.Equal(t, 5, diag.Range.Start.Column)
	assert.Equal(t, 14, diag.Range.End.Column)

	require.NotNil(t, diag.Snippet)
	assert.Equal(t, `    from_root = "child"`, diag.Snippet.Code)
	assert.Equal(t, 4, diag.Snippet.HighlightStartOffset)
	assert.Equal(t, 13, diag.Snippet.HighlightEndOffset)
}
