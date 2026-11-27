package cli

import (
	"fmt"
	"maps"
	"runtime"
	"slices"
	"strings"
	"sync"
	"weak"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// defaultAbbreviationNote is the last line of group help unless the root
// command overrides it with SetAbbreviationNote.
const defaultAbbreviationNote = "Commands accept unique prefixes and short aliases."

type commandMeta struct {
	examples         []Example
	args             map[string]string
	next             []string
	footer           string
	commandGroup     string
	disallowedFlags  map[string]bool
	mutates          bool
	destructive      bool
	quietUnsupported bool
	localOnly        bool
	abbreviationNote string
	stateClassifier  StateClassifier
}

var (
	commandMetaMu    sync.RWMutex
	commandMetaStore = map[weak.Pointer[cobra.Command]]*commandMeta{}
)

func editCommandMeta(cmd *cobra.Command, fn func(*commandMeta)) {
	if cmd == nil {
		return
	}
	wp := weak.Make(cmd)
	commandMetaMu.Lock()
	defer commandMetaMu.Unlock()
	m, ok := commandMetaStore[wp]
	if !ok {
		m = &commandMeta{}
		commandMetaStore[wp] = m
		runtime.AddCleanup(cmd, cleanupCommandMeta, wp)
	}
	fn(m)
}

func readCommandMeta[T any](cmd *cobra.Command, fn func(*commandMeta) T) T {
	var zero T
	if cmd == nil {
		return zero
	}
	wp := weak.Make(cmd)
	commandMetaMu.RLock()
	defer commandMetaMu.RUnlock()
	m, ok := commandMetaStore[wp]
	if !ok {
		return zero
	}
	return fn(m)
}

func cleanupCommandMeta(wp weak.Pointer[cobra.Command]) {
	commandMetaMu.Lock()
	delete(commandMetaStore, wp)
	commandMetaMu.Unlock()
}

// Example is one example in a command's help.
type Example struct {
	// Comment is one sentence that states what the example does. It is
	// optional.
	Comment string `json:"comment,omitempty"`
	// Command is the whole command line, starting with "leapmux ". Use full
	// command names: an abbreviation can become ambiguous in a later
	// release.
	Command string `json:"command"`
}

// Leaf declares one runnable command.
//
// Use is the name followed by the positional arguments, and it is the only
// declaration of them: `<name>` is required, `[name]` is optional, and a
// trailing `...` repeats the last one. The command validates the count from
// it, Invocation.Arg reads a value by its name, and the completion completes
// each position by its name.
type Leaf struct {
	Use      string
	Aliases  []string
	Short    string
	Long     string
	Examples []Example
	// Args describes positional arguments of Use, by name: one sentence
	// about the values that each accepts. An argument that Args omits takes
	// the description that DescribeArgs gives its name for the whole tree.
	Args map[string]string
	// Mutates marks a command that changes state: it creates, changes or
	// removes something. `leapmux commands` reports it, so a caller tells a
	// read from a write without running either. A command that changes or
	// removes an existing object takes the object as a REQUIRED argument,
	// never as an optional one that defaults to the caller's own object.
	Mutates bool
	// HandlesSignals marks a command that handles SIGINT and SIGTERM itself,
	// as a server in the foreground does to shut down gracefully on them.
	// The leaf then installs no handler of its own, and it returns the
	// command's error as it is, never as the interrupted exit status: an
	// error after a shutdown signal is a failure of the shutdown.
	HandlesSignals bool
	// QuietUnsupported marks a command that has no meaningful -q / --quiet
	// output (such as streams or build-info dumps). When set, passing -q
	// fails with ExitUsage before Run executes.
	QuietUnsupported bool
	// Destructive marks a command that deletes, closes, revokes, or resets
	// state. Unknown-command suggestions require edit distance <= 1 for it.
	Destructive bool
	// Next lists suggested follow-up commands rendered in leaf help.
	Next []string
	// Flags registers the command's own flags.
	Flags func(fs *pflag.FlagSet)
	// Run does the work and returns the result to print. A nil result
	// prints nothing.
	Run func(inv *Invocation) (any, error)
	// Complete completes the positional argument at position i, whose
	// declaration is spec. Nil completes nothing.
	Complete func(cmd *cobra.Command, spec ArgSpec, toComplete string) ([]string, cobra.ShellCompDirective)
	// FlagComplete registers completion functions for flags declared on this
	// leaf, keyed by flag name. NewLeaf panics if a key does not match a flag
	// declared on this leaf or collides with another registration (such as
	// SetEnum).
	FlagComplete map[string]cobra.CompletionFunc
}

// NewLeaf builds the cobra command for l.
func NewLeaf(l Leaf) *cobra.Command {
	specs := ParseArgSpecs(l.Use)
	for name := range l.Args {
		if !slices.ContainsFunc(specs, func(s ArgSpec) bool { return s.Name == name }) {
			panic(fmt.Sprintf("cli: %q describes the argument %q, which its Use line does not declare", l.Use, name))
		}
	}
	cmd := &cobra.Command{
		Use:     l.Use,
		Aliases: l.Aliases,
		Short:   l.Short,
		Long:    l.Long,
		Example: formatExamples(l.Examples),
		Args:    argsValidator(specs),
	}
	if len(l.Examples) > 0 || len(l.Args) > 0 || l.Mutates || l.Destructive || l.QuietUnsupported || len(l.Next) > 0 {
		editCommandMeta(cmd, func(m *commandMeta) {
			if len(l.Examples) > 0 {
				m.examples = slices.Clone(l.Examples)
			}
			if len(l.Args) > 0 {
				m.args = maps.Clone(l.Args)
			}
			m.mutates = l.Mutates
			m.destructive = l.Destructive
			m.quietUnsupported = l.QuietUnsupported
			if len(l.Next) > 0 {
				m.next = slices.Clone(l.Next)
			}
		})
	}
	cmd.Flags().SortFlags = false
	if l.Flags != nil {
		l.Flags(cmd.Flags())
	}
	registerEnumCompletionsForSet(cmd, cmd.Flags())
	for _, name := range slices.Sorted(maps.Keys(l.FlagComplete)) {
		fn := l.FlagComplete[name]
		if cmd.Flags().Lookup(name) == nil {
			panic(fmt.Sprintf("cli: %q registers completion for undeclared flag %q", l.Use, name))
		}
		if fn == nil {
			panic(fmt.Sprintf("cli: %q registers nil completion for flag %q", l.Use, name))
		}
		if err := cmd.RegisterFlagCompletionFunc(name, fn); err != nil {
			panic(fmt.Sprintf("cli: %q failed to register completion for flag %q: %v", l.Use, name, err))
		}
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if err := validateAllowedFlags(cmd); err != nil {
			return err
		}
		if err := validateRequiredFlags(cmd); err != nil {
			return err
		}
		printer, err := printerForValidated(cmd)
		if err != nil {
			return err
		}
		if printer.Format == FormatQuiet && l.QuietUnsupported {
			return Usagef("--quiet does not apply to %s", cmd.CommandPath()).
				WithHint("use --json, or the text form")
		}
		ctx := cmd.Context()
		if !l.HandlesSignals {
			var stop func()
			ctx, stop = WithInterrupt(ctx)
			defer stop()
		}
		inv := &Invocation{
			Context: ctx,
			Command: cmd,
			Args:    args,
			Printer: printer,
			Stdin:   cmd.InOrStdin(),
			specs:   specs,
		}
		result, err := l.Run(inv)
		if err != nil {
			// An interrupt ends every call in flight, and each reports the
			// cancellation as its own failure. The user asked for the stop,
			// so the command exits as a shell reports an interrupted one.
			if !l.HandlesSignals && Interrupted(inv.Context) {
				return &ExitStatus{Code: ExitInterrupted}
			}
			return err
		}
		return inv.Printer.Result(result)
	}
	if l.Complete != nil {
		cmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
			spec, ok := specAt(specs, len(args))
			if !ok {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			return l.Complete(cmd, spec, toComplete)
		}
	}
	return cmd
}

// Group builds a command that only holds other commands. Run alone, it
// prints its help.
func Group(use, short string, children ...*cobra.Command) *cobra.Command {
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
	}
	cmd.AddCommand(children...)
	return cmd
}

