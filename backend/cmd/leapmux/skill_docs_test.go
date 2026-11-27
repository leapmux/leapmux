package main

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/clitest"
	"github.com/leapmux/leapmux/internal/skills"
)

var (
	skillInlineMention = regexp.MustCompile(`(?m)(^|[([ \t"'` + "`" + `])(?:leapmux|lmx)[ \t]+`)
	skillCommandWord   = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	shellEnvAssign     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
)

func normalizeNewlines(s string) string {
	return strings.ReplaceAll(s, "\r\n", "\n")
}

// TestBundledSkills_CommandsAndFlagsExistInTree verifies that every `leapmux ...`
// command path AND every flag documented inside bundled SKILL.md and
// references/*.md files resolves cleanly in the live root Cobra command tree.
func TestBundledSkills_CommandsAndFlagsExistInTree(t *testing.T) {
	root := newRootCommand(testCLIRunners(t))

	for _, s := range skills.Catalog() {
		for _, f := range s.Files {
			if !strings.HasSuffix(f.RelPath, ".md") {
				continue
			}
			body := normalizeNewlines(f.Content)
			if f.RelPath == "SKILL.md" {
				if _, after, ok := strings.Cut(body, "\n---\n"); ok {
					body = after
				}
			}
			source := s.Name + "/" + f.RelPath

			// 1. Validate every inline or fenced `leapmux`/`lmx` command mention.
			inlineCmds := extractInlineLeapmuxCommands(root, body)
			if f.RelPath == "SKILL.md" {
				require.NotEmpty(t, inlineCmds, "skill %s should demonstrate leapmux commands", s.Name)
			}
			for _, parts := range inlineCmds {
				assertCommandResolves(t, root, source, parts)
			}

			// 2. Shell-tokenize every fenced sh/bash/zsh block and validate every
			// command AND flag on each `leapmux`/`lmx` invocation.
			blocks, err := extractShellCodeBlocks(body)
			require.NoError(t, err, "%s has malformed code fence", source)
			for _, snippet := range blocks {
				invocations, err := extractLeapmuxInvocations(snippet)
				require.NoError(t, err, "%s has malformed shell snippet:\n%s", source, snippet)
				for _, tokens := range invocations {
					assertInvocationCommandsAndFlags(t, root, source, tokens)
				}
			}

			// 3. Shell-tokenize every inline backtick span that mentions
			// `leapmux`/`lmx` and validate every command AND flag on it.
			for _, span := range extractInlineBacktickSpans(body) {
				if !skillInlineMention.MatchString(span) {
					continue
				}
				invocations, err := extractLeapmuxInvocations(span)
				require.NoError(t, err, "%s has malformed inline code span: %q", source, span)
				for _, tokens := range invocations {
					assertInvocationCommandsAndFlags(t, root, source, tokens)
				}
			}
		}
	}
}

func resolveCommand(root *cobra.Command, source string, parts []string) (*cobra.Command, error) {
	cmd, rest, err := root.Find(parts)
	if err != nil {
		return nil, fmt.Errorf("%s refers to unknown command %q: %w", source, strings.Join(parts, " "), err)
	}
	if cmd == nil || cmd == root {
		return nil, fmt.Errorf("%s refers to unresolved command %q (rest=%v)", source, strings.Join(parts, " "), rest)
	}
	if cmd.HasAvailableSubCommands() && len(rest) > 0 {
		return nil, fmt.Errorf("%s refers to unknown subcommand %q under %q", source, rest[0], cmd.CommandPath())
	}
	return cmd, nil
}

func assertCommandResolves(t *testing.T, root *cobra.Command, source string, parts []string) *cobra.Command {
	t.Helper()
	cmd, err := resolveCommand(root, source, parts)
	require.NoError(t, err)
	return cmd
}

