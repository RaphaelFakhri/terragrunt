package run_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gruntwork-io/terragrunt/internal/report"
	"github.com/gruntwork-io/terragrunt/internal/runner/run"
	"github.com/gruntwork-io/terragrunt/internal/runner/run/creds"
	"github.com/gruntwork-io/terragrunt/internal/runner/runcfg"
	"github.com/gruntwork-io/terragrunt/internal/vexec"
	"github.com/gruntwork-io/terragrunt/internal/vfs"
	"github.com/gruntwork-io/terragrunt/test/helpers/logger"
	"github.com/gruntwork-io/terragrunt/test/helpers/venvtest"
)

func TestRunWritesVarFilesToCacheDir(t *testing.T) {
	t.Parallel()

	s := setupRunE2EScaffold(t)
	files := &varFilesRecorder{}
	v := venvtest.New().WithExec(vexec.NewMemExec(files.handler())).WithFS(vfs.NewOSFS())

	cfg := &runcfg.RunConfig{
		Inputs: map[string]any{"from_env": "env"},
		TFVarsInputs: map[string]any{
			"name":     "value",
			"template": "${not_a_reference}",
			"tags":     map[string]any{"team": "platform"},
			"unset":    nil,
		},
		JSONInputs: map[string]any{
			"count":    json.Number("2"),
			"template": "${not_a_reference}",
		},
	}

	require.NoError(
		t,
		run.Run(t.Context(), logger.CreateLogger(), v, newRunE2EOpts(t, s, "plan"), report.NewReport(), cfg, creds.NewGetter()),
	)

	workingDir := cacheWorkingDir(t, s)
	tfvars := filepath.Join(workingDir, run.TFVarsFile)
	jsonVars := filepath.Join(workingDir, run.JSONVarsFile)

	assert.Equal(t, `name = "value"
tags = {
  team = "platform"
}
template = "$${not_a_reference}"
unset    = null
`, files.contents[tfvars])
	assert.JSONEq(t, `{"count": 2, "template": "${not_a_reference}"}`, files.contents[jsonVars])

	assert.Contains(t, files.env, "TF_VAR_from_env=env")
	assert.NotContains(t, files.env, `TF_VAR_name="value"`)

	assert.FileExists(t, tfvars, "the tfvars file must stay in the cache directory after the run")
	assert.FileExists(t, jsonVars, "the tfvars.json file must stay in the cache directory after the run")
}

func TestRunWritesVarFilesForInputsSetInEnv(t *testing.T) {
	t.Parallel()

	s := setupRunE2EScaffold(t)
	files := &varFilesRecorder{}
	v := venvtest.New().
		WithExec(vexec.NewMemExec(files.handler())).
		WithFS(vfs.NewOSFS()).
		WithEnv(map[string]string{"TF_VAR_name": `"from-env"`, "TF_VAR_other": `"from-env"`})

	cfg := &runcfg.RunConfig{
		TFVarsInputs: map[string]any{"name": "from-config"},
		JSONInputs:   map[string]any{"other": "from-config"},
	}

	require.NoError(
		t,
		run.Run(t.Context(), logger.CreateLogger(), v, newRunE2EOpts(t, s, "plan"), report.NewReport(), cfg, creds.NewGetter()),
	)

	workingDir := cacheWorkingDir(t, s)

	assert.Equal(t, "name = \"from-config\"\n", files.contents[filepath.Join(workingDir, run.TFVarsFile)])
	assert.JSONEq(t, `{"other": "from-config"}`, files.contents[filepath.Join(workingDir, run.JSONVarsFile)])
}

func TestRunRemovesStaleVarFilesFromCacheDir(t *testing.T) {
	t.Parallel()

	s := setupRunE2EScaffold(t)
	rec := &recorder{}
	v := venvtest.New().WithExec(vexec.NewMemExec(rec.handler(vexec.Result{}))).WithFS(vfs.NewOSFS())
	l := logger.CreateLogger()
	opts := newRunE2EOpts(t, s, "plan")

	withFiles := &runcfg.RunConfig{
		TFVarsInputs: map[string]any{"name": "value"},
		JSONInputs:   map[string]any{"name": "value"},
	}
	require.NoError(t, run.Run(t.Context(), l, v, opts, report.NewReport(), withFiles, creds.NewGetter()))

	workingDir := cacheWorkingDir(t, s)
	require.FileExists(t, filepath.Join(workingDir, run.TFVarsFile))
	require.FileExists(t, filepath.Join(workingDir, run.JSONVarsFile))

	envOnly := &runcfg.RunConfig{Inputs: map[string]any{"name": "value"}}
	require.NoError(t, run.Run(t.Context(), l, v, opts, report.NewReport(), envOnly, creds.NewGetter()))

	assert.NoFileExists(t, filepath.Join(workingDir, run.TFVarsFile))
	assert.NoFileExists(t, filepath.Join(workingDir, run.JSONVarsFile))
}

func TestRunNoDotTerragruntCacheVarFilesAreWrittenForTheRun(t *testing.T) {
	t.Parallel()

	s := setupRunE2EScaffold(t)
	files := &varFilesRecorder{}
	v := venvtest.New().WithExec(vexec.NewMemExec(files.handler())).WithFS(vfs.NewOSFS())

	cfg := &runcfg.RunConfig{
		Terraform:    runcfg.TerraformConfig{NoDotTerragruntCache: true},
		TFVarsInputs: map[string]any{"name": "value"},
		JSONInputs:   map[string]any{"other": "value"},
	}

	require.NoError(
		t,
		run.Run(
			t.Context(), logger.CreateLogger(), v, newNoDotTerragruntCacheOpts(t, s, "plan"),
			report.NewReport(), cfg, creds.NewGetter(),
		),
	)

	for _, name := range []string{run.TFVarsFile, run.JSONVarsFile} {
		path := filepath.Join(s.dir, name)

		assert.Contains(t, files.contents, path, "%s must exist in the unit directory while tofu runs", name)
		assert.NoFileExists(t, path, "%s must be removed from the unit directory after the run", name)
	}
}

