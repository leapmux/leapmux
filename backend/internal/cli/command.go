package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Annotation keys that a command or a flag carries. `leapmux commands`
// reads them, so the help, the completion and the machine-readable
// command tree all come from one declaration.
const (
	// annotationMutates marks a command that changes state.
	annotationMutates = "leapmux_mutates"
	// annotationFooter is a line that a group's help prints last.
	annotationFooter = "leapmux_footer"
	// annotationRequired marks a flag that a command requires.
	annotationRequired = "leapmux_required"
	// annotationEnum lists the values that a flag accepts.
	annotationEnum = "leapmux_enum"
	// annotationEnvDefault gives the environment variable that a flag
	// defaults to when the command line omits it.
	annotationEnvDefault = "leapmux_env_default"
	// annotationExamples holds the JSON of a leaf's []Example, so the
	// structured form survives beside the text that the help prints.
	annotationExamples = "leapmux_examples"
	// annotationArgs holds the JSON of a leaf's argument descriptions: a map
	// from the argument name to one sentence about the values it accepts.
	annotationArgs = "leapmux_args"
	// annotationGroup gives the section of the help that lists a flag, for a
	// command with more flags than one list reads well.
	annotationGroup = "leapmux_group"
	// annotationDisallowedFlags lists inherited persistent flag names (comma-
	// separated) that do not apply to this command subtree unless a descendant
	// declares its own local flag of the same name.
	annotationDisallowedFlags = "leapmux_disallowed_flags"
	// annotationNext holds the JSON of a leaf's []string workflow follow-up hints.
	annotationNext = "leapmux_next"
	// annotationDestructive marks a leaf that permanently removes or revokes
	// state, requiring a stricter edit-distance threshold in typo suggestions.
	annotationDestructive = "leapmux_destructive"
	// annotationQuietUnsupported marks a leaf that rejects -q / --quiet.
	annotationQuietUnsupported = "leapmux_quiet_unsupported"
	// annotationCommandGroup groups top-level commands into task sections in help.
	annotationCommandGroup = "leapmux_cmd_group"
)

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
}

// ArgSpec is one positional argument that a Use line declares.
type ArgSpec struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
	Variadic bool   `json:"variadic"`
	// Help is one sentence about the values that the argument accepts, from
	// Leaf.Args or DescribeArgs. ParseArgSpecs leaves it empty.
	Help string `json:"help,omitempty"`
}

// ParseArgSpecs reads the positional arguments that a Use line declares.
func ParseArgSpecs(use string) []ArgSpec {
	fields := strings.Fields(use)
	if len(fields) < 2 {
		return nil
	}
	var out []ArgSpec
	for _, f := range fields[1:] {
		spec := ArgSpec{}
		// The repetition mark stands outside the brackets ("<agent>...") or
		// inside them ("[text...]"); both forms are common.
		if strings.HasSuffix(f, "...") {
			spec.Variadic = true
			f = strings.TrimSuffix(f, "...")
		}
		switch {
		case strings.HasPrefix(f, "<") && strings.HasSuffix(f, ">"):
			spec.Required = true
			spec.Name = f[1 : len(f)-1]
		case strings.HasPrefix(f, "[") && strings.HasSuffix(f, "]"):
			spec.Name = f[1 : len(f)-1]
		default:
			continue
		}
		if name, ok := strings.CutSuffix(spec.Name, "..."); ok {
			spec.Variadic = true
			spec.Name = name
		}
		out = append(out, spec)
	}
	return out
}

// Invocation is what a leaf's Run receives.
type Invocation struct {
	Context context.Context
	Command *cobra.Command
	Args    []string
	Printer *Printer
	Stdin   io.Reader
	specs   []ArgSpec
}

// Arg returns the positional argument that the Use line declares as name,
// or "" when the command line omitted it. For a variadic argument it
// returns the first value; use Variadic for all of them.
func (inv *Invocation) Arg(name string) string {
	for i, spec := range inv.specs {
		if spec.Name == name {
			if i < len(inv.Args) {
				return inv.Args[i]
			}
			return ""
		}
	}
	panic(fmt.Sprintf("cli: %s declares no argument %q", inv.Command.CommandPath(), name))
}

// HasArg reports whether the command line supplied the positional argument
// name.
func (inv *Invocation) HasArg(name string) bool {
	for i, spec := range inv.specs {
		if spec.Name == name {
			return i < len(inv.Args)
		}
	}
	panic(fmt.Sprintf("cli: %s declares no argument %q", inv.Command.CommandPath(), name))
}

