// Package clitest checks the rules that every command tree built with package
// cli keeps. A test of the binary runs CheckTree over the whole tree, so a
// command that a later change adds is checked without a test of its own.
package clitest

import (
	"slices"
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
		checkFlags(t, leaf)
		checkTargets(t, leaf, rules)
		examples := cli.ExamplesOf(leaf)
		assert.NotEmptyf(t, examples, "%s has no example", path)
		for _, ex := range examples {
			checkExample(t, root, leaf, ex)
		}
	})
	assert.NotZero(t, leaves, "the walk must find the leaves of the tree")
}

// walkLeaves calls fn for each runnable command that parses its own flags. A
// command that disables the parse hands its arguments to another dispatcher,
// which owns their rules.
func walkLeaves(cmd *cobra.Command, fn func(*cobra.Command)) {
	if cmd.Runnable() && !cmd.HasSubCommands() && !cmd.DisableFlagParsing && cmd.Name() != "help" {
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
func checkFlags(t TestingT, leaf *cobra.Command) {
	t.Helper()
	leaf.LocalNonPersistentFlags().VisitAll(func(f *pflag.Flag) {
		if f.Hidden || f.Name == "help" {
			return
		}
		if !assert.NotEmptyf(t, f.Usage, "%s --%s has no help", leaf.CommandPath(), f.Name) {
			return
		}
		assert.Falsef(t, startsWithCapitalizedWord(f.Usage), "the help of %s --%s starts with a lowercase letter: %q", leaf.CommandPath(), f.Name, f.Usage)
		assert.Falsef(t, strings.HasSuffix(f.Usage, "."), "the help of %s --%s ends without a period: %q", leaf.CommandPath(), f.Name, f.Usage)
	})
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
	for _, spec := range cli.ArgsOf(leaf) {
		if !slices.Contains(rules.Targets, spec.Name) {
			continue
		}
		assert.NotEmptyf(t, spec.Help, "<%s> of %s states no forms", spec.Name, leaf.CommandPath())
		if cli.IsMutating(leaf) {
			assert.Truef(t, spec.Required,
				"%s changes state, so its <%s> must be required: an optional one acts on the caller's own object", leaf.CommandPath(), spec.Name)
		}
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
	// Find leaves the flags and the arguments in rest. Parse them on a new
	// set that holds the leaf's own flags and the flags it inherits.
	flags := pflag.NewFlagSet(path, pflag.ContinueOnError)
	flags.AddFlagSet(leaf.Flags())
	flags.AddFlagSet(leaf.InheritedFlags())
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
