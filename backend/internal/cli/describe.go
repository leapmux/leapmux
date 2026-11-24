package cli

import (
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// CommandInfo describes one command for `leapmux commands`.
type CommandInfo struct {
	Path    string `json:"path" table:"COMMAND"`
	Summary string `json:"summary" table:"SUMMARY"`
	// Description is the long help of the command, when it has one.
	Description      string     `json:"description,omitempty"`
	Usage            string     `json:"usage"`
	Group            bool       `json:"group,omitempty"`
	Mutates          bool       `json:"mutates,omitempty"`
	QuietUnsupported bool       `json:"quiet_unsupported,omitempty"`
	Aliases          []string   `json:"aliases,omitempty"`
	Args             []ArgSpec  `json:"args,omitempty"`
	Flags            []FlagInfo `json:"flags,omitempty"`
	Examples         []Example  `json:"examples,omitempty"`
}

// FlagInfo describes one flag of a command.
type FlagInfo struct {
	Name      string `json:"name"`
	Shorthand string `json:"shorthand,omitempty"`
	// Type is the type of the value: "string", "bool", "int",
	// "stringSlice", "duration", and so on.
	Type       string   `json:"type"`
	Default    string   `json:"default,omitempty"`
	EnvDefault string   `json:"env_default,omitempty"`
	Usage      string   `json:"usage"`
	Required   bool     `json:"required,omitempty"`
	Enum       []string `json:"enum,omitempty"`
	// Global marks a flag that a parent command declares for all of its
	// descendants.
	Global bool `json:"global,omitempty"`
	// Group is the section of the help that lists the flag, when it has one.
	Group string `json:"group,omitempty"`
}

// CommandTree is the result of `leapmux commands`.
type CommandTree struct {
	Commands []CommandInfo `json:"commands"`
}

// DescribeOptions filters and trims the output of DescribeFiltered.
type DescribeOptions struct {
	// Prefix filters commands to those equal to Prefix or starting with Prefix+" ".
	Prefix string
	// Depth limits the number of space-separated path segments (0 = unlimited).
	Depth int
	// Compact drops long descriptions, examples, and non-required flags to keep
	// agent token usage minimal.
	Compact bool
}

// Describe lists every visible command under root, depth first, in the
// order that the help lists them.
func Describe(root *cobra.Command) CommandTree {
	return DescribeFiltered(root, DescribeOptions{})
}

// DescribeFiltered lists commands matching opts.
func DescribeFiltered(root *cobra.Command, opts DescribeOptions) CommandTree {
	prefix := strings.TrimSpace(opts.Prefix)
	prefixWords := len(strings.Fields(prefix))
	var out []CommandInfo
	var visit func(*cobra.Command)
	visit = func(cmd *cobra.Command) {
		if cmd.Hidden || cmd.Name() == "help" {
			return
		}
		if cmd != root {
			info := describeOne(cmd)
			include := true
			if prefix != "" && info.Path != prefix && !strings.HasPrefix(info.Path, prefix+" ") {
				include = false
			}
			if include && opts.Depth > 0 && len(strings.Fields(info.Path))-prefixWords > opts.Depth {
				include = false
			}
			if include {
				if opts.Compact {
					info = compactCommandInfo(info)
				}
				out = append(out, info)
			}
		}
		for _, child := range cmd.Commands() {
			visit(child)
		}
	}
	visit(root)
	return CommandTree{Commands: out}
}

func compactCommandInfo(info CommandInfo) CommandInfo {
	info.Description = ""
	info.Examples = nil
	var reqFlags []FlagInfo
	for _, f := range info.Flags {
		if f.Required {
			reqFlags = append(reqFlags, f)
		}
	}
	info.Flags = reqFlags
	return info
}

func describeOne(cmd *cobra.Command) CommandInfo {
	// cobra adds the help flag when a command runs, and the usage line ends
	// in "[flags]" only once a flag exists. Add it now, so every usage line
	// reads the same whichever command ran.
	cmd.InitDefaultHelpFlag()
	info := CommandInfo{
		Path:             strings.TrimPrefix(cmd.CommandPath(), cmd.Root().Name()+" "),
		Summary:          cmd.Short,
		Description:      cmd.Long,
		Usage:            cmd.UseLine(),
		Group:            cmd.HasSubCommands(),
		Mutates:          IsMutating(cmd),
		QuietUnsupported: cmd.Annotations[annotationQuietUnsupported] == "true",
		Aliases:          cmd.Aliases,
	}
	if !info.Group {
		info.Args = ArgsOf(cmd)
	}
	info.Examples = ExamplesOf(cmd)
	cmd.LocalFlags().VisitAll(func(f *pflag.Flag) {
		if f.Hidden || f.Name == "help" {
			return
		}
		info.Flags = append(info.Flags, describeFlag(f, false))
	})
	cmd.InheritedFlags().VisitAll(func(f *pflag.Flag) {
		if f.Hidden || f.Name == "help" || disallowedInheritedFlag(cmd, f.Name) {
			return
		}
		info.Flags = append(info.Flags, describeFlag(f, true))
	})
	return info
}

// Text is the help of the flag followed by what its declaration adds: the
// accepted values, the default, and whether it is required or repeatable.
// The help of a command and the reference page both print it.
func (f FlagInfo) Text() string {
	var extra []string
	if len(f.Enum) > 0 {
		extra = append(extra, "one of: "+strings.Join(f.Enum, ", "))
	}
	if f.EnvDefault != "" {
		extra = append(extra, "default: $"+f.EnvDefault)
	} else if f.Default != "" {
		extra = append(extra, "default: "+f.Default)
	}
	if f.Required {
		extra = append(extra, "required")
	}
	if f.Repeatable() {
		extra = append(extra, "repeatable")
	}
	if len(extra) == 0 {
		return f.Usage
	}
	return f.Usage + " (" + strings.Join(extra, "; ") + ")"
}

// Repeatable reports whether the flag takes more than one value.
func (f FlagInfo) Repeatable() bool {
	return strings.HasSuffix(f.Type, "Array") || strings.HasSuffix(f.Type, "Slice")
}

// Placeholder is the word that stands for the value of the flag in a help
// line, or "" for a flag that takes none.
func (f FlagInfo) Placeholder() string {
	switch f.Type {
	case "bool":
		return ""
	case "stringArray", "stringSlice":
		return "string"
	case "int", "int32", "int64", "uint", "uint32", "uint64":
		return "int"
	case "float32", "float64":
		return "number"
	default:
		return f.Type
	}
}

// Names is the spelling of the flag in a help line: its shorthand, its long
// name, and the placeholder of its value.
func (f FlagInfo) Names() string {
	names := "--" + f.Name
	if f.Shorthand != "" {
		names = "-" + f.Shorthand + ", " + names
	}
	if p := f.Placeholder(); p != "" {
		names += " " + p
	}
	return names
}

func describeFlag(f *pflag.Flag, global bool) FlagInfo {
	info := FlagInfo{
		Name:      f.Name,
		Shorthand: f.Shorthand,
		Type:      f.Value.Type(),
		Usage:     f.Usage,
		Global:    global,
		Required:  slices.Equal(f.Annotations[annotationRequired], []string{"true"}),
		Enum:      f.Annotations[annotationEnum],
	}
	if env := f.Annotations[annotationEnvDefault]; len(env) > 0 {
		info.EnvDefault = env[0]
	}
	if group := f.Annotations[annotationGroup]; len(group) > 0 {
		info.Group = group[0]
	}
	if f.DefValue != "" && f.DefValue != "false" && f.DefValue != "0" && f.DefValue != "[]" {
		info.Default = f.DefValue
	}
	return info
}
