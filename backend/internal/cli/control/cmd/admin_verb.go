package cmd

import (
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
)

// adminArgs is the parsed command line that an admin verb body reads: the
// positional arguments, and which flags the operator actually passed.
type adminArgs struct {
	// passed holds every flag name that the operator gave.
	passed map[string]bool
	// verbFlags holds the names that the verb declared, so AnyPassed can
	// ignore the global flags of `leapmux control`.
	verbFlags map[string]bool
	// Rest holds the positional arguments. The Use line of the verb already
	// fixed their count.
	Rest []string
	inv  *cli.Invocation
}

// Passed reports whether the operator gave the named flag.
//
// A zero value is not the same as an absent flag: `--memory-cost 0` and
// `--parallelism 0` are the ONLY legal values for the PBKDF2/SHA algorithm
// families, and `--max-attempts 0` must not be confused with "leave it
// alone". Every verb that merges a partial document asks this, never
// `!= 0` or `!= ""`.
func (a adminArgs) Passed(name string) bool { return a.passed[name] }

// AnyPassed reports whether the operator gave at least one of the verb's
// own flags.
//
// --hub selects the hub to talk to. It never asks the verb to change
// anything, so an invocation that carries --hub alone is still empty.
func (a adminArgs) AnyPassed() bool {
	for name := range a.passed {
		if a.verbFlags[name] {
			return true
		}
	}
	return false
}

// Arg returns the positional argument that the verb's Use line declares as
// name.
func (a adminArgs) Arg(name string) string { return a.inv.Arg(name) }

// Result prints the verb's result. A verb body returns it, so the result
// prints once, in the format that the command line selected.
func (a adminArgs) Result(v any) error { return a.inv.Printer.Result(v) }

// adminPageFlags holds the pagination pair that every paginated admin verb
// takes. Declare it through adminVerbSpec.Page, never by binding --limit
// and --cursor directly: the spec binds the pair AND checks the limit
// before the dial, so the two cannot separate.
//
// The hub normalizes the page too (normalizePageParams in hub/service), which
// is what protects every other client. The check here is for the ANSWER an operator
// gets: it runs before the dial, so `--limit 0` identifies the flag and its
// range instead of quietly returning a page of a size nobody asked for.
// Both sides read the same constants, so the range the CLI states is the
// hub's own. The hub caps an oversized limit; the CLI refuses it, so the
// operator sees the range instead of a shorter page.
type adminPageFlags struct {
	Limit  int64
	Cursor string
}

func (p *adminPageFlags) bind(fs *pflag.FlagSet) {
	fs.Int64Var(&p.Limit, "limit", contracts.DefaultPageLimit, "page size")
	fs.StringVar(&p.Cursor, "cursor", "", "the cursor that the previous page printed")
}

// request builds the page a listing RPC sends. The verb framework already
// refused a limit outside [minListLimit, contracts.MaxPageLimit], so the
// narrowing to int32 cannot overflow.
func (p *adminPageFlags) request() *leapmuxv1.PageRequest {
	return &leapmuxv1.PageRequest{Limit: int32(p.Limit), Cursor: p.Cursor}
}

// adminVerbSpec declares one `control admin ...` leaf. Use, Short, Examples
// and Run are required. Every other field is optional.
type adminVerbSpec struct {
	// Use is the name followed by the positional arguments, as cli.Leaf
	// states it.
	Use      string
	Short    string
	Long     string
	Examples []cli.Example
	// Mutates marks a verb that changes state, as cli.Leaf.Mutates states.
	Mutates bool
	// Flags binds the verb's own flags.
	Flags func(fs *pflag.FlagSet)
	// Page binds --limit and --cursor, and checks the limit before the
	// dial. A verb that reads a page declares this.
	Page *adminPageFlags
	// BeforeDial runs after the parse and BEFORE the client exists. Put
	// every local check here, so that a bad flag answers with the flag
	// and not with a connection error from a hub that the operator never
	// needed to reach. Local work that must not wait on a connection
	// belongs here also, such as the password prompt of `user create`.
	//
	// A value that BeforeDial computes reaches Run through a variable of
	// the enclosing verb, exactly as a flag value does.
	BeforeDial func(a adminArgs) error
	// Run does the RPC work and prints its result through a.Result.
	Run func(c *control.Client, a adminArgs) error
}

// adminLeaf builds one `control admin ...` leaf.
//
// The wrapper OWNS the dial. requireAdminClient carries the refusal that
// keeps admin commands out of agent reach, and a verb that built its own
// client would skip that refusal without any signal. Here there is no such
// verb to write, because the body receives a client and cannot make one.
//
// The order of the steps is fixed, and each step earns its position. The
// flags parse first, so a usage error identifies the flag. The limit check and
// BeforeDial run next, while no connection exists yet. The dial is the
// last step before the body.
func adminLeaf(spec adminVerbSpec) *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:      spec.Use,
		Short:    spec.Short,
		Long:     spec.Long,
		Examples: spec.Examples,
		Mutates:  spec.Mutates,
		Flags: func(fs *pflag.FlagSet) {
			if spec.Flags != nil {
				spec.Flags(fs)
			}
			if spec.Page != nil {
				spec.Page.bind(fs)
			}
		},
		Run: func(inv *cli.Invocation) (any, error) {
			a := adminArgs{passed: map[string]bool{}, verbFlags: map[string]bool{}, Rest: inv.Args, inv: inv}
			inv.Command.LocalNonPersistentFlags().VisitAll(func(f *pflag.Flag) {
				a.verbFlags[f.Name] = true
			})
			inv.Command.Flags().Visit(func(f *pflag.Flag) { a.passed[f.Name] = true })
			if spec.Page != nil {
				if err := validateListLimit(spec.Page.Limit); err != nil {
					return nil, err
				}
			}
			if spec.BeforeDial != nil {
				if err := spec.BeforeDial(a); err != nil {
					return nil, err
				}
			}
			c, err := requireAdminClient(inv)
			if err != nil {
				return nil, err
			}
			return nil, spec.Run(c, a)
		},
	})
}
