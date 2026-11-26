// Package clitest checks the rules that every command tree built with package
// cli keeps. A test of the binary runs CheckTree over the whole tree, so a
// command that a later change adds is checked without a test of its own.
package clitest

import (
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"

	"github.com/leapmux/leapmux/internal/cli"
)

// TestingT is the part of *testing.T that CheckTree uses. A test of the
// checker itself passes a recorder.
type TestingT interface {
	assert.TestingT
	Helper()
}

// TreeRules holds what a tree states about its own vocabulary.
type TreeRules struct {
	// Targets lists the argument names that select an existing object, such
	// as "tab". A command that changes state takes such an argument as a
	// REQUIRED one: an optional one defaults to the caller's own object, so
	// a bare invocation would act on the tab that runs it. Each such
	// argument also states its forms in the help.
	Targets []string
	// RequireCompleteTargets requires every leaf that declares an argument in
	// Targets to register a positional completion function (Leaf.Complete).
	RequireCompleteTargets bool
	// RequireFlagGroupsAt requires every leaf with at least this many visible
	// local flags to organize them into named sections via cli.SetFlagGroup.
	// Zero disables the check.
	RequireFlagGroupsAt int
}

// CheckTree checks every leaf under root that cli.NewLeaf built. root is the
// command whose name each example starts with, such as "leapmux".
func CheckTree(t TestingT, root *cobra.Command, rules TreeRules) {
	t.Helper()
	leaves := 0
	walkLeaves(root, func(leaf *cobra.Command) {
		leaves++
		path := leaf.CommandPath()
		checkSummary(t, path, leaf.Short)
		checkFlags(t, leaf, rules)
		checkTargets(t, leaf, rules)
		examples := cli.ExamplesOf(leaf)
		assert.NotEmptyf(t, examples, "%s has no example", path)
		for _, ex := range examples {
			checkExample(t, root, leaf, ex)
		}
	})
	assert.NotZero(t, leaves, "the walk must find the leaves of the tree")
}

// walkLeaves calls fn for each runnable command of the tree.
func walkLeaves(cmd *cobra.Command, fn func(*cobra.Command)) {
	if cmd.Runnable() && !cmd.HasSubCommands() && cmd.Name() != "help" {
		fn(cmd)
	}
	for _, child := range cmd.Commands() {
		walkLeaves(child, fn)
	}
}

// checkSummary checks the one line that a group's help prints for the
// command: a phrase that starts with a capital letter and ends without a
// period, as every other summary does.
func checkSummary(t TestingT, path, short string) {
	t.Helper()
	if !assert.NotEmptyf(t, short, "%s has no summary", path) {
		return
	}
	assert.Truef(t, unicode.IsUpper([]rune(short)[0]), "the summary of %s starts with a capital letter: %q", path, short)
	assert.Falsef(t, strings.HasSuffix(short, "."), "the summary of %s ends without a period: %q", path, short)
}

// checkFlags checks the help of each flag that the leaf declares: a phrase
// that starts with a lowercase letter and ends without a period, so the
// clauses that the help appends ("required", "one of: ...") read as one line.
func checkFlags(t TestingT, leaf *cobra.Command, rules TreeRules) {
	t.Helper()
	visible := 0
	hasGroup := false
	leaf.LocalNonPersistentFlags().VisitAll(func(f *pflag.Flag) {
		if f.Hidden || f.Name == "help" {
			return
		}
		visible++
		if cli.FlagGroupOf(f) != "" {
			hasGroup = true
		}
		if !assert.NotEmptyf(t, f.Usage, "%s --%s has no help", leaf.CommandPath(), f.Name) {
			return
		}
		assert.Falsef(t, startsWithCapitalizedWord(f.Usage), "the help of %s --%s starts with a lowercase letter: %q", leaf.CommandPath(), f.Name, f.Usage)
		assert.Falsef(t, strings.HasSuffix(f.Usage, "."), "the help of %s --%s ends without a period: %q", leaf.CommandPath(), f.Name, f.Usage)
		// Guard against hand-written "one of: ..." in flag help when cli.SetEnum
		// was forgotten (SetEnum populates the enum annotation and drives shell
		// completion and `leapmux commands --json`).
		if strings.Contains(strings.ToLower(f.Usage), "one of:") {
			assert.NotEmptyf(t, cli.EnumOf(f), "%s --%s mentions \"one of:\" in usage without cli.SetEnum", leaf.CommandPath(), f.Name)
		}
	})
	if rules.RequireFlagGroupsAt > 0 && visible >= rules.RequireFlagGroupsAt {
		assert.Truef(t, hasGroup, "%s declares %d flags without cli.SetFlagGroup", leaf.CommandPath(), visible)
	}
}

// startsWithCapitalizedWord reports whether text starts with a word whose
// first letter alone is upper case, such as "Path". An acronym such as "URL"
// or "OAuth" keeps its own case, so it does not count.
func startsWithCapitalizedWord(text string) bool {
	word, _, _ := strings.Cut(text, " ")
	runes := []rune(word)
	if len(runes) == 0 || !unicode.IsUpper(runes[0]) {
		return false
	}
	for _, r := range runes[1:] {
		if unicode.IsUpper(r) {
			return false
		}
	}
	return true
}