// Variadic returns every value of the variadic argument name.
func (inv *Invocation) Variadic(name string) []string {
	for i, spec := range inv.specs {
		if spec.Name == name {
			if !spec.Variadic {
				panic(fmt.Sprintf("cli: argument %q of %s is not variadic", name, inv.Command.CommandPath()))
			}
			if i < len(inv.Args) {
				return inv.Args[i:]
			}
			return nil
		}
	}
	panic(fmt.Sprintf("cli: %s declares no argument %q", inv.Command.CommandPath(), name))
}

// Changed reports whether the command line set the flag name.
func (inv *Invocation) Changed(name string) bool {
	f := inv.Command.Flags().Lookup(name)
	return f != nil && f.Changed
}

// StringArray returns each value of the repeated flag name, exactly as the
// command line gave it, or an empty list when it gave none. The flag must be
// a StringArray flag.
//
// pflag's GetStringArray is impossible here: it reads the values back from
// their CSV form, which states one empty value as "[]", so `--x ""` would
// come back as no value at all, and a script whose variable is unset would
// run as if it had omitted the flag instead of failing.
func (inv *Invocation) StringArray(name string) []string {
	flag := inv.Command.Flags().Lookup(name)
	if flag == nil {
		panic(fmt.Sprintf("cli: %s declares no flag %q", inv.Command.CommandPath(), name))
	}
	values, ok := flag.Value.(pflag.SliceValue)
	if !ok {
		panic(fmt.Sprintf("cli: flag %q of %s is not a list", name, inv.Command.CommandPath()))
	}
	return append([]string{}, values.GetSlice()...)
}

