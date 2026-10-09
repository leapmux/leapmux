package muse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// prepareConnection owns the launch setup for a session host or a query host.
func prepareConnection(ctx context.Context, opts agent.Options, registration agent.Registration, env []string) (*connection, error) {
	args, err := launchArgs(opts)
	if err != nil {
		return nil, err
	}
	spec, err := providerkit.ResolveLaunch(ctx, opts, registration)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	pinned := []string{"MUSE_NO_AUTO_UPDATE=1"}
	cmd, delimiter, prefix := launch.Wrap(ctx, launch.WrapSpec{Shell: opts.Shell, LoginShell: opts.LoginShell, Launch: spec, BaseArgs: args, WorkingDir: opts.WorkingDir, SetEnv: pinned})
	if env == nil {
		env = cmd.Environ()
	}
	cmd.Env = envutil.PinEnv(providerkit.FinalizeAgentEnv(env, opts), pinned...)
	pipes, err := providerkit.SetupProcessPipes(cmd, cancel)
	if err != nil {
		return nil, err
	}
	c := &connection{JSONRPCProcess: providerkit.JSONRPCProcess{Process: providerkit.NewProcess(opts, providerkit.ProcessLaunch{ProviderName: "Muse Code", ShutdownGrace: registration.ShutdownGrace, PreambleDelimiter: delimiter, PreambleMetaPrefix: prefix}, pipes, ctx, cancel)}}
	c.stdout, c.stderr = pipes.Stdout(), pipes.Stderr()
	return c, nil
}

func openConnection(ctx context.Context, opts agent.Options, registration agent.Registration, env []string, handle providerkit.LineHandler) (*connection, error) {
	c, err := prepareConnection(ctx, opts, registration, env)
	if err != nil {
		return nil, err
	}
	if err = c.initialize(opts, handle, nil); err != nil {
		return nil, err
	}
	return c, nil
}

// initialize starts the reader after the caller installs the prepared transport.
func (c *connection) initialize(opts agent.Options, handle providerkit.LineHandler, closed func()) error {
	if err := c.StartCmd(); err != nil {
		return err
	}
	c.outputDone = make(chan struct{})
	c.DrainStderr(c.stderr)
	go func() {
		defer close(c.outputDone)
		c.ReadOutputLoop(agent.NewStdoutScanner(c.stdout), func(line *providerkit.ParsedLine) {
			if handle != nil {
				handle(line)
			} else if line.HasID() {
				c.RefuseUnsupportedRequest(line)
			}
		})
		if closed != nil {
			closed()
		}
	}()
	raw, err := c.request(methodInitialize, map[string]any{"clientInfo": map[string]string{"name": "leapmux", "version": "1"}, "capabilities": map[string]any{"experimentalApi": true, "requestedCapabilities": []string{"rawLog", "sessionMcp"}}}, opts.EffectiveStartupTimeout(), nil)
	if err == nil {
		err = json.Unmarshal(raw, &c.handshake)
		if err == nil && (c.handshake.Schema.Version != 1 || c.handshake.Schema.Fingerprint == "" || c.handshake.ServerInfo.Name != "muse") {
			err = fmt.Errorf("the Muse host returned an invalid handshake")
		}
	}
	if err == nil {
		err = c.SendNotification(methodInitialized, nil)
	}
	if err != nil {
		err = errors.Join(err, c.close())
		return c.FormatStartupError("initialize Muse Code", err)
	}
	return nil
}

func (c *connection) hasCapability(value string) bool {
	return slices.Contains(c.handshake.GrantedCapabilities, value)
}