// SetAliases attaches explicit short aliases to cmd and returns cmd.
func SetAliases(cmd *cobra.Command, aliases ...string) *cobra.Command {
	cmd.Aliases = append(cmd.Aliases, aliases...)
	return cmd
}

// SetCommandGroup assigns cmd to a section in its parent's help output
// (such as "Daily Work", "Files & Git", "Local Daemon").
func SetCommandGroup(cmd *cobra.Command, group string) *cobra.Command {
	editCommandMeta(cmd, func(m *commandMeta) {
		m.commandGroup = group
	})
	return cmd
}

// CommandGroupOf returns the help section group assigned to cmd, or "".
func CommandGroupOf(cmd *cobra.Command) string {
	return readCommandMeta(cmd, func(m *commandMeta) string {
		return m.commandGroup
	})
}

// SetFooter sets the line that the help of cmd prints last.
func SetFooter(cmd *cobra.Command, footer string) {
	editCommandMeta(cmd, func(m *commandMeta) {
		m.footer = footer
	})
}

func footerOf(cmd *cobra.Command) string {
	return readCommandMeta(cmd, func(m *commandMeta) string {
		return m.footer
	})
}

// SetAbbreviationNote overrides the abbreviation note printed at the bottom of
// group help under root.
func SetAbbreviationNote(root *cobra.Command, note string) {
	editCommandMeta(root, func(m *commandMeta) {
		m.abbreviationNote = note
	})
}

