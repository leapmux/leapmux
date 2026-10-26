package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"

	"github.com/stretchr/testify/require"
)

func TestRewriteGo(t *testing.T) {
	source := "// use example.com/lib/v7\npackage fixture\nimport (\n alias \"example.com/lib/v7/sub\" // retain\n _ `gopkg.in/yaml.v2`\n \"example.com/lib/v70\"\n)\nconst unchanged = \"example.com/lib/v7\"\n"
	updated, err := rewriteGo([]byte(source), []moduleChange{{Old: "example.com/lib/v7", New: "example.com/lib/v8", Version: "v8.2.0"}, {Old: "gopkg.in/yaml.v2", New: "gopkg.in/yaml.v3", Version: "v3.0.1"}})
	require.NoError(t, err)
	expected := strings.Replace(source, "alias \"example.com/lib/v7/sub\"", "alias \"example.com/lib/v8/sub\"", 1)
	expected = strings.Replace(expected, "`gopkg.in/yaml.v2`", "\"gopkg.in/yaml.v3\"", 1)
	require.Equal(t, expected, string(updated))
}

func TestRewriteMod(t *testing.T) {
	for _, source := range []string{
		"module example.com/app\ngo 1.27.1\nrequire example.com/lib/v7 v7.0.0 // keep\nreplace example.com/lib/v7 => ../local\ntool example.com/lib/v7/cmd/tool\nexclude example.com/lib/v7 v7.0.1\n",
		"module example.com/app\ngo 1.27.1\nrequire (\n example.com/lib/v7 v7.0.0 // keep\n)\nreplace (\n example.com/lib/v7 => ../local\n example.com/other => example.com/lib/v7 v7.1.0\n)\ntool (\n example.com/lib/v7/cmd/tool\n)\n",
	} {
		updated, err := rewriteMod([]byte(source), []moduleChange{{Old: "example.com/lib/v7", New: "example.com/lib/v8", Version: "v8.2.0"}})
		require.NoError(t, err)
		expected := strings.ReplaceAll(source, "example.com/lib/v7", "example.com/lib/v8")
		expected = strings.ReplaceAll(expected, "v7.0.0", "v8.2.0")
		expected = strings.ReplaceAll(expected, "v7.1.0", "v8.2.0")
		expected = strings.ReplaceAll(expected, "exclude example.com/lib/v8", "exclude example.com/lib/v7")
		require.Equal(t, expected, string(updated))
	}
}

func TestRejectMalformedSourcesAndInput(t *testing.T) {
	require.ErrorContains(t, run(strings.NewReader("[]"+strings.Repeat(" ", 1<<20))), "input limit")
	_, err := rewriteGo([]byte("invalid"), nil)
	require.Error(t, err)
	_, err = rewriteMod([]byte("invalid"), nil)
	require.Error(t, err)
	for _, input := range []string{"", "null", "{}", "[] []", `[{"old":"github.com/leapmux/leapmux","new":"example.com/moved"}]`, `[{"old":"../local","new":"example.com/moved"}]`, `[{"old":"example.com/lib","new":"example.com/lib","extra":1}]`} {
		require.Error(t, run(strings.NewReader(input)))
	}
}

func TestModuleVersionMigration(t *testing.T) {
	source := "module example.com/app\ngo 1.27.1\nrequire example.com/lib/v7 v7.0.0 // preserve\nreplace example.com/lib/v7 v7.0.0 => example.com/lib/v7 v7.1.0\nexclude example.com/lib/v7 v7.9.0\n"
	updated, err := rewriteMod([]byte(source), []moduleChange{{Old: "example.com/lib/v7", New: "example.com/lib/v8", Version: "v8.2.0"}})
	require.NoError(t, err)
	require.Equal(t, "module example.com/app\ngo 1.27.1\nrequire example.com/lib/v8 v8.2.0 // preserve\nreplace example.com/lib/v8 v8.2.0 => example.com/lib/v8 v8.2.0\nexclude example.com/lib/v7 v7.9.0\n", string(updated))
}

