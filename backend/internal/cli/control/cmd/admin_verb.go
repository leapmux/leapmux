package cmd

import (
	"flag"
	"fmt"

	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/hub/service"
)

// adminArgs is the parsed command line that an admin verb body reads: the
// positional arguments, and which flags the operator actually passed.
type adminArgs struct {
	// passed holds every flag name that the operator gave.
	passed map[string]bool
	// verbFlags holds the names that the verb declared, so AnyPassed can
	// ignore the framework flags that adminVerb binds.
	verbFlags map[string]bool
	// Rest holds the positional arguments. adminVerb already checked the
	// count against the one that the spec declares.
	Rest []string
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

// adminPageFlags holds the pagination pair that every paginated admin verb
// takes. Declare it through adminVerbSpec.Page, never by binding --limit
// and --cursor directly: the spec binds the pair AND checks the limit
// before the dial, so the two cannot separate.
//
// The hub normalizes the page too (service.NormalizePageParams), which is what
// protects every other client. The check here is for the ANSWER an operator
// gets: it runs before the dial, so `--limit 0` identifies the flag and its
// range instead of quietly returning a page of a size nobody asked for.
// Both sides read the same constants, so the range the CLI names is the
// hub's own. The hub caps an oversized limit; the CLI refuses it, so the
// operator sees the range instead of a shorter page.
type adminPageFlags struct {
	Limit  int64
	Cursor string
}

func (p *adminPageFlags) bind(fs *flag.FlagSet) {
	fs.Int64Var(&p.Limit, "limit", service.DefaultPageLimit, "page size")
	fs.StringVar(&p.Cursor, "cursor", "", "pagination cursor from the previous page")
}

// adminVerbSpec declares one `control admin ...` leaf. Run is required.
// Every other field is optional.
type adminVerbSpec struct {
	// Flags binds the verb's own flags. adminVerb already bound the
	// framework flags (--hub) on the same set.
	Flags func(fs *flag.FlagSet)
	// Page binds --limit and --cursor, and checks the limit before the
	// dial. A verb that reads a page declares this.
	Page *adminPageFlags
	// Positionals is the exact number of positional arguments that the
	// verb takes. Zero means none, and the parser then refuses any.
	Positionals int
	// Usage states the positional form. It is the message for a wrong
	// count AND the line --help prints under the flags, so an operator
	// learns the form without having to get the count wrong first. Set it
	// whenever Positionals is not zero.
	Usage string
	// BeforeDial runs after the parse and BEFORE the client exists. Put
	// every local check here, so that a bad flag answers with the flag
	// and not with a connection error from a hub that the operator never
	// needed to reach. Local work that must not wait on a connection
	// belongs here also, such as the password prompt of `user create`.
	//
	// A value that BeforeDial computes reaches Run through a variable of
	// the enclosing verb, exactly as a flag value does.
	BeforeDial func(a adminArgs) error
	// Run does the RPC work.
	Run func(c *control.Client, a adminArgs) error
}

// adminVerb runs one `control admin ...` leaf.
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
func adminVerb(rawCtx any, args []string, spec adminVerbSpec) error {
	cmd := asCtx(rawCtx)
	var hub string
	fs := flagSet(cmd, &hub)
	// Snapshot what the framework bound, so that AnyPassed stays correct
	// when flagSet gains another flag.
	framework := map[string]bool{}
	fs.VisitAll(func(f *flag.Flag) { framework[f.Name] = true })
	if spec.Flags != nil {
		spec.Flags(fs)
	}
	if spec.Page != nil {
		spec.Page.bind(fs)
	}

	// A verb with positional arguments needs the parser that keeps them;
	// ConfigureAndParse refuses every trailing token. That parser also
	// carries spec.Usage into --help, which the flag package cannot state
	// on its own: it knows the flags and nothing about the positionals.
	var perr error
	if spec.Positionals > 0 {
		perr = parseFlagsWithPositionals(fs, args, cmd.Description(), spec.Usage)
	} else {
		perr = parseFlags(fs, args, cmd.Description())
	}
	if perr != nil {
		return perr
	}
	if spec.Positionals > 0 && fs.NArg() != spec.Positionals {
		// A spec that declares a count but no message would otherwise
		// answer with an empty one, which tells an operator nothing.
		usage := spec.Usage
		if usage == "" {
			usage = fmt.Sprintf("this command takes exactly %d positional arguments", spec.Positionals)
		}
		return control.EmitError("invalid_request", usage)
	}

	a := adminArgs{passed: map[string]bool{}, verbFlags: map[string]bool{}, Rest: fs.Args()}
	fs.Visit(func(f *flag.Flag) { a.passed[f.Name] = true })
	fs.VisitAll(func(f *flag.Flag) {
		if !framework[f.Name] {
			a.verbFlags[f.Name] = true
		}
	})

	if spec.Page != nil {
		if err := validateListLimit(spec.Page.Limit); err != nil {
			return err
		}
	}
	if spec.BeforeDial != nil {
		if err := spec.BeforeDial(a); err != nil {
			return err
		}
	}

	c, err := requireAdminClient(hub)
	if err != nil {
		return err
	}
	return spec.Run(c, a)
}