func validateInvocationCommandsAndFlags(root *cobra.Command, source string, tokens []string) error {
	if len(tokens) < 2 {
		return nil
	}
	args := tokens[1:]

	// Parse leading global flags before the subcommand path.
	rootFlags := clitest.CloneCommandFlags(root, true)
	rootFlags.SetInterspersed(false)
	if err := rootFlags.Parse(args); err != nil {
		return fmt.Errorf("%s has invalid leading flags on %q (line: %s): %w", source, root.CommandPath(), strings.Join(tokens, " "), err)
	}
	if rootFlags.ArgsLenAtDash() == 0 {
		return nil
	}
	afterLeading := rootFlags.Args()

	// Resolve subcommand path using non-flag tokens after leading global flags.
	var cmdParts []string
	for _, tok := range afterLeading {
		if strings.HasPrefix(tok, "-") || strings.HasPrefix(tok, "<") || strings.HasPrefix(tok, "$") {
			break
		}
		cmdParts = append(cmdParts, tok)
	}
	if len(cmdParts) == 0 {
		return nil
	}

	cmd, err := resolveCommand(root, source, cmdParts)
	if err != nil {
		return err
	}

	cmdFlags := clitest.CloneCommandFlags(cmd, true)
	var leadingErr error
	rootFlags.Visit(func(f *pflag.Flag) {
		if leadingErr != nil {
			return
		}
		if cmdFlags.Lookup(f.Name) == nil {
			leadingErr = fmt.Errorf("%s uses disallowed flag --%s on %q (line: %s)", source, f.Name, cmd.CommandPath(), strings.Join(tokens, " "))
		}
	})
	if leadingErr != nil {
		return leadingErr
	}

	if err := cmdFlags.Parse(afterLeading); err != nil {
		return fmt.Errorf("%s has invalid flags on %q (line: %s): %w", source, cmd.CommandPath(), strings.Join(tokens, " "), err)
	}
	return nil
}

func assertInvocationCommandsAndFlags(t *testing.T, root *cobra.Command, source string, tokens []string) {
	t.Helper()
	require.NoError(t, validateInvocationCommandsAndFlags(root, source, tokens))
}

func extractInlineLeapmuxCommands(root *cobra.Command, md string) [][]string {
	norm := normalizeNewlines(md)
	var out [][]string
	for _, loc := range skillInlineMention.FindAllStringSubmatchIndex(norm, -1) {
		prefix := norm[loc[2]:loc[3]]
		rest := norm[loc[1]:]
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			rest = rest[:nl]
		}
		switch prefix {
		case "`":
			if end := strings.IndexByte(rest, '`'); end >= 0 {
				rest = rest[:end]
			}
		case `"`:
			if end := strings.IndexByte(rest, '"'); end >= 0 {
				rest = rest[:end]
			}
		case `'`:
			if end := strings.IndexByte(rest, '\''); end >= 0 {
				rest = rest[:end]
			}
		default:
			if end := strings.IndexAny(rest, "`)]|,;&"); end >= 0 {
				rest = rest[:end]
			}
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		// Skip leading root persistent flags such as --json or --hub https://...
		rootFlags := clitest.CloneCommandFlags(root, true)
		rootFlags.SetInterspersed(false)
		if err := rootFlags.Parse(fields); err != nil || rootFlags.ArgsLenAtDash() == 0 {
			continue
		}
		var parts []string
		for _, field := range rootFlags.Args() {
			w := strings.TrimRight(field, ".,:")
			if !skillCommandWord.MatchString(w) {
				break
			}
			parts = append(parts, w)
			if strings.HasSuffix(field, ".") || strings.HasSuffix(field, ",") {
				break
			}
		}
		if len(parts) > 0 {
			out = append(out, parts)
		}
	}
	return out
}

func parseFenceOpen(line string) (ch byte, count int, info string, ok bool) {
	trimmed := strings.TrimLeft(line, " \t")
	if len(trimmed) < 3 || (trimmed[0] != '`' && trimmed[0] != '~') {
		return 0, 0, "", false
	}
	ch = trimmed[0]
	for count < len(trimmed) && trimmed[count] == ch {
		count++
	}
	if count < 3 {
		return 0, 0, "", false
	}
	info = strings.TrimSpace(trimmed[count:])
	// CommonMark forbids backticks in backtick fence info strings (that is an inline code span).
	if ch == '`' && strings.ContainsRune(info, '`') {
		return 0, 0, "", false
	}
	return ch, count, info, true
}