func TestMigrationSurvivesTidy(t *testing.T) {
	root := testutil.NewGitRepo(t)
	t.Chdir(root)
	require.NoError(t, os.MkdirAll("backend", 0o755))
	require.NoError(t, os.MkdirAll("library", 0o755))
	require.NoError(t, os.WriteFile("library/go.mod", []byte("module example.com/lib/v8\ngo 1.27.1\n"), 0o644))
	require.NoError(t, os.WriteFile("library/library.go", []byte("package lib\nconst Value = 8\n"), 0o644))
	require.NoError(t, os.WriteFile("backend/go.mod", []byte("module example.com/app\ngo 1.27.1\nrequire example.com/lib/v7 v7.0.0\nreplace example.com/lib/v7 => ../library\n"), 0o644))
	require.NoError(t, os.WriteFile("backend/main.go", []byte("package app\nimport alias \"example.com/lib/v7\" // preserve\nvar Value = alias.Value\n"), 0o644))
	require.NoError(t, os.MkdirAll("site", 0o755))
	require.NoError(t, os.WriteFile("site/go.mod", []byte("module example.com/site\ngo 1.27.1\nrequire example.com/lib/v7 v7.0.0\nreplace example.com/lib/v7 => ../library\n"), 0o644))
	require.NoError(t, os.WriteFile("site/site.go", []byte("package site\nimport alias \"example.com/lib/v7\"\nvar Value = alias.Value\n"), 0o644))
	command := exec.Command("git", "add", "backend", "library", "site")
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
	require.NoError(t, run(strings.NewReader(`[{"old":"example.com/lib/v7","new":"example.com/lib/v8","version":"v8.2.0","directories":["backend","site"]}]`)))
	command = exec.Command("go", "mod", "tidy")
	command.Dir = filepath.Join(root, "backend")
	command.Env = append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local")
	output, err = command.CombinedOutput()
	require.NoError(t, err, string(output))
	mod, err := os.ReadFile("backend/go.mod")
	require.NoError(t, err)
	require.Contains(t, string(mod), "require example.com/lib/v8 v8.2.0")
	require.Contains(t, string(mod), "replace example.com/lib/v8 => ../library")
	require.NotContains(t, string(mod), "/v7")
	source, err := os.ReadFile("backend/main.go")
	require.NoError(t, err)
	require.Contains(t, string(source), `alias "example.com/lib/v8" // preserve`)
	command = exec.Command("go", "mod", "tidy")
	command.Dir = filepath.Join(root, "site")
	command.Env = append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local")
	output, err = command.CombinedOutput()
	require.NoError(t, err, string(output))
	mod, err = os.ReadFile("site/go.mod")
	require.NoError(t, err)
	require.Contains(t, string(mod), "require example.com/lib/v8 v8.2.0")
}

func TestRewriteImportsIsIdempotent(t *testing.T) {
	source := []byte("package app\nimport (\n _ \"example.com/lib/pkg\"\n _ \"example.com/lib/v2/already\"\n)\n")
	changes := []moduleChange{{Old: "example.com/lib", New: "example.com/lib/v2", Version: "v2.0.0"}}
	updated, err := rewriteGo(source, changes)
	require.NoError(t, err)
	require.Equal(t, "package app\nimport (\n _ \"example.com/lib/v2/pkg\"\n _ \"example.com/lib/v2/already\"\n)\n", string(updated))
	again, err := rewriteGo(updated, changes)
	require.NoError(t, err)
	require.Equal(t, string(updated), string(again))
}

func TestRewriteModPreservesIndependentSubmodule(t *testing.T) {
	source := []byte("module example.com/app\ngo 1.27.1\nrequire example.com/lib v1.0.0\nrequire example.com/lib/extra v1.5.0\nreplace example.com/lib/extra => ../extra\n")
	updated, err := rewriteMod(source, []moduleChange{{Old: "example.com/lib", New: "example.com/lib/v2", Version: "v2.0.0"}})
	require.NoError(t, err)
	require.Equal(t, "module example.com/app\ngo 1.27.1\nrequire example.com/lib/v2 v2.0.0\nrequire example.com/lib/extra v1.5.0\nreplace example.com/lib/extra => ../extra\n", string(updated))
}

func TestModuleScopeAndImportOwnership(t *testing.T) {
	root := testutil.NewGitRepo(t)
	t.Chdir(root)
	require.NoError(t, os.MkdirAll("backend", 0o755))
	require.NoError(t, os.MkdirAll("site", 0o755))
	require.NoError(t, os.WriteFile("backend/go.mod", []byte("module example.com/backend\ngo 1.27.1\nrequire example.com/lib/v2 v2.0.0\nrequire example.com/lib/extra v1.5.0\n"), 0o644))
	require.NoError(t, os.WriteFile("backend/app.go", []byte("package app\nimport (\n _ \"example.com/lib/pkg\"\n _ \"example.com/lib/extra/pkg\"\n)\n"), 0o644))
	const siteModule = "module example.com/site\ngo 1.27.1\nrequire example.com/lib v1.0.0\n"
	const siteSource = "package site\nimport _ \"example.com/lib/pkg\"\n"
	require.NoError(t, os.WriteFile("site/go.mod", []byte(siteModule), 0o644))
	require.NoError(t, os.WriteFile("site/site.go", []byte(siteSource), 0o644))
	output, err := exec.Command("git", "add", "backend", "site").CombinedOutput()
	require.NoError(t, err, string(output))
	require.NoError(t, run(strings.NewReader(`[{"old":"example.com/lib","new":"example.com/lib/v2","version":"v2.0.0","directories":["backend","desktop/go"]}]`)))
	updated, err := os.ReadFile("backend/app.go")
	require.NoError(t, err)
	require.Equal(t, "package app\nimport (\n _ \"example.com/lib/v2/pkg\"\n _ \"example.com/lib/extra/pkg\"\n)\n", string(updated))
	updated, err = os.ReadFile("site/site.go")
	require.NoError(t, err)
	require.Equal(t, siteSource, string(updated))
	updated, err = os.ReadFile("site/go.mod")
	require.NoError(t, err)
	require.Equal(t, siteModule, string(updated))
}