// NewLeaf builds the cobra command for l.
func NewLeaf(l Leaf) *cobra.Command {
	specs := ParseArgSpecs(l.Use)
	cmd := &cobra.Command{
		Use:         l.Use,
		Aliases:     l.Aliases,
		Short:       l.Short,
		Long:        l.Long,
		Example:     formatExamples(l.Examples),
		Args:        argsValidator(specs),
		Annotations: map[string]string{},
	}
	if len(l.Examples) > 0 {
		raw, err := json.Marshal(l.Examples)
		if err != nil {
			panic(fmt.Sprintf("cli: encode the examples of %q: %v", l.Use, err))
		}
		cmd.Annotations[annotationExamples] = string(raw)
	}
	for name := range l.Args {
		if !slices.ContainsFunc(specs, func(s ArgSpec) bool { return s.Name == name }) {
			panic(fmt.Sprintf("cli: %q describes the argument %q, which its Use line does not declare", l.Use, name))
		}
	}
	setArgHelp(cmd, l.Args)
	if l.Mutates {
		cmd.Annotations[annotationMutates] = "true"
	}
	if l.Destructive {
		cmd.Annotations[annotationDestructive] = "true"
	}
	if l.QuietUnsupported {
		cmd.Annotations[annotationQuietUnsupported] = "true"
	}
	if len(l.Next) > 0 {
		raw, err := json.Marshal(l.Next)
		if err != nil {
			panic(fmt.Sprintf("cli: encode next hints of %q: %v", l.Use, err))
		}
		cmd.Annotations[annotationNext] = string(raw)
	}
	cmd.Flags().SortFlags = false
	if l.Flags != nil {
		l.Flags(cmd.Flags())
	}
	registerEnumCompletions(cmd)
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
		if printer.Format != FormatText && printer.Quiet {
			if f := cmd.Flags().Lookup("json"); f != nil && f.Value.String() == "true" {
				return Usagef("--json and --quiet cannot be used together")
			}
			return Usagef("--format and --quiet cannot be used together")
		}
		if printer.Quiet && l.QuietUnsupported {
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

func registerEnumCompletions(cmd *cobra.Command) {
	registerEnumCompletionsForSet(cmd, cmd.Flags())
}

func registerEnumCompletionsForSet(cmd *cobra.Command, fs *pflag.FlagSet) {
	fs.VisitAll(func(flag *pflag.Flag) {
		values := flag.Annotations[annotationEnum]
		if len(values) == 0 {
			return
		}
		err := cmd.RegisterFlagCompletionFunc(flag.Name, func(_ *cobra.Command, _ []string, word string) ([]string, cobra.ShellCompDirective) {
			prefix := ""
			if flag.Value.Type() == "stringSlice" {
				if comma := strings.LastIndexByte(word, ','); comma >= 0 {
					prefix, word = word[:comma+1], word[comma+1:]
				}
			}
			var matches []string
			for _, value := range values {
				if strings.HasPrefix(value, word) {
					matches = append(matches, prefix+value)
				}
			}
			return matches, cobra.ShellCompDirectiveNoFileComp
		})
		if err != nil {
			panic(fmt.Sprintf("cli: complete flag %q of %q: %v", flag.Name, cmd.Use, err))
		}
	})
}

// EnumOf returns the enum values registered on f with SetEnum, or nil.
func EnumOf(f *pflag.Flag) []string {
	if f == nil {
		return nil
	}
	return f.Annotations[annotationEnum]
}

// specAt returns the declaration of the positional argument at index i.
func specAt(specs []ArgSpec, i int) (ArgSpec, bool) {
	if i < len(specs) {
		return specs[i], true
	}
	if len(specs) > 0 && specs[len(specs)-1].Variadic {
		return specs[len(specs)-1], true
	}
	return ArgSpec{}, false
}

// Group builds a command that only holds other commands. Run alone, it
// prints its help.
func Group(use, short string, children ...*cobra.Command) *cobra.Command {
	cmd := &cobra.Command{
		Use:         use,
		Short:       short,
		Annotations: map[string]string{},
	}
	cmd.AddCommand(children...)
	return cmd
}

// SetAliases attaches explicit short aliases to cmd and returns cmd.
func SetAliases(cmd *cobra.Command, aliases ...string) *cobra.Command {
	cmd.Aliases = append(cmd.Aliases, aliases...)
	return cmd
}

// SetCommandGroup assigns cmd to a named section in its parent's help output
// (such as "Daily Work", "Files & Git", "Local Daemon").
func SetCommandGroup(cmd *cobra.Command, group string) *cobra.Command {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[annotationCommandGroup] = group
	return cmd
}

// CommandGroupOf returns the help section group assigned to cmd, or "".
func CommandGroupOf(cmd *cobra.Command) string {
	if cmd == nil {
		return ""
	}
	return cmd.Annotations[annotationCommandGroup]
}

// SetFooter sets the line that the help of cmd prints last.
func SetFooter(cmd *cobra.Command, footer string) {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[annotationFooter] = footer
}

// DisallowInheritedFlags marks the named persistent flags as inapplicable to
// cmd and its descendants unless a descendant defines its own local flag of
// the same name. Disallowed flags are omitted from help and `commands --json`
// and rejected as unknown flags at run time.
func DisallowInheritedFlags(cmd *cobra.Command, names ...string) {
	if len(names) == 0 {
		return
	}
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	existing := strings.Split(cmd.Annotations[annotationDisallowedFlags], ",")
	seen := make(map[string]bool, len(existing)+len(names))
	var merged []string
	for _, item := range append(existing, names...) {
		item = strings.TrimSpace(item)
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		merged = append(merged, item)
	}
	cmd.Annotations[annotationDisallowedFlags] = strings.Join(merged, ",")
}

// disallowedInheritedFlag reports whether cmd rejects the inherited flag name.
func disallowedInheritedFlag(cmd *cobra.Command, name string) bool {
	if cmd.LocalFlags().Lookup(name) != nil {
		return false
	}
	for c := cmd; c != nil; c = c.Parent() {
		raw := c.Annotations[annotationDisallowedFlags]
		if raw == "" {
			continue
		}
		for item := range strings.SplitSeq(raw, ",") {
			if item == name {
				return true
			}
		}
	}
	return false
}

func validateAllowedFlags(cmd *cobra.Command) error {
	var disallowed string
	cmd.InheritedFlags().VisitAll(func(f *pflag.Flag) {
		if disallowed == "" && f.Changed && disallowedInheritedFlag(cmd, f.Name) {
			disallowed = f.Name
		}
	})
	if disallowed != "" {
		return Usagef("unknown flag: --%s", disallowed).
			WithHint(`run "%s --help" for usage`, cmd.CommandPath())
	}
	return nil
}

// ExamplesOf returns the examples that the leaf cmd declares.
func ExamplesOf(cmd *cobra.Command) []Example {
	raw, ok := cmd.Annotations[annotationExamples]
	if !ok {
		return nil
	}
	var out []Example
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		panic(fmt.Sprintf("cli: decode the examples of %q: %v", cmd.CommandPath(), err))
	}
	return out
}

// ArgsOf returns the positional arguments that the leaf cmd declares, each
// with its description.
func ArgsOf(cmd *cobra.Command) []ArgSpec {
	specs := ParseArgSpecs(cmd.Use)
	help := argHelp(cmd)
	for i := range specs {
		specs[i].Help = help[specs[i].Name]
	}
	return specs
}

// DescribeArgs gives a description to every positional argument under root
// whose name is a key of help, unless the leaf describes that argument
// itself. A tree whose arguments share one vocabulary, such as a <tab>
// target, states each meaning once this way.
func DescribeArgs(root *cobra.Command, help map[string]string) {
	var visit func(*cobra.Command)
	visit = func(cmd *cobra.Command) {
		if cmd.Runnable() {
			own := argHelp(cmd)
			merged := map[string]string{}
			for _, spec := range ParseArgSpecs(cmd.Use) {
				if text, ok := own[spec.Name]; ok {
					merged[spec.Name] = text
				} else if text, ok := help[spec.Name]; ok {
					merged[spec.Name] = text
				}
			}
			setArgHelp(cmd, merged)
		}
		for _, child := range cmd.Commands() {
			visit(child)
		}
	}
	visit(root)
}

func argHelp(cmd *cobra.Command) map[string]string {
	raw, ok := cmd.Annotations[annotationArgs]
	if !ok {
		return nil
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		panic(fmt.Sprintf("cli: decode the argument descriptions of %q: %v", cmd.CommandPath(), err))
	}
	return out
}

func setArgHelp(cmd *cobra.Command, help map[string]string) {
	if len(help) == 0 {
		delete(cmd.Annotations, annotationArgs)
		return
	}
	raw, err := json.Marshal(help)
	if err != nil {
		panic(fmt.Sprintf("cli: encode the argument descriptions of %q: %v", cmd.Use, err))
	}
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[annotationArgs] = string(raw)
}

// IsMutating reports whether cmd changes state.
func IsMutating(cmd *cobra.Command) bool {
	return cmd.Annotations[annotationMutates] == "true"
}

// IsDestructive reports whether cmd permanently deletes, closes, revokes, or resets state.
func IsDestructive(cmd *cobra.Command) bool {
	return cmd.Annotations[annotationDestructive] == "true"
}

// IsQuietUnsupported reports whether cmd explicitly rejects -q / --quiet.
func IsQuietUnsupported(cmd *cobra.Command) bool {
	return cmd.Annotations[annotationQuietUnsupported] == "true"
}

// NextOf returns the follow-up workflow hints declared on cmd.
func NextOf(cmd *cobra.Command) []string {
	raw := cmd.Annotations[annotationNext]
	if raw == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

// FlagGroupOf returns the help section group assigned to flag f, or "".
func FlagGroupOf(f *pflag.Flag) string {
	if f == nil {
		return ""
	}
	if vals := f.Annotations[annotationGroup]; len(vals) > 0 {
		return vals[0]
	}
	return ""
}

// MarkRequired marks the flag name as required. The command refuses to run
// without it, and `leapmux commands` lists it as required.
func MarkRequired(fs *pflag.FlagSet, name string) {
	mustAnnotate(fs, name, annotationRequired, []string{"true"})
}

// SetEnum records the values that the flag name accepts. The help lists
// them, the completion offers them, and `leapmux commands` reports them.
// The command still validates the value itself.
func SetEnum(fs *pflag.FlagSet, name string, values ...string) {
	mustAnnotate(fs, name, annotationEnum, values)
}

// SetEnvDefault records that the flag name defaults to the environment
// variable env. The flag's own default stays empty, so the help never prints
// the live value of the caller's environment.
func SetEnvDefault(fs *pflag.FlagSet, name, env string) {
	mustAnnotate(fs, name, annotationEnvDefault, []string{env})
}

// SetFlagGroup puts the flag name in the section group of the help. The
// help lists the flags of no group first, then each group in the order of
// its first flag.
func SetFlagGroup(fs *pflag.FlagSet, name, group string) {
	mustAnnotate(fs, name, annotationGroup, []string{group})
}

func mustAnnotate(fs *pflag.FlagSet, name, key string, values []string) {
	if err := fs.SetAnnotation(name, key, values); err != nil {
		panic(fmt.Sprintf("cli: annotate flag %q: %v", name, err))
	}
}

func validateRequiredFlags(cmd *cobra.Command) error {
	var missing []string
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if _, ok := f.Annotations[annotationRequired]; ok && !f.Changed {
			missing = append(missing, "--"+f.Name)
		}
	})
	if len(missing) == 0 {
		return nil
	}
	noun := "flag"
	if len(missing) > 1 {
		noun = "flags"
	}
	return Usagef("missing required %s %s", noun, strings.Join(missing, ", ")).
		WithHint(`run "%s --help" for usage`, cmd.CommandPath())
}

