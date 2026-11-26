package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Flag annotation keys stored on pflag.Flag.Annotations. Help rendering,
// shell completion, and `leapmux commands` read them from one declaration.
const (
	// annotationRequired marks a flag that a command requires.
	annotationRequired = "leapmux_required"
	// annotationEnum lists the values that a flag accepts.
	annotationEnum = "leapmux_enum"
	// annotationEnvDefault gives the environment variable that a flag
	// defaults to when the command line omits it.
	annotationEnvDefault = "leapmux_env_default"
	// annotationGroup gives the section of the help that lists a flag, for a
	// command with more flags than one list reads well.
	annotationGroup = "leapmux_group"
)

// EnumOf returns the enum values registered on f with SetEnum, or nil.
func EnumOf(f *pflag.Flag) []string {
	if f == nil {
		return nil
	}
	return f.Annotations[annotationEnum]
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

func validateAllowedFlags(cmd *cobra.Command) error {
	var disallowed string
	cmd.InheritedFlags().VisitAll(func(f *pflag.Flag) {
		if disallowed == "" && f.Changed && IsInheritedFlagDisallowed(cmd, f.Name) {
			disallowed = f.Name
		}
	})
	if disallowed != "" {
		return Usagef("unknown flag: --%s", disallowed).
			WithHint(`run "%s --help" for usage`, cmd.CommandPath())
	}
	return nil
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
		if p.Format != FormatText {
			if jsonSet {
				return p, Usagef("--json and --quiet cannot be used together")
			}
			return p, Usagef("--format and --quiet cannot be used together")
		}
		p.Format = FormatQuiet
	}
	if f := cmd.Flags().Lookup("wide"); f != nil && f.Value.String() == "true" {
		p.Wide = true
	}
	if f := cmd.Flags().Lookup("no-header"); f != nil && f.Value.String() == "true" {
		p.NoHeader = true
	}
	if f := cmd.Flags().Lookup("fields"); f != nil && f.Changed {
		for part := range strings.SplitSeq(f.Value.String(), ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				p.Fields = append(p.Fields, part)
			}
		}
		if len(p.Fields) == 0 {
			return p, Usagef("--fields is empty")
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