func isFenceClose(line string, ch byte, minLen int) bool {
	trimmed := strings.TrimLeft(line, " \t")
	count := 0
	for count < len(trimmed) && trimmed[count] == ch {
		count++
	}
	if count < minLen {
		return false
	}
	return strings.TrimSpace(trimmed[count:]) == ""
}

// extractInlineBacktickSpans strips fenced code blocks and returns all
// single-line CommonMark inline code spans matched by equal-length backtick
// delimiters.
func extractInlineBacktickSpans(md string) []string {
	norm := normalizeNewlines(md)
	var spans []string
	inFence := false
	var fenceChar byte
	fenceLen := 0

	for _, line := range strings.Split(norm, "\n") {
		if !inFence {
			if ch, count, _, ok := parseFenceOpen(line); ok {
				inFence = true
				fenceChar = ch
				fenceLen = count
				continue
			}
		} else {
			if isFenceClose(line, fenceChar, fenceLen) {
				inFence = false
			}
			continue
		}

		for i := 0; i < len(line); i++ {
			if line[i] != '`' {
				continue
			}
			j := i
			for j < len(line) && line[j] == '`' {
				j++
			}
			openLen := j - i

			matched := false
			for k := j; k < len(line); {
				if line[k] != '`' {
					k++
					continue
				}
				end := k
				for end < len(line) && line[end] == '`' {
					end++
				}
				if end-k == openLen {
					content := line[j:k]
					if len(content) >= 2 && content[0] == ' ' && content[len(content)-1] == ' ' && strings.Trim(content, " ") != "" {
						content = content[1 : len(content)-1]
					}
					spans = append(spans, content)
					i = end - 1
					matched = true
					break
				}
				k = end
			}
			if !matched {
				// Unmatched backtick run is literal text per CommonMark; skip it whole.
				i = j - 1
			}
		}
	}
	return spans
}

func extractShellCodeBlocks(md string) ([]string, error) {
	norm := normalizeNewlines(md)
	var blocks []string
	var cur []string
	inFence := false
	var fenceChar byte
	fenceLen := 0
	isShell := false

	for _, line := range strings.Split(norm, "\n") {
		if !inFence {
			ch, count, info, ok := parseFenceOpen(line)
			if !ok {
				continue
			}
			inFence = true
			fenceChar = ch
			fenceLen = count
			lang := ""
			if fields := strings.Fields(info); len(fields) > 0 {
				lang = strings.ToLower(fields[0])
			}
			isShell = lang == "sh" || lang == "bash" || lang == "zsh"
			cur = nil
			continue
		}
		if isFenceClose(line, fenceChar, fenceLen) {
			if isShell {
				blocks = append(blocks, strings.Join(cur, "\n"))
			}
			inFence = false
			isShell = false
			cur = nil
			continue
		}
		if isShell {
			cur = append(cur, line)
		}
	}
	if inFence {
		return nil, fmt.Errorf("unclosed %c code fence", fenceChar)
	}
	return blocks, nil
}

// findClosingCmdSubst returns the index of the ')' matching the "$(" at s[start:start+2].
func findClosingCmdSubst(s string, start int) (int, error) {
	depth := 1
	var quote byte
	for i := start + 2; i < len(s); i++ {
		ch := s[i]
		if quote == '\'' {
			if ch == '\'' {
				quote = 0
			}
			continue
		}
		if quote == '"' {
			if ch == '\\' && i+1 < len(s) {
				i++
				continue
			}
			if ch == '"' {
				quote = 0
				continue
			}
			if ch == '$' && i+1 < len(s) && s[i+1] == '(' {
				end, err := findClosingCmdSubst(s, i)
				if err != nil {
					return -1, err
				}
				i = end
			}
			continue
		}
		switch ch {
		case '\\':
			if i+1 < len(s) {
				i++
			}
		case '\'', '"':
			quote = ch
		case '$':
			if i+1 < len(s) && s[i+1] == '(' {
				depth++
				i++
			}
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i, nil
			}
		}
	}
	return -1, fmt.Errorf("unclosed command substitution in %q", s)
}