func abbreviationNoteOf(cmd *cobra.Command) string {
	commandMetaMu.RLock()
	defer commandMetaMu.RUnlock()
	for c := cmd; c != nil; c = c.Parent() {
		if m := commandMetaStore[weak.Make(c)]; m != nil && m.abbreviationNote != "" {
			return m.abbreviationNote
		}
	}
	return defaultAbbreviationNote
}

// SetStateClassifier registers the StateClassifier that commands under root
// inherit for `,state` table columns.
func SetStateClassifier(root *cobra.Command, fn StateClassifier) {
	editCommandMeta(root, func(m *commandMeta) {
		m.stateClassifier = fn
	})
}

func stateClassifierOf(cmd *cobra.Command) StateClassifier {
	commandMetaMu.RLock()
	defer commandMetaMu.RUnlock()
	for c := cmd; c != nil; c = c.Parent() {
		if m := commandMetaStore[weak.Make(c)]; m != nil && m.stateClassifier != nil {
			return m.stateClassifier
		}
	}
	return nil
}

// MarkLocalOnly marks cmd (and its descendants) as acting strictly on the local
// machine, so handoff or remote dispatch skips it.
func MarkLocalOnly(cmd *cobra.Command) {
	editCommandMeta(cmd, func(m *commandMeta) {
		m.localOnly = true
	})
}

// IsLocalOnly reports whether cmd or an ancestor was marked local-only via
// MarkLocalOnly.
func IsLocalOnly(cmd *cobra.Command) bool {
	commandMetaMu.RLock()
	defer commandMetaMu.RUnlock()
	for c := cmd; c != nil; c = c.Parent() {
		if m := commandMetaStore[weak.Make(c)]; m != nil && m.localOnly {
			return true
		}
	}
	return false
}

// DisallowInheritedFlags marks the specified persistent flags as inapplicable to
// cmd and its descendants unless a descendant defines its own local flag of
// the same name. Disallowed flags are omitted from help, shell completion, and
// `commands --json`, and rejected as unknown flags at run time.
func DisallowInheritedFlags(cmd *cobra.Command, names ...string) {
	if cmd == nil || len(names) == 0 {
		return
	}
	editCommandMeta(cmd, func(m *commandMeta) {
		if m.disallowedFlags == nil {
			m.disallowedFlags = make(map[string]bool, len(names))
		}
		for _, item := range names {
			item = strings.TrimSpace(item)
			if item != "" {
				m.disallowedFlags[item] = true
			}
		}
	})
}

