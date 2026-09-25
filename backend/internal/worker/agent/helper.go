package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/atomicfile"
)

// A provider HELPER is a short program that a provider's CLI starts by itself,
// in the middle of its own work. Amp's `delegate` permission rule is the case
// that needs one: Amp runs a program for each tool call and reads the program's
// exit code as the decision.
//
// The helper is the executable that runs the worker, started a second time. It
// finds its work through ONE environment variable, contracts.EnvAgentHelper,
// and no argument. That is not a style choice. A CLI can give the program no
// argument at all: Amp spawns it with an empty argument list and no shell. A
// batch file cannot stand in for the arguments on Windows either, because the
// runtime refuses to spawn a `.bat` or `.cmd` file without a shell. The
// variable points at a spec file that the provider wrote into a directory that
// the worker owns, so no secret reaches argv or the environment.
//
// The mechanism is generic. The spec states the provider and a helper name, and
// the executable dispatches through the Registry to the provider's own
// HelperFunc. Nothing here knows what a helper does.
//
// A provider sets the variable in the environment of the CLI that starts the
// helper, and the executable runs as a helper when it starts with no argument
// and finds the variable set. These keep an inherited copy away from each
// process that did not ask for one:
//
//   - A worker run that is not a helper removes the variable from its own
//     environment (worker.RunAgentHelper), so no child of the worker inherits
//     it.
//   - FinalizeAgentEnv removes it from every agent that it launches.
//   - A terminal spawn removes it too.
//   - The desktop shell removes it from the sidecar that it spawns.
//
// One process still inherits the value by design: each command that the
// provider's CLI runs for the model. Such a command can run the helper and
// raise a banner in its own agent. It cannot answer one.

// HelperExitUnusable is the exit code of a helper run that could not start its
// work: the spec is unreadable, or no provider owns the helper that it
// specifies. It is a failure in every CLI's reading of an exit code. Amp reads 2
// or more as a refusal that shows the stderr text to the model, so the reason
// reaches the transcript and does not disappear.
const HelperExitUnusable = 2

// maxHelperSpecBytes caps the spec file. A spec holds a provider, a helper name
// and a small configuration, so a larger file is not one the worker wrote.
const maxHelperSpecBytes = 64 << 10

// HelperSpec is the file that tells the executable which helper to run.
type HelperSpec struct {
	// Provider is the provider's enum name, such as "AGENT_PROVIDER_AMP". The
	// enum name and not the CLI alias, because the alias table can grow a second
	// spelling for one provider and the enum name cannot.
	Provider string `json:"provider"`
	// Helper selects one entry of the provider's Registration.Helpers.
	Helper string `json:"helper"`
	// Config is the provider's own configuration for the helper. Only the
	// provider reads it.
	Config json.RawMessage `json:"config,omitempty"`
}

// HelperInvocation is what one helper run receives.
type HelperInvocation struct {
	// Config is HelperSpec.Config, which the provider wrote.
	Config json.RawMessage
	Stdin  io.Reader
	Stdout io.Writer
	// Stderr reaches the CLI that started the helper. A CLI can show it to the
	// model or to the user (Amp shows it as the reason for a refusal), so a
	// helper writes nothing to it but the message it means to deliver.
	Stderr io.Writer
	// Getenv reads the helper's environment, which the CLI that started it
	// composed. A test supplies its own.
	Getenv func(string) string
}

// HelperFunc runs one helper and returns its exit code. ctx ends when the
// process receives an interrupt or a termination signal.
type HelperFunc func(ctx context.Context, invocation HelperInvocation) int

// WriteHelperSpec writes spec to path with owner-only permissions, and returns
// the environment entry that points a helper run at it.
//
// atomicfile.WriteFile replaces the file in one step, so a helper that the CLI
// starts at the same moment reads either the old spec or the new one, never a
// torn one.
func WriteHelperSpec(path string, spec HelperSpec) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("helper spec path %q is not absolute", path)
	}
	if err := spec.validate(); err != nil {
		return "", err
	}
	data, err := json.Marshal(spec)
	if err != nil {
		return "", fmt.Errorf("encode helper spec: %w", err)
	}
	if err := atomicfile.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("write helper spec: %w", err)
	}
	return contracts.EnvAgentHelper + "=" + path, nil
}

// ReadHelperSpec reads and checks the spec at path.
func ReadHelperSpec(path string) (HelperSpec, error) {
	file, err := os.Open(path)
	if err != nil {
		return HelperSpec{}, fmt.Errorf("open helper spec: %w", err)
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, maxHelperSpecBytes+1))
	if err != nil {
		return HelperSpec{}, fmt.Errorf("read helper spec: %w", err)
	}
	if len(data) > maxHelperSpecBytes {
		return HelperSpec{}, fmt.Errorf("helper spec exceeds %d bytes", maxHelperSpecBytes)
	}
	var spec HelperSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		return HelperSpec{}, fmt.Errorf("decode helper spec: %w", err)
	}
	if err := spec.validate(); err != nil {
		return HelperSpec{}, err
	}
	return spec, nil
}

func (s HelperSpec) validate() error {
	var errs []error
	if strings.TrimSpace(s.Provider) == "" {
		errs = append(errs, errors.New("the helper spec states no provider"))
	}
	if strings.TrimSpace(s.Helper) == "" {
		errs = append(errs, errors.New("the helper spec states no helper"))
	}
	return errors.Join(errs...)
}

// Helper returns the helper that a provider registers under name.
func (r *Registry) Helper(provider leapmuxv1.AgentProvider, name string) (HelperFunc, bool) {
	reg, ok := r.byProvider[provider]
	if !ok {
		return nil, false
	}
	helper, ok := reg.Helpers[name]
	return helper, ok && helper != nil
}

// RunHelper reads the spec at specPath and runs the helper it states. It
// returns the exit code for the process. A spec it cannot use answers
// HelperExitUnusable, with the reason on stderr.
func (r *Registry) RunHelper(ctx context.Context, specPath string, invocation HelperInvocation) int {
	fail := func(format string, args ...any) int {
		_, _ = fmt.Fprintf(invocation.Stderr, "LeapMux helper: "+format+"\n", args...)
		return HelperExitUnusable
	}
	spec, err := ReadHelperSpec(specPath)
	if err != nil {
		return fail("%v", err)
	}
	value, known := leapmuxv1.AgentProvider_value[spec.Provider]
	provider := leapmuxv1.AgentProvider(value)
	if !known || provider == leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED {
		return fail("the helper spec states an unknown provider %q", spec.Provider)
	}
	helper, ok := r.Helper(provider, spec.Helper)
	if !ok {
		return fail("%s registers no helper %q", spec.Provider, spec.Helper)
	}
	invocation.Config = spec.Config
	if invocation.Getenv == nil {
		invocation.Getenv = os.Getenv
	}
	return helper(ctx, invocation)
}