func stripInlineShellComment(line string) string {
	var quote byte
	for i := 0; i < len(line); i++ {
		ch := line[i]
		if quote == '\'' {
			if ch == '\'' {
				quote = 0
			}
			continue
		}
		if quote == '"' {
			if ch == '\\' && i+1 < len(line) {
				i++
				continue
			}
			if ch == '"' {
				quote = 0
			}
			continue
		}
		if ch == '\\' && i+1 < len(line) {
			i++
			continue
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			continue
		}
		if ch == '$' && i+1 < len(line) && line[i+1] == '(' {
			if end, err := findClosingCmdSubst(line, i); err == nil {
				i = end
			}
			continue
		}
		if ch == '#' {
			if i == 0 || line[i-1] == ' ' || line[i-1] == '\t' || line[i-1] == ';' || line[i-1] == '|' || line[i-1] == '&' {
				return line[:i]
			}
		}
	}
	return line
}

func splitShellSegments(line string) ([]string, error) {
	var segments []string
	var quote byte
	segStart := 0
	for i := 0; i < len(line); i++ {
		ch := line[i]
		if quote == '\'' {
			if ch == '\'' {
				quote = 0
			}
			continue
		}
		if quote == '"' {
			if ch == '\\' && i+1 < len(line) {
				i++
				continue
			}
			if ch == '"' {
				quote = 0
				continue
			}
			if ch == '$' && i+1 < len(line) && line[i+1] == '(' {
				end, err := findClosingCmdSubst(line, i)
				if err != nil {
					return nil, err
				}
				i = end
			}
			continue
		}
		if ch == '\\' && i+1 < len(line) {
			i++
			continue
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			continue
		}
		if ch == '$' && i+1 < len(line) && line[i+1] == '(' {
			end, err := findClosingCmdSubst(line, i)
			if err != nil {
				return nil, err
			}
			i = end
			continue
		}
		switch ch {
		case ';':
			segments = append(segments, line[segStart:i])
			segStart = i + 1
		case '|':
			segments = append(segments, line[segStart:i])
			if i+1 < len(line) && line[i+1] == '|' {
				i++
			}
			segStart = i + 1
		case '&':
			if i+1 < len(line) && line[i+1] == '&' {
				segments = append(segments, line[segStart:i])
				i++
				segStart = i + 1
			}
		}
	}
	segments = append(segments, line[segStart:])
	return segments, nil
}

// extractCommandSubstitutions returns the inner body of every top-level $(...)
// appearing outside single quotes in seg.
func extractCommandSubstitutions(seg string) ([]string, error) {
	var out []string
	var quote byte
	for i := 0; i < len(seg); i++ {
		ch := seg[i]
		if quote == '\'' {
			if ch == '\'' {
				quote = 0
			}
			continue
		}
		if quote == '"' {
			if ch == '\\' && i+1 < len(seg) {
				i++
				continue
			}
			if ch == '"' {
				quote = 0
				continue
			}
			if ch == '$' && i+1 < len(seg) && seg[i+1] == '(' {
				end, err := findClosingCmdSubst(seg, i)
				if err != nil {
					return nil, err
				}
				out = append(out, seg[i+2:end])
				i = end
			}
			continue
		}
		if ch == '\\' && i+1 < len(seg) {
			i++
			continue
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			continue
		}
		if ch == '$' && i+1 < len(seg) && seg[i+1] == '(' {
			end, err := findClosingCmdSubst(seg, i)
			if err != nil {
				return nil, err
			}
			out = append(out, seg[i+2:end])
			i = end
		}
	}
	return out, nil
}

