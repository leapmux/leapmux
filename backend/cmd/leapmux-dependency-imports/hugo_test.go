package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRewriteHugoImportScalars(t *testing.T) {
	change := []moduleChange{{Old: "example.com/theme", New: "example.com/theme/v2", Version: "v2.0.0"}}
	for _, scalar := range []string{"example.com/theme/assets", `'example.com/theme/assets'`, `"example.com/theme/assets"`, `!!str "example.com/theme/assets"`, `&theme "example.com/theme/assets"`} {
		source := "# 한글 comments remain\nmodule: {imports: [{path: " + scalar + "}]} # retain\n"
		updated, err := rewriteHugoImports([]byte(source), change, nil)
		require.NoError(t, err)
		require.Equal(t, strings.Replace(source, "example.com/theme/assets", "example.com/theme/v2/assets", 1), string(updated))
		again, err := rewriteHugoImports(updated, change, nil)
		require.NoError(t, err)
		require.Equal(t, updated, again)
	}
	for _, style := range []string{"|-", ">-"} {
		source := "module:\n  imports:\n    - path: " + style + " # retain\n        example.com/theme/assets\n"
		updated, err := rewriteHugoImports([]byte(source), change, nil)
		require.NoError(t, err)
		require.Equal(t, strings.Replace(source, "example.com/theme/assets", "example.com/theme/v2/assets", 1), string(updated))
	}
}

func TestRewriteHugoImportsKeepsUnicodeSourcePositions(t *testing.T) {
	for _, prefix := range []string{"", "\ufeff"} {
		source := prefix + "module: {imports: [{description: 한글, path: 'example.com/theme'}]} # retain\n"
		updated, err := rewriteHugoImports([]byte(source), []moduleChange{{Old: "example.com/theme", New: "example.com/theme/v2", Version: "v2.0.0"}}, nil)
		require.NoError(t, err)
		require.Equal(t, strings.Replace(source, "'example.com/theme'", "'example.com/theme/v2'", 1), string(updated))
	}
}

func TestRewriteHugoImportAliasKeepsUnrelatedAnchor(t *testing.T) {
	source := "theme: &theme example.com/theme\nmodule:\n  imports:\n    - path: *theme # retain\n"
	updated, err := rewriteHugoImports([]byte(source), []moduleChange{{Old: "example.com/theme", New: "example.com/theme/v2", Version: "v2.0.0"}}, nil)
	require.NoError(t, err)
	require.Equal(t, "theme: &theme example.com/theme\nmodule:\n  imports:\n    - path: example.com/theme/v2 # retain\n", string(updated))
}

func TestRewriteHugoImportsRejectsMalformedConfiguration(t *testing.T) {
	changes := []moduleChange{{Old: "example.com/theme", New: "example.com/theme/v2", Version: "v2.0.0"}}
	for _, source := range []string{"module: [", "module: text", "module: { imports: {} }", "module: { imports: [{path: []}] }", "module: {imports: [], Imports: []}"} {
		_, err := rewriteHugoImports([]byte(source), changes, nil)
		require.Error(t, err, source)
	}
	for _, source := range []string{"", "title: unrelated\n", "module: {}\n", "module: null\n"} {
		updated, err := rewriteHugoImports([]byte(source), changes, nil)
		require.NoError(t, err)
		require.Equal(t, source, string(updated))
	}
}
