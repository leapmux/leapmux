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
	cmd.Flags().SortFlags = false
	if l.Flags != nil {
		l.Flags(cmd.Flags())
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if err := validateRequiredFlags(cmd); err != nil {
			return err
		}
		ctx, stop := WithInterrupt(cmd.Context())
		defer stop()
		inv := &Invocation{
			Context: ctx,
			Command: cmd,
			Args:    args,
			Printer: PrinterFor(cmd),
			Stdin:   cmd.InOrStdin(),
			specs:   specs,
		}
		result, err := l.Run(inv)
		if err != nil {
			// An interrupt ends every call in flight, and each reports the
			// cancellation as its own failure. The user asked for the stop,
			// so the command exits as a shell reports an interrupted one.
			if Interrupted(inv.Context) {
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

// SetFooter sets the line that the help of cmd prints last.
func SetFooter(cmd *cobra.Command, footer string) {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[annotationFooter] = footer
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
	p := &Printer{Out: cmd.OutOrStdout(), Err: cmd.ErrOrStderr()}
	if f := cmd.Flags().Lookup("json"); f != nil && f.Value.String() == "true" {
		p.Format = FormatJSON
	}
	if f := cmd.Flags().Lookup("quiet"); f != nil && f.Value.String() == "true" {
		p.Quiet = true
	}
	return p
}

// AddOutputFlags registers --json and --quiet as persistent flags of cmd.
func AddOutputFlags(cmd *cobra.Command) {
	cmd.PersistentFlags().Bool("json", false, "print the result as one JSON envelope per line")
	cmd.PersistentFlags().BoolP("quiet", "q", false, "print only the ids of the result, one per line")
}