func extractLeapmuxInvocations(snippet string) ([][]string, error) {
	norm := normalizeNewlines(snippet)
	// Join backslash line continuations.
	joined := strings.ReplaceAll(norm, "\\\n", " ")
	var out [][]string
	for _, rawLine := range strings.Split(joined, "\n") {
		line := strings.TrimSpace(stripInlineShellComment(rawLine))
		if line == "" {
			continue
		}
		segments, err := splitShellSegments(line)
		if err != nil {
			return nil, err
		}
		for _, rawSeg := range segments {
			seg := strings.TrimSpace(rawSeg)
			if seg == "" {
				continue
			}
			substs, err := extractCommandSubstitutions(seg)
			if err != nil {
				return nil, err
			}
			for _, inner := range substs {
				nested, err := extractLeapmuxInvocations(inner)
				if err != nil {
					return nil, err
				}
				out = append(out, nested...)
			}
			toks, err := splitShellWords(seg)
			if err != nil {
				return nil, err
			}
			for len(toks) > 0 && shellEnvAssign.MatchString(toks[0]) {
				toks = toks[1:]
			}
			if len(toks) >= 2 && (toks[0] == "leapmux" || toks[0] == "lmx") {
				out = append(out, toks)
			}
		}
	}
	return out, nil
}