// IsInheritedFlagDisallowed reports whether cmd rejects the inherited persistent flag name.
func IsInheritedFlagDisallowed(cmd *cobra.Command, name string) bool {
	if cmd == nil || cmd.LocalFlags().Lookup(name) != nil {
		return false
	}
	commandMetaMu.RLock()
	defer commandMetaMu.RUnlock()
	for c := cmd; c != nil; c = c.Parent() {
		if m := commandMetaStore[weak.Make(c)]; m != nil && m.disallowedFlags[name] {
			return true
		}
	}
	return false
}

// ExamplesOf returns a copy of the examples that the leaf cmd declares.
func ExamplesOf(cmd *cobra.Command) []Example {
	return readCommandMeta(cmd, func(m *commandMeta) []Example {
		if len(m.examples) == 0 {
			return nil
		}
		return slices.Clone(m.examples)
	})
}

// ArgsOf returns the positional arguments that the leaf cmd declares, each
// with its description.
func ArgsOf(cmd *cobra.Command) []ArgSpec {
	if cmd == nil {
		return nil
	}
	specs := ParseArgSpecs(cmd.Use)
	if len(specs) == 0 {
		return nil
	}
	commandMetaMu.RLock()
	m := commandMetaStore[weak.Make(cmd)]
	if m != nil && len(m.args) > 0 {
		for i := range specs {
			specs[i].Help = m.args[specs[i].Name]
		}
	}
	commandMetaMu.RUnlock()
	return specs
}

// DescribeArgs gives a description to every positional argument under root
// whose name is a key of help, unless the leaf describes that argument
// itself. A tree whose arguments share one vocabulary, such as a <tab>
// target, states each meaning once this way.
func DescribeArgs(root *cobra.Command, help map[string]string) {
	if root == nil || len(help) == 0 {
		return
	}
	walk(root, func(cmd *cobra.Command) {
		if !cmd.Runnable() {
			return
		}
		specs := ParseArgSpecs(cmd.Use)
		if len(specs) == 0 {
			return
		}
		hasMatch := false
		for _, spec := range specs {
			if _, ok := help[spec.Name]; ok {
				hasMatch = true
				break
			}
		}
		if !hasMatch {
			return
		}
		editCommandMeta(cmd, func(m *commandMeta) {
			merged := make(map[string]string, len(specs))
			for _, spec := range specs {
				if text, ok := m.args[spec.Name]; ok {
					merged[spec.Name] = text
				} else if text, ok := help[spec.Name]; ok {
					merged[spec.Name] = text
				}
			}
			m.args = merged
		})
	})
}

// IsMutating reports whether cmd changes state.
func IsMutating(cmd *cobra.Command) bool {
	return readCommandMeta(cmd, func(m *commandMeta) bool {
		return m.mutates
	})
}

// IsDestructive reports whether cmd permanently deletes, closes, revokes, or resets state.
func IsDestructive(cmd *cobra.Command) bool {
	return readCommandMeta(cmd, func(m *commandMeta) bool {
		return m.destructive
	})
}

// IsQuietUnsupported reports whether cmd explicitly rejects -q / --quiet.
func IsQuietUnsupported(cmd *cobra.Command) bool {
	return readCommandMeta(cmd, func(m *commandMeta) bool {
		return m.quietUnsupported
	})
}

// NextOf returns a copy of the follow-up workflow hints declared on cmd.
func NextOf(cmd *cobra.Command) []string {
	return readCommandMeta(cmd, func(m *commandMeta) []string {
		if len(m.next) == 0 {
			return nil
		}
		return slices.Clone(m.next)
	})
}

func formatExamples(examples []Example) string {
	var sb strings.Builder
	for i, ex := range examples {
		if i > 0 {
			sb.WriteString("\n")
		}
		if ex.Comment != "" {
			fmt.Fprintf(&sb, "  # %s\n", ex.Comment)
		}
		fmt.Fprintf(&sb, "  %s", ex.Command)
		if i < len(examples)-1 {
			sb.WriteString("\n")
		}
	}
	return sb.String()
}