// argsValidator checks the count of positional arguments against specs.
func argsValidator(specs []ArgSpec) cobra.PositionalArgs {
	minArgs, maxArgs := 0, len(specs)
	for _, s := range specs {
		if s.Required {
			minArgs++
		}
		if s.Variadic {
			maxArgs = -1
		}
	}
	return func(cmd *cobra.Command, args []string) error {
		if len(args) < minArgs {
			return Usagef("missing argument <%s>", specs[len(args)].Name).
				WithHint(`run "%s --help" for usage`, cmd.CommandPath())
		}
		if maxArgs >= 0 && len(args) > maxArgs {
			return Usagef("unexpected argument %q", args[maxArgs]).
				WithHint(`run "%s --help" for usage`, cmd.CommandPath())
		}
		return nil
	}
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

// PrinterFor returns the printer for an invocation of cmd: JSON when --json
// is set, ids only when --quiet is set, and text otherwise.
func PrinterFor(cmd *cobra.Command) *Printer {
	p, _ := printerForValidated(cmd)
	return p
}

func printerForValidated(cmd *cobra.Command) (*Printer, error) {
	p := &Printer{Out: cmd.OutOrStdout(), Err: cmd.ErrOrStderr(), oneLineResult: IsMutating(cmd)}
	jsonFlag := cmd.Flags().Lookup("json")
	jsonSet := jsonFlag != nil && jsonFlag.Value.String() == "true"
	if f := cmd.Flags().Lookup("format"); f != nil && f.Changed {
		fmtVal, err := ParseFormat(f.Value.String())
		if err != nil {
			return p, err
		}
		if jsonSet && fmtVal != FormatJSON {
			return p, Usagef("--json and --format %s cannot be used together", f.Value.String())
		}
		p.Format = fmtVal
	} else if jsonSet {
		p.Format = FormatJSON
	}
	if f := cmd.Flags().Lookup("quiet"); f != nil && f.Value.String() == "true" {
		p.Quiet = true
	}
	if f := cmd.Flags().Lookup("wide"); f != nil && f.Value.String() == "true" {
		p.Wide = true
	}
	if f := cmd.Flags().Lookup("no-header"); f != nil && f.Value.String() == "true" {
		p.NoHeader = true
	}
	if f := cmd.Flags().Lookup("fields"); f != nil && f.Changed {
		raw := strings.TrimSpace(f.Value.String())
		if raw == "" {
			return p, Usagef("--fields is empty")
		}
		for part := range strings.SplitSeq(raw, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				p.Fields = append(p.Fields, part)
			}
		}
	}
	if f := cmd.Flags().Lookup("color"); f != nil && f.Changed {
		mode, err := ParseColorMode(f.Value.String())
		if err != nil {
			return p, err
		}
		p.Color = mode
	}
	return p, nil
}

// AddOutputFlags registers output formatting persistent flags on cmd.
func AddOutputFlags(cmd *cobra.Command) {
	fs := cmd.PersistentFlags()
	fs.Bool("json", false, "print the result as one JSON envelope per line")
	fs.String("format", "text", "output format")
	SetEnum(fs, "format", "text", "json", "ndjson", "tsv")
	fs.BoolP("quiet", "q", false, "print only the ids of the result, one per line")
	fs.BoolP("wide", "w", false, "do not truncate table columns")
	fs.Bool("no-header", false, "omit the header row from table and TSV output")
	fs.String("fields", "", "comma-separated columns or fields to print")
	fs.String("color", "auto", "when to emit ANSI colors on TTY output")
	SetEnum(fs, "color", "auto", "always", "never")
	registerEnumCompletionsForSet(cmd, fs)
}