func TestRunNoDotTerragruntCacheVarFilesKeepExistingFile(t *testing.T) {
	t.Parallel()

	s := setupRunE2EScaffold(t)
	tfvars := filepath.Join(s.dir, run.TFVarsFile)
	require.NoError(t, os.WriteFile(tfvars, []byte(`name = "mine"`), 0o644))

	rec := &recorder{}
	v := venvtest.New().WithExec(vexec.NewMemExec(rec.handler(vexec.Result{}))).WithFS(vfs.NewOSFS())

	cfg := &runcfg.RunConfig{
		Terraform:    runcfg.TerraformConfig{NoDotTerragruntCache: true},
		TFVarsInputs: map[string]any{"name": "value"},
	}

	err := run.Run(
		t.Context(), logger.CreateLogger(), v, newNoDotTerragruntCacheOpts(t, s, "plan"),
		report.NewReport(), cfg, creds.NewGetter(),
	)

	var exists run.VarsFileExists

	require.ErrorAs(t, err, &exists)
	assert.Equal(t, tfvars, exists.Path)
	assert.Empty(t, rec.snapshot(), "tofu must not run")

	contents, err := os.ReadFile(tfvars)
	require.NoError(t, err)
	assert.Equal(t, `name = "mine"`, string(contents))
}

func TestRunRejectsTFVarsInputWithInvalidName(t *testing.T) {
	t.Parallel()

	s := setupRunE2EScaffold(t)
	rec := &recorder{}
	v := venvtest.New().WithExec(vexec.NewMemExec(rec.handler(vexec.Result{}))).WithFS(vfs.NewOSFS())

	cfg := &runcfg.RunConfig{TFVarsInputs: map[string]any{"not-valid.name": "value"}}

	err := run.Run(
		t.Context(), logger.CreateLogger(), v, newRunE2EOpts(t, s, "plan"), report.NewReport(), cfg, creds.NewGetter(),
	)

	var nameErr run.InvalidVariableNameError

	require.ErrorAs(t, err, &nameErr)
	assert.Equal(t, "not-valid.name", nameErr.Name)
	assert.Empty(t, rec.snapshot(), "tofu must not run")
}

func TestRunValidatesDeclaredInputs(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		module     string
		undeclared []string
	}{
		{
			name:   "every validated input is declared",
			module: `variable "declared" {}`,
		},
		{
			name:       "validated inputs are not declared",
			module:     `variable "other" {}`,
			undeclared: []string{"also_missing", "declared"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s := setupRunE2EScaffold(t)
			require.NoError(t, os.WriteFile(filepath.Join(s.dir, "main.tf"), []byte(tc.module), 0o644))

			rec := &recorder{}
			v := venvtest.New().WithExec(vexec.NewMemExec(rec.handler(vexec.Result{}))).WithFS(vfs.NewOSFS())

			validated := map[string]struct{}{"declared": {}}
			if tc.undeclared != nil {
				validated["also_missing"] = struct{}{}
			}

			cfg := &runcfg.RunConfig{
				TFVarsInputs:    map[string]any{"declared": "value", "also_missing": "value"},
				ValidatedInputs: validated,
			}

			err := run.Run(
				t.Context(), logger.CreateLogger(), v, newRunE2EOpts(t, s, "plan"),
				report.NewReport(), cfg, creds.NewGetter(),
			)

			if tc.undeclared == nil {
				require.NoError(t, err)
				assert.Len(t, rec.snapshot(), 1)

				return
			}

			var undeclaredErr run.UndeclaredInputsError

			require.ErrorAs(t, err, &undeclaredErr)
			assert.Equal(t, tc.undeclared, undeclaredErr.Names)
			assert.Empty(t, rec.snapshot(), "tofu must not run")
		})
	}
}

// varFilesRecorder records, when tofu runs, the variable files in its working directory and its environment.
type varFilesRecorder struct {
	contents map[string]string
	env      []string
	mu       sync.Mutex
}

func (r *varFilesRecorder) handler() vexec.Handler {
	return func(_ context.Context, inv vexec.Invocation) vexec.Result {
		r.mu.Lock()
		defer r.mu.Unlock()

		r.contents = map[string]string{}
		r.env = inv.Env

		for _, name := range []string{run.TFVarsFile, run.JSONVarsFile} {
			path := filepath.Join(inv.Dir, name)

			contents, err := os.ReadFile(path)
			if err != nil {
				continue
			}

			r.contents[path] = string(contents)
		}

		return vexec.Result{}
	}
}

// cacheWorkingDir returns the single cache working directory a run of s copied the unit into.
func cacheWorkingDir(t *testing.T, s runE2EScaffold) string {
	t.Helper()

	mainFiles, err := filepath.Glob(filepath.Join(s.dir, ".terragrunt-cache", "*", "*", "main.tf"))
	require.NoError(t, err)
	require.Len(t, mainFiles, 1, "expected exactly one cache working directory")

	return filepath.Dir(mainFiles[0])
}