func splitShellWords(s string) ([]string, error) {
	var words []string
	var cur strings.Builder
	var quote byte
	inWord := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if quote == '\'' {
			if ch == '\'' {
				quote = 0
				continue
			}
			cur.WriteByte(ch)
			continue
		}
		if quote == '"' {
			if ch == '"' {
				quote = 0
				continue
			}
			if ch == '\\' && i+1 < len(s) {
				next := s[i+1]
				if next == '"' || next == '\\' || next == '$' || next == '`' {
					cur.WriteByte(next)
					i++
					continue
				}
			}
			if ch == '$' && i+1 < len(s) && s[i+1] == '(' {
				end, err := findClosingCmdSubst(s, i)
				if err != nil {
					return nil, err
				}
				cur.WriteString(s[i : end+1])
				i = end
				continue
			}
			cur.WriteByte(ch)
			continue
		}
		switch ch {
		case '\\':
			if i+1 >= len(s) {
				return nil, fmt.Errorf("trailing unescaped backslash in %q", s)
			}
			i++
			cur.WriteByte(s[i])
			inWord = true
		case '\'', '"':
			quote = ch
			inWord = true
		case '$':
			if i+1 < len(s) && s[i+1] == '(' {
				end, err := findClosingCmdSubst(s, i)
				if err != nil {
					return nil, err
				}
				cur.WriteString(s[i : end+1])
				i = end
				inWord = true
				continue
			}
			cur.WriteByte(ch)
			inWord = true
		case ' ', '\t':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteByte(ch)
			inWord = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unclosed %c quote in %q", quote, s)
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words, nil
}

func TestSkillDocHelpers_ShellExtractionAndFlagValidation(t *testing.T) {
	t.Run("extractShellCodeBlocks handles CRLF, outer non-shell fences, zsh, shader, and unclosed fence", func(t *testing.T) {
		md := strings.Join([]string{
			"````markdown",
			"```sh",
			"leapmux fake-nested-ignored",
			"```",
			"````",
			"```shader",
			"leapmux fake-shader-ignored",
			"```",
			"```sh",
			"leapmux skill list",
			"```",
			"```zsh",
			"lmx worker list",
			"```",
		}, "\r\n")
		blocks, err := extractShellCodeBlocks(md)
		require.NoError(t, err)
		assert.Equal(t, []string{"leapmux skill list", "lmx worker list"}, blocks)

		_, err = extractShellCodeBlocks("```sh\nleapmux skill list\n")
		require.Error(t, err)
	})

	t.Run("extractLeapmuxInvocations and splitShellWords handle pipelines, comments, env vars, substitutions, escapes, and errors", func(t *testing.T) {
		snippet := strings.Join([]string{
			"FOO=1 BAR=2 leapmux worker list --json | jq . # comment",
			`A1="$(leapmux agent open --provider codex)" && lmx skill status`,
			`leapmux agent send "$A1" $(cat prompt.txt); leapmux agent send id hello\ world`,
		}, "\r\n")
		got, err := extractLeapmuxInvocations(snippet)
		require.NoError(t, err)
		assert.Equal(t, [][]string{
			{"leapmux", "worker", "list", "--json"},
			{"leapmux", "agent", "open", "--provider", "codex"},
			{"lmx", "skill", "status"},
			{"leapmux", "agent", "send", "$A1", "$(cat prompt.txt)"},
			{"leapmux", "agent", "send", "id", "hello world"},
		}, got)

		_, err = splitShellWords(`leapmux skill show "unclosed`)
		require.Error(t, err)
		_, err = splitShellWords(`leapmux skill show 'unclosed`)
		require.Error(t, err)
	})

	t.Run("extractInlineLeapmuxCommands handles lmx, quotes, 3+ subcommands, and leading global flags", func(t *testing.T) {
		root := newRootCommand(testCLIRunners(t))
		md := strings.Join([]string{
			`Run "leapmux --json worker list" or 'lmx git branch delete foo'.`,
			"Or use `leapmux --hub https://hub.example tab status clear`.",
		}, "\r\n")
		got := extractInlineLeapmuxCommands(root, md)
		assert.Equal(t, [][]string{
			{"worker", "list"},
			{"git", "branch", "delete", "foo"},
			{"tab", "status", "clear"},
		}, got)
	})

	t.Run("validateInvocationCommandsAndFlags handles leading global flags, NoOptDefVal, short attached values, disallowed flags, and unknown subcommands", func(t *testing.T) {
		root := newRootCommand(testCLIRunners(t))
		probe := cli.NewLeaf(cli.Leaf{
			Use:   "probe",
			Short: "Probe flag parser",
			Flags: func(fs *pflag.FlagSet) {
				fs.IntP("limit", "n", 0, "limit")
				fs.StringP("timeout", "t", "", "timeout")
				f := fs.String("optval", "", "optional value flag")
				_ = f
				fs.Lookup("optval").NoOptDefVal = "default"
			},
			Run: func(*cli.Invocation) (any, error) { return nil, nil },
		})
		root.AddCommand(probe)

		assert.NoError(t, validateInvocationCommandsAndFlags(root, "test", []string{"leapmux", "--json", "worker", "list"}))
		assert.NoError(t, validateInvocationCommandsAndFlags(root, "test", []string{"leapmux", "probe", "-n5", "-t=15m", "--optval", "-q"}))
		assert.NoError(t, validateInvocationCommandsAndFlags(root, "test", []string{"leapmux", "agent", "messages", "id", "--limit", "<n>"}))
		assert.NoError(t, validateInvocationCommandsAndFlags(root, "test", []string{"leapmux", "agent", "messages", "id", "--limit", "$LIMIT"}))
		assert.NoError(t, validateInvocationCommandsAndFlags(root, "test", []string{"leapmux", "agent", "wait", "id", "--timeout", "<duration>"}))
		assert.Error(t, validateInvocationCommandsAndFlags(root, "test", []string{"leapmux", "--hub", "https://hub.example", "skill", "show", "leapmux"}))
		assert.Error(t, validateInvocationCommandsAndFlags(root, "test", []string{"leapmux", "skill", "show", "leapmux", "--hub", "https://hub.example"}))
		assert.Error(t, validateInvocationCommandsAndFlags(root, "test", []string{"leapmux", "git", "branch", "nonexistent"}))
		assert.Error(t, validateInvocationCommandsAndFlags(root, "test", []string{"leapmux", "agent", "messages", "id", "--limit"}))
		assert.Error(t, validateInvocationCommandsAndFlags(root, "test", []string{"leapmux", "agent", "messages", "id", "--limit", "abc"}))
		assert.Error(t, validateInvocationCommandsAndFlags(root, "test", []string{"leapmux", "agent", "wait", "id", "--timeout", "not-a-duration"}))
		assert.Error(t, validateInvocationCommandsAndFlags(root, "test", []string{"leapmux", "worker", "list", "--json=nope"}))
		assert.Error(t, validateInvocationCommandsAndFlags(root, "test", []string{"leapmux", "--hub"}))
		assert.Error(t, validateInvocationCommandsAndFlags(root, "test", []string{"leapmux", "--bogus", "worker", "list"}))
	})

	t.Run("validateInvocationCommandsAndFlags covers root-only commands and rejects disallowed or unknown flags on them", func(t *testing.T) {
		root := newRootCommand(testCLIRunners(t))

		valid := [][]string{
			{"leapmux", "commands", "--compact", "--json"},
			{"leapmux", "commands", "agent", "--depth", "1"},
			{"leapmux", "completion", "zsh"},
			{"leapmux", "daemon", "solo", "status"},
			{"leapmux", "up", "--help"},
			{"leapmux", "down", "--help"},
			{"leapmux", "up", "-h"},
			{"leapmux", "recover", "db"},
		}
		for _, tokens := range valid {
			assert.NoError(t, validateInvocationCommandsAndFlags(root, "test", tokens), "expected valid: %v", tokens)
		}

		invalid := [][]string{
			{"leapmux", "commands", "--hub", "https://hub.example"},
			{"leapmux", "--hub", "https://hub.example", "commands"},
			{"leapmux", "commands", "--bogus"},
			{"leapmux", "daemon", "solo", "status", "--hub", "https://hub.example"},
			{"leapmux", "--hub", "https://hub.example", "daemon", "solo", "status"},
			{"leapmux", "daemon", "solo", "status", "--bogus"},
		}
		for _, tokens := range invalid {
			assert.Error(t, validateInvocationCommandsAndFlags(root, "test", tokens), "expected invalid: %v", tokens)
		}
	})

	t.Run("extractInlineBacktickSpans and inline backtick flag validation", func(t *testing.T) {
		root := newRootCommand(testCLIRunners(t))

		md := strings.Join([]string{
			"```sh",
			"leapmux ignored-in-fence --bogus",
			"```",
			"~~~sh",
			"leapmux ignored-in-tilde-fence --bogus",
			"~~~",
			"Unmatched `` literal and `leapmux agent send \"$A1\" \"...\" --steer` plus `leapmux git diff --dir <path> --base main`.",
			"Double-tick ``leapmux skill show leapmux-review --reference review-schemas`` and `` `leapmux up --help` ``.",
		}, "\r\n")
		spans := extractInlineBacktickSpans(md)
		assert.Equal(t, []string{
			`leapmux agent send "$A1" "..." --steer`,
			`leapmux git diff --dir <path> --base main`,
			`leapmux skill show leapmux-review --reference review-schemas`,
			"`leapmux up --help`",
		}, spans)

		for _, span := range spans[:3] {
			require.True(t, skillInlineMention.MatchString(span), "span=%q", span)
			invocations, err := extractLeapmuxInvocations(span)
			require.NoError(t, err, "span=%q", span)
			require.NotEmpty(t, invocations, "span=%q", span)
			for _, tokens := range invocations {
				assert.NoError(t, validateInvocationCommandsAndFlags(root, "inline", tokens), "span=%q", span)
			}
		}

		// Valid inline help flag.
		helpSpans := extractInlineBacktickSpans("See `leapmux up --help` for flags.")
		require.Equal(t, []string{"leapmux up --help"}, helpSpans)
		helpInvs, err := extractLeapmuxInvocations(helpSpans[0])
		require.NoError(t, err)
		require.Len(t, helpInvs, 1)
		assert.NoError(t, validateInvocationCommandsAndFlags(root, "inline", helpInvs[0]))

		// Bogus and disallowed flags in inline backticks must fail validation.
		for _, badSpan := range []string{
			`leapmux agent send "$A1" --bogus`,
			`leapmux skill show leapmux --hub https://hub.example`,
		} {
			extracted := extractInlineBacktickSpans("Run `" + badSpan + "`.")
			require.Equal(t, []string{badSpan}, extracted)
			require.True(t, skillInlineMention.MatchString(extracted[0]))
			invs, err := extractLeapmuxInvocations(extracted[0])
			require.NoError(t, err)
			require.Len(t, invs, 1)
			assert.Error(t, validateInvocationCommandsAndFlags(root, "inline", invs[0]), "span=%q", badSpan)
		}
	})
}