// checkTargets checks the arguments that select an existing object.
func checkTargets(t TestingT, leaf *cobra.Command, rules TreeRules) {
	t.Helper()
	hasTarget := false
	for _, spec := range cli.ArgsOf(leaf) {
		if !slices.Contains(rules.Targets, spec.Name) {
			continue
		}
		hasTarget = true
		assert.NotEmptyf(t, spec.Help, "<%s> of %s states no forms", spec.Name, leaf.CommandPath())
		if cli.IsMutating(leaf) {
			assert.Truef(t, spec.Required,
				"%s changes state, so its <%s> must be required: an optional one acts on the caller's own object", leaf.CommandPath(), spec.Name)
		}
	}
	if hasTarget && rules.RequireCompleteTargets {
		assert.NotNilf(t, leaf.ValidArgsFunction, "%s takes a target argument and must register Complete", leaf.CommandPath())
	}
}

// checkExample checks that an example runs the leaf that declares it, with
// flags the leaf knows and the argument count its Use line allows. A renamed
// command or flag then fails here instead of in the help that a reader
// copies from.
func checkExample(t TestingT, root, leaf *cobra.Command, ex cli.Example) {
	t.Helper()
	path := leaf.CommandPath()
	if ex.Comment != "" {
		assert.Truef(t, strings.HasSuffix(ex.Comment, "."), "the comment of an example of %s is a sentence: %q", path, ex.Comment)
	}
	// A pipeline can run the binary more than once, as in "... get | ...
	// set". The run that reaches the leaf is the one this example shows.
	var rest []string
	reached := false
	for _, words := range commandRuns(ex.Command, root.Name()) {
		found, args, err := root.Find(words)
		if err == nil && found == leaf {
			rest, reached = args, true
			break
		}
	}
	if !assert.Truef(t, reached, "an example of %s does not run it with full command names: %s", path, ex.Command) {
		return
	}
	// Find leaves the flags and the arguments in rest. Clone the allowed flag
	// specs into a fresh set with throwaway values so parsing an example never
	// mutates the live flags attached to the command tree.
	flags := pflag.NewFlagSet(path, pflag.ContinueOnError)
	addClone := func(f *pflag.Flag) {
		if flags.Lookup(f.Name) != nil {
			return
		}
		flags.AddFlag(cloneFlagForExample(f))
	}
	leaf.LocalFlags().VisitAll(addClone)
	leaf.InheritedFlags().VisitAll(func(f *pflag.Flag) {
		if cli.IsInheritedFlagDisallowed(leaf, f.Name) {
			return
		}
		addClone(f)
	})
	if flags.Lookup("help") == nil {
		flags.BoolP("help", "h", false, "")
	}
	if !assert.NoErrorf(t, flags.Parse(rest), "an example of %s does not parse: %s", path, ex.Command) {
		return
	}
	if leaf.Args != nil {
		assert.NoErrorf(t, leaf.Args(leaf, flags.Args()), "an example of %s has the wrong arguments: %s", path, ex.Command)
	}
}

type exampleFlagValue struct {
	flagType string
	val      string
}

func (v *exampleFlagValue) String() string { return v.val }
func (v *exampleFlagValue) Type() string   { return v.flagType }

func (v *exampleFlagValue) Set(s string) error {
	switch v.flagType {
	case "bool":
		if _, err := strconv.ParseBool(s); err != nil {
			return err
		}
	case "int", "int8", "int16", "int32", "int64":
		if _, err := strconv.ParseInt(s, 0, 64); err != nil {
			return err
		}
	case "uint", "uint8", "uint16", "uint32", "uint64":
		if _, err := strconv.ParseUint(s, 0, 64); err != nil {
			return err
		}
	case "float32", "float64":
		if _, err := strconv.ParseFloat(s, 64); err != nil {
			return err
		}
	}
	v.val = s
	return nil
}

func cloneFlagForExample(f *pflag.Flag) *pflag.Flag {
	return &pflag.Flag{
		Name:        f.Name,
		Shorthand:   f.Shorthand,
		Usage:       f.Usage,
		Value:       &exampleFlagValue{flagType: f.Value.Type()},
		DefValue:    f.DefValue,
		NoOptDefVal: f.NoOptDefVal,
	}
}

// commandRuns returns the words of each run of name in a shell command line,
// honoring single and double quotes. A run ends at a pipe, a redirection, a
// command separator or the end of a command substitution.
func commandRuns(line, name string) [][]string {
	var runs [][]string
	for offset := 0; ; {
		i := strings.Index(line[offset:], name+" ")
		if i < 0 {
			return runs
		}
		start := offset + i + len(name) + 1
		words, end := wordsUntilOperator(line[start:])
		runs = append(runs, words)
		offset = start + end
	}
}

// wordsUntilOperator splits text into words up to the first shell operator
// outside quotes, and returns the words and the length of text it read.
func wordsUntilOperator(text string) ([]string, int) {
	var words []string
	var cur strings.Builder
	inWord := false
	var quote rune
	flush := func() {
		if inWord {
			words = append(words, cur.String())
			cur.Reset()
			inWord = false
		}
	}
	for i, r := range text {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case strings.ContainsRune("|;)<>&", r):
			flush()
			return words, i
		case unicode.IsSpace(r):
			flush()
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	flush()
	return words, len(text)
}
