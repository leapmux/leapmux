package launch

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/leapmux/leapmux/internal/util/envprobe"
)

// EnvMetadata selects the native environment values retained in the launch
// preamble. Program is the absolute path of a worker executable that handles
// envprobe's private command. Values never enter shell source or arguments.
type EnvMetadata struct {
	Program   string
	Variables []string
}

func (metadata *EnvMetadata) arguments(prefix string) []string {
	if !filepath.IsAbs(metadata.Program) {
		panic("the native environment helper must use an absolute executable path")
	}
	if prefix == "" {
		panic("the native environment helper requires a metadata prefix")
	}
	args, err := (envprobe.Request{Variables: metadata.Variables, MetadataPrefix: prefix}).Arguments()
	if err != nil {
		panic(err)
	}
	return args
}

func quoteArguments(arguments []string, quote func(string) string) string {
	words := make([]string, len(arguments))
	for i, argument := range arguments {
		words[i] = quote(argument)
	}
	return strings.Join(words, " ")
}

func shellEnvMetadata(metadata *EnvMetadata, prefix string, quote func(string) string) string {
	if metadata == nil {
		return ""
	}
	command := quote(metadata.Program) + " " + quoteArguments(metadata.arguments(prefix), quote)
	// Best-effort metadata leaves the snapshot unknown after a helper failure.
	return command + " || true; "
}

func nuEnvMetadata(metadata *EnvMetadata, prefix string) string {
	if metadata == nil {
		return ""
	}
	command := "^" + nuQuote(metadata.Program) + " " + quoteArguments(metadata.arguments(prefix), nuQuote)
	return fmt.Sprintf("do { let leapmuxNativeEnv = (try { do -i { %s } | complete } catch { {stdout: '', exit_code: 1} }); if $leapmuxNativeEnv.exit_code == 0 { print --no-newline $leapmuxNativeEnv.stdout } }; ", command)
}

func pwshEnvMetadata(metadata *EnvMetadata, prefix string) string {
	if metadata == nil {
		return ""
	}
	arguments := quoteArguments(metadata.arguments(prefix), legacyNativeArgument)
	// ProcessStartInfo preserves the literal helper path and inherited stdin.
	return fmt.Sprintf("& { $leapmuxEnvProcess = $null; try { "+
		"$leapmuxEnvStart = [System.Diagnostics.ProcessStartInfo]::new(); "+
		"$leapmuxEnvStart.FileName = %s; $leapmuxEnvStart.Arguments = %s; "+
		"$leapmuxEnvStart.UseShellExecute = $false; $leapmuxEnvStart.CreateNoWindow = $true; "+
		"$leapmuxEnvStart.WorkingDirectory = $ExecutionContext.SessionState.Path.CurrentFileSystemLocation.ProviderPath; "+
		"$leapmuxEnvProcess = [System.Diagnostics.Process]::Start($leapmuxEnvStart); $leapmuxEnvProcess.WaitForExit() "+
		"} catch { } finally { if ($null -ne $leapmuxEnvProcess) { $leapmuxEnvProcess.Dispose() } } }; ", pwshQuote(metadata.Program), pwshQuote(arguments))
}
