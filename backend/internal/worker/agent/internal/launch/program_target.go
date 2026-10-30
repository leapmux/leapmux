package launch

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// ProgramTarget passes a native executable selection to a provider helper.
// PowerShell resolves executable aliases in the current profile. Other shells
// pass the target for the helper to resolve through its inherited PATH.
// The helper must remove Variable before starting the target's children.
type ProgramTarget struct {
	Program  string
	Variable string
}

func checkProgramTarget(target *ProgramTarget) {
	if target == nil {
		return
	}
	if !shellIdentifier.MatchString(target.Variable) {
		panic("the program target variable must be a plain identifier")
	}
	if target.Program == "" || !utf8.ValidString(target.Program) || strings.ContainsRune(target.Program, '\x00') {
		panic("the program target must be a nonempty UTF-8 executable name or path")
	}
}

func programTargetEntries(target *ProgramTarget) []string {
	if target == nil {
		return nil
	}
	return []string{target.Variable + "=" + target.Program}
}

func pwshProgramTarget(target *ProgramTarget) string {
	if target == nil {
		return ""
	}
	return fmt.Sprintf("& { %sif (-not $leapmuxNative) { throw 'the provider helper requires a native executable target' }; "+
		"$env:%s = if ($null -ne $leapmuxPath) { $leapmuxPath } else { $leapmuxCommand.Path } }; ",
		pwshResolveCommand(target.Program, "exit 127"), target.Variable)
}
