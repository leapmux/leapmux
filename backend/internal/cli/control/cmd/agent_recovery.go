package cmd

import (
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/util/agentlabels"
)

func newAgentRecoveriesCommand() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:   "recoveries",
		Short: "List agents whose input queue is paused waiting for process recovery approval",
		Examples: []cli.Example{
			{Command: "leapmux agent recoveries"},
			{Command: "leapmux agent recoveries --provider claude"},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.String("provider", "", "filter recovery targets by agent provider")
			addWorkerFlag(fs)
		},
		Run: runAgentRecoveries,
	})
}

type agentRecoveryRow struct {
	AgentID          string `json:"agent_id" table:"AGENT,short"`
	Title            string `json:"title" table:"TITLE"`
	Provider         string `json:"provider" table:"PROVIDER"`
	SessionID        string `json:"session_id,omitempty" table:"SESSION,short"`
	OutputGeneration int64  `json:"output_generation"`
	ProcessInstance  string `json:"process_instance_id"`
}

type agentRecoveryList struct {
	Targets []agentRecoveryRow `json:"targets"`
}

func (l agentRecoveryList) QuietLines() []string {
	out := make([]string, 0, len(l.Targets))
	for _, t := range l.Targets {
		out = append(out, t.AgentID)
	}
	return out
}

func runAgentRecoveries(inv *cli.Invocation) (any, error) {
	provider, err := parseProviderFlag(stringFlag(inv, "provider"))
	if err != nil {
		return nil, err
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	workerID, err := s.workerScope()
	if err != nil {
		return nil, err
	}
	var resp leapmuxv1.ListAgentInputRecoveriesResponse
	if err := callInnerRPC(s.ctx, s.c, workerID, leapmuxv1connect.WorkerAgentInputServiceListAgentInputRecoveriesProcedure,
		&leapmuxv1.ListAgentInputRecoveriesRequest{AgentProvider: provider}, &resp); err != nil {
		return nil, err
	}
	rows := make([]agentRecoveryRow, 0, len(resp.GetTargets()))
	for _, t := range resp.GetTargets() {
		rec := t.GetRecovery()
		rows = append(rows, agentRecoveryRow{
			AgentID:          t.GetAgentId(),
			Title:            t.GetTitle(),
			Provider:         agentlabels.CLIAlias(rec.GetAgentProvider()),
			SessionID:        rec.GetAgentSessionId(),
			OutputGeneration: rec.GetOutputGeneration(),
			ProcessInstance:  rec.GetProcessInstanceId(),
		})
	}
	return agentRecoveryList{Targets: rows}, nil
}

func newAgentRecoverCommand() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:     "recover <agent>",
		Short:   "Approve process recovery for an agent paused for login or process recovery",
		Mutates: true,
		Examples: []cli.Example{
			{Command: "leapmux agent recover 87a19d5f"},
		},
		Flags:    addTargetWorkspaceFlag,
		Complete: completeTargets,
		Run:      runAgentRecover,
	})
}

func runAgentRecover(inv *cli.Invocation) (any, error) {
	return runAgentQueue(inv, func(q agentQueue) (any, error) {
		queue, err := q.snapshot()
		if err != nil {
			return nil, err
		}
		rec := queue.GetProcessRecovery()
		if rec == nil {
			return nil, cli.Newf(cli.CodeNotFound, "agent %s has no pending process recovery", q.agent.ID)
		}
		var resp leapmuxv1.RecoverAgentInputProcessResponse
		if err := q.call(leapmuxv1connect.WorkerAgentInputServiceRecoverAgentInputProcessProcedure,
			&leapmuxv1.RecoverAgentInputProcessRequest{AgentId: q.agent.ID, Recovery: rec}, &resp); err != nil {
			return nil, err
		}
		next := resp.GetSnapshot()
		reason := pauseReasonOf(next)
		return agentQueueState{ID: q.agent.ID, Paused: reason != "", PauseReason: reason}, nil
	})
}
