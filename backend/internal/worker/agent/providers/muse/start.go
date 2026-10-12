package muse

import (
	"context"
	"errors"
	"fmt"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

var _ agent.StartFunc = Start

func launchArgs(opts agent.Options) ([]string, error) {
	args := []string{"serve"}
	switch opts.Get(contracts.MuseOptionIDWorkspaceTrust) {
	case "", contracts.MuseWorkspaceTrustNative:
	case contracts.MuseWorkspaceTrustAgent:
		args = append(args, "--trust-workspace")
	default:
		return nil, fmt.Errorf("the Muse workspace trust choice is invalid")
	}
	return args, nil
}

// Start returns only after the native host acknowledges the selected session.
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	a := &Agent{sink: agent.NewModelProgressResetSink(sink), opts: opts, settings: make(optionmap.Map), sessions: make(map[string]*sessionState)}
	c, err := prepareConnection(ctx, opts, Registration(), nil)
	if err != nil {
		return nil, err
	}
	a.connection = c
	if err = c.initialize(opts, a.handleOutput, func() { a.retireHost(c.ProcessExitCompletion()) }); err != nil {
		return nil, err
	}
	if err = a.openSession(opts.ResumeSessionID, opts.EffectiveStartupTimeout()); err == nil {
		err = a.refreshCatalog(opts.EffectiveStartupTimeout())
	}
	if err == nil && opts.Effort() != "" && opts.Effort() != agent.EffortAuto {
		err = a.applyOption(agent.OptionIDEffort, opts.Effort(), opts.EffectiveStartupTimeout())
	}
	if err != nil {
		err = errors.Join(err, c.close())
		if opts.ResumeSessionID != "" {
			err = providerkit.ResumeFailedError(opts.ResumeSessionID, err)
		}
		return nil, c.FormatStartupError("start the Muse session", err)
	}
	a.sink.UpdateSessionID(a.sessionID)
	a.sink.BroadcastStatusActive(a.sessionID)
	a.sink.PersistSettingsRefresh(a.SettingsSnapshot().SurfacedOptions)
	return a, nil
}
