package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v4"

	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/skills"
)

func TestSkillCLI_LocalLifecycleAndDriftGuard(t *testing.T) {
	targetDir := filepath.Join(t.TempDir(), "custom-skills")
	targetFlag := "dir:" + targetDir

	// List shows all 6 bundled skills as not_installed.
	o := runControl(t, "skill", "list", "--target", targetFlag)
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	var listEnv struct {
		Data skillListResult `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(o.Stdout), &listEnv))
	require.Len(t, listEnv.Data.Skills, len(skills.Catalog()))
	assert.Equal(t, "not_installed", listEnv.Data.Skills[0].State)

	// Show prints the raw SKILL.md document.
	o = runControlText(t, "skill", "show", "leapmux-review")
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	assert.Contains(t, o.Stdout, "name: leapmux-review")
	assert.Contains(t, o.Stdout, "--output-schema")

	// Show --references lists bundled companion references.
	o = runControlText(t, "skill", "show", "leapmux-review", "--references")
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	assert.Equal(t, "review-schemas\n", o.Stdout)

	// Show --reference prints the companion Markdown document.
	o = runControlText(t, "skill", "show", "leapmux-review", "--reference", "review-schemas")
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	assert.Contains(t, o.Stdout, "Code Diff Review Schema")

	// Unknown reference returns CodeNotFound with hint.
	o = runControl(t, "skill", "show", "leapmux-review", "--reference", "missing-ref")
	assert.Equal(t, cli.ExitNotFound, o.Code)
	assert.Equal(t, cli.CodeNotFound, o.Failure(t).Code)
	assert.Contains(t, o.Stdout, "review-schemas")

	// Mutual exclusion of --references and --reference.
	o = runControl(t, "skill", "show", "leapmux-review", "--references", "--reference", "review-schemas")
	assert.Equal(t, cli.ExitUsage, o.Code)

	// Install all skills via the plural alias `skills`.
	o = runControl(t, "skills", "install", "--all", "--target", targetFlag)
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	for _, s := range skills.Catalog() {
		assert.FileExists(t, filepath.Join(targetDir, s.Name, "SKILL.md"))
		assert.FileExists(t, filepath.Join(targetDir, s.Name, "agents", "openai.yaml"))
		for _, ref := range s.References {
			assert.FileExists(t, filepath.Join(targetDir, s.Name, "references", ref+".md"))
		}
	}

	// Status reports up_to_date.
	o = runControl(t, "skill", "status", "--target", targetFlag)
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	require.NoError(t, json.Unmarshal([]byte(o.Stdout), &listEnv))
	for _, s := range listEnv.Data.Skills {
		assert.Equal(t, "up_to_date", s.State)
	}

	// User edit triggers modified state and blocks overwrite without --force.
	skillFile := filepath.Join(targetDir, "leapmux", "SKILL.md")
	require.NoError(t, os.WriteFile(skillFile, []byte("# edited by user\n"), 0o644))
	o = runControl(t, "skill", "install", "leapmux", "--target", targetFlag)
	assert.Equal(t, cli.ExitFailure, o.Code)
	assert.Equal(t, cli.CodeConflict, o.Failure(t).Code)
	assert.Contains(t, o.Failure(t).Message, "local edits")

	// --force restores the bundled skill.
	o = runControl(t, "skill", "install", "leapmux", "--target", targetFlag, "--force")
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)

	// Uninstall removes cleanly.
	o = runControl(t, "skill", "uninstall", "leapmux", "--target", targetFlag)
	require.Equal(t, cli.ExitOK, o.Code, o.Stderr)
	_, statErr := os.Stat(skillFile)
	assert.ErrorIs(t, statErr, os.ErrNotExist)
}

var (
	skillLeapmuxInvocation = regexp.MustCompile(`(?m)(?:^|[([ \t` + "`" + `])leapmux[ \t]+([a-z][a-z0-9-]*(?:[ \t]+[a-z][a-z0-9-]*)?)`)
	skillRelativeRefLink   = regexp.MustCompile(`references/[a-zA-Z0-9._-]+\.md`)
)

type openAISkillYAML struct {
	Interface struct {
		DisplayName      string `yaml:"display_name"`
		ShortDescription string `yaml:"short_description"`
		DefaultPrompt    string `yaml:"default_prompt"`
	} `yaml:"interface"`
}

// TestBundledSkills_ContractAndMetadata verifies frontmatter discipline,
// SKILL.md line budgets, Codex agents/openai.yaml metadata, and relative link
// integrity across every bundled skill.
func TestBundledSkills_ContractAndMetadata(t *testing.T) {
	const maxSkillLines = 150

	for _, s := range skills.Catalog() {
		lines := strings.Split(strings.TrimSuffix(s.Content, "\n"), "\n")
		assert.LessOrEqual(t, len(lines), maxSkillLines, "skill %s SKILL.md exceeds %d-line budget (%d lines)", s.Name, maxSkillLines, len(lines))

		if s.Name != "leapmux" {
			assert.True(t, s.UserInvocable, "workflow/help skill %s must set user-invocable: true", s.Name)
			assert.NotEmpty(t, s.ArgumentHint, "workflow/help skill %s must set argument-hint", s.Name)
		}

		// Every bundled skill ships agents/openai.yaml with complete interface metadata.
		var openAIFile *skills.SkillFile
		fileSet := make(map[string]bool, len(s.Files))
		for i := range s.Files {
			f := &s.Files[i]
			fileSet[f.RelPath] = true
			if f.RelPath == "agents/openai.yaml" {
				openAIFile = f
			}
		}
		require.NotNil(t, openAIFile, "skill %s must include agents/openai.yaml", s.Name)
		var meta openAISkillYAML
		require.NoError(t, yaml.Unmarshal([]byte(openAIFile.Content), &meta), "parse %s/agents/openai.yaml", s.Name)
		assert.NotEmpty(t, meta.Interface.DisplayName, "%s/agents/openai.yaml missing display_name", s.Name)
		assert.NotEmpty(t, meta.Interface.ShortDescription, "%s/agents/openai.yaml missing short_description", s.Name)
		assert.NotEmpty(t, meta.Interface.DefaultPrompt, "%s/agents/openai.yaml missing default_prompt", s.Name)

		// Every references/*.md pointer in SKILL.md must exist in the bundle.
		for _, match := range skillRelativeRefLink.FindAllString(s.Content, -1) {
			assert.True(t, fileSet[match], "skill %s references missing file %q", s.Name, match)
		}
	}
}

// TestBundledSkills_CommandsAndFlagsExistInTree verifies that every `leapmux ...`
// command path AND every flag documented inside bundled SKILL.md and
// references/*.md files resolves cleanly in the live Cobra command tree.
func TestBundledSkills_CommandsAndFlagsExistInTree(t *testing.T) {
	root := NewCommand()
	// Attach `commands` and `completion` placeholders registered in cmd/leapmux.
	commandsCmd := cli.Group("commands", "List commands")
	cli.AddOutputFlags(commandsCmd)
	root.AddCommand(commandsCmd)
	root.AddCommand(cli.Group("completion", "Print shell completions"))

	for _, s := range skills.Catalog() {
		for _, f := range s.Files {
			if !strings.HasSuffix(f.RelPath, ".md") {
				continue
			}
			body := f.Content
			if f.RelPath == "SKILL.md" {
				if _, after, ok := strings.Cut(body, "\n---\n"); ok {
					body = after
				}
			}
			// 1. Validate every inline or fenced `leapmux <subcommand>` mention.
			matches := skillLeapmuxInvocation.FindAllStringSubmatch(body, -1)
			if f.RelPath == "SKILL.md" {
				require.NotEmpty(t, matches, "skill %s should demonstrate leapmux commands", s.Name)
			}
			for _, m := range matches {
				parts := strings.Fields(m[1])
				if len(parts) == 0 {
					continue
				}
				assertCommandResolves(t, root, s.Name+"/"+f.RelPath, parts)
			}

			// 2. Shell-tokenize every fenced sh/bash block and validate every
			// command AND flag on each `leapmux ...` invocation.
			for _, snippet := range extractShellCodeBlocks(body) {
				for _, tokens := range extractLeapmuxInvocations(snippet) {
					assertInvocationCommandsAndFlags(t, root, s.Name+"/"+f.RelPath, tokens)
				}
			}
		}
	}
}

func assertCommandResolves(t *testing.T, root *cobra.Command, source string, parts []string) {
	t.Helper()
	cmd, rest, err := root.Find(parts)
	require.NoError(t, err, "%s references unknown command %q", source, strings.Join(parts, " "))
	require.NotNil(t, cmd)
	if cmd == root {
		t.Fatalf("%s references unresolved command %q (rest=%v)", source, strings.Join(parts, " "), rest)
	}
	if cmd.HasAvailableSubCommands() && len(rest) > 0 {
		t.Fatalf("%s references unknown subcommand %q under %q", source, rest[0], cmd.CommandPath())
	}
}

func assertInvocationCommandsAndFlags(t *testing.T, root *cobra.Command, source string, tokens []string) {
	t.Helper()
	if len(tokens) < 2 {
		return
	}
	args := tokens[1:]
	// Resolve subcommand path using non-flag leading tokens.
	var cmdParts []string
	var flagStart int
	for i, tok := range args {
		if strings.HasPrefix(tok, "-") || strings.HasPrefix(tok, "<") || strings.HasPrefix(tok, "$") {
			flagStart = i
			break
		}
		cmdParts = append(cmdParts, tok)
		flagStart = i + 1
	}
	if len(cmdParts) == 0 {
		return
	}
	cmd, _, err := root.Find(cmdParts)
	require.NoError(t, err, "%s references unknown command %q", source, strings.Join(tokens, " "))
	require.NotNil(t, cmd)
	require.NotEqual(t, root, cmd, "%s references unresolved command %q", source, strings.Join(tokens, " "))

	allFlags := cmd.Flags()
	inherited := cmd.InheritedFlags()
	for i := flagStart; i < len(args); i++ {
		tok := args[i]
		if tok == "--" {
			break
		}
		if tok == "-" || !strings.HasPrefix(tok, "-") {
			continue
		}
		if strings.HasPrefix(tok, "--") {
			name := strings.TrimPrefix(tok, "--")
			if eq := strings.IndexByte(name, '='); eq >= 0 {
				name = name[:eq]
			}
			f := allFlags.Lookup(name)
			if f == nil {
				f = inherited.Lookup(name)
			}
			require.NotNil(t, f, "%s uses unknown flag --%s on %q (line: %s)", source, name, cmd.CommandPath(), strings.Join(tokens, " "))
			if f.Value.Type() != "bool" && !strings.Contains(tok, "=") {
				i++ // skip value token
			}
			continue
		}
		// Short flag(s) such as -q.
		shorts := strings.TrimPrefix(tok, "-")
		for j := 0; j < len(shorts); j++ {
			sh := shorts[j : j+1]
			f := allFlags.ShorthandLookup(sh)
			if f == nil {
				f = inherited.ShorthandLookup(sh)
			}
			require.NotNil(t, f, "%s uses unknown short flag -%s on %q (line: %s)", source, sh, cmd.CommandPath(), strings.Join(tokens, " "))
			if f.Value.Type() != "bool" && j == len(shorts)-1 {
				i++ // skip value token
			}
		}
	}
}

func extractShellCodeBlocks(md string) []string {
	var blocks []string
	lines := strings.Split(md, "\n")
	inShell := false
	var cur []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inShell {
			if strings.HasPrefix(trimmed, "```sh") || strings.HasPrefix(trimmed, "```bash") {
				inShell = true
				cur = nil
			}
			continue
		}
		if strings.HasPrefix(trimmed, "```") {
			blocks = append(blocks, strings.Join(cur, "\n"))
			inShell = false
			continue
		}
		cur = append(cur, line)
	}
	return blocks
}

func extractLeapmuxInvocations(snippet string) [][]string {
	// Join backslash line continuations.
	joined := strings.ReplaceAll(snippet, "\\\n", " ")
	var out [][]string
	for _, rawLine := range strings.Split(joined, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Unwrap simple assignment command substitution: A1=$(leapmux ...)
		if idx := strings.Index(line, "$("); idx >= 0 && strings.HasSuffix(line, ")") {
			line = strings.TrimSpace(line[idx+2 : len(line)-1])
		}
		toks := splitShellWords(line)
		if len(toks) >= 2 && (toks[0] == "leapmux" || toks[0] == "lmx") {
			out = append(out, toks)
		}
	}
	return out
}

func splitShellWords(s string) []string {
	var words []string
	var cur strings.Builder
	var quote byte
	inWord := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if quote != 0 {
			if ch == quote {
				quote = 0
				continue
			}
			if quote == '"' && ch == '\\' && i+1 < len(s) {
				i++
				cur.WriteByte(s[i])
				continue
			}
			cur.WriteByte(ch)
			continue
		}
		switch ch {
		case '\'', '"':
			quote = ch
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
	if inWord {
		words = append(words, cur.String())
	}
	return words
}
