package agent

// LeapMux notification-type vocabulary. The platform persists each of these as
// the inner `type` field on a notification envelope: LEAPMUX source for a
// worker-synthesized event, AGENT source for agent-emitted metadata that flows
// through the same renderer.
//
// The tokens themselves live in contracts/worker-vocab.json and reach Go as
// contracts.NotificationType*. This file holds only what the contract cannot:
// what the worker DOES with each one. Import the generated constant directly --
// one Go spelling per contract constant, per the repo-root CLAUDE.md, so an
// alias here would be a second name for the same value.
//
//   - AgentError: a worker-emitted agent failure (startup crash, restart
//     failure, settings-apply failure). Carries an `error` string with the
//     user-facing reason.
//
//   - SettingsChanged: the user updated the agent's model, effort, permission
//     mode or options. Carries a `changes` map of {key: {old, new}} entries.
//
//   - ContextCleared: the agent's context was cleared in place (/clear) or by a
//     fresh restart. Marks a turn boundary for the working-state heuristic.
//
//   - Interrupted: the user interrupted an in-flight turn. Marks a real turn end
//     on the frontend.
//
//   - PlanExecution: the worker started plan-mode execution. Carries the plan
//     metadata (file path, title).
//
//   - PlanUpdated: the active plan file changed -- either a new file path was
//     chosen or the title rotated.
//
//   - Compacting: the wire shape for the ACP and Codex compaction-progress
//     notifications, surfaced as system events.
//
//   - AgentSessionInfo: an ephemeral session-info payload (cost, context usage,
//     rate limits) outside the message stream. Frontends route it through
//     agentSessionStore, not the chat renderer.
//
//   - RateLimit and RateLimitEvent: the two wire shapes Claude and Codex use for
//     rate-limit metadata. Both route into the rate-limit popover.
//
//   - SubagentEnded: closes a subagent RUN. The worker writes one into a child
//     transcript each time that subagent's background-task row reaches a final
//     status, so the subagent tab shows WHERE it stopped and WHY instead of a
//     thinking indicator that never resolves. Carries a `status` field holding
//     the registry's final wire status (completed / failed / stopped /
//     interrupted). Provider-neutral: the registry close is the one moment every
//     provider agrees a subagent is over, including the ones whose child
//     transcript simply stops. One per run, NOT one per transcript, and nothing
//     follows it only until something does -- Claude restarts a finished
//     subagent when the parent messages it, and the restarted run ends the same
//     way, so a transcript holds as many of these as the subagent had runs, each
//     with more messages below it.
//
//   - GoalUpdated and GoalCleared: a session goal TRANSITION -- the objective
//     changed, the status changed, or the goal went away. Carries `objective`,
//     and for an update the neutral `goal_status` plus the provider's own
//     `status_detail`. They are worker-authored and provider-NEUTRAL on purpose:
//     five CLIs report a goal in five different wire shapes, so persisting each
//     verbatim would put five copies of goal parsing in five renderers. The
//     worker normalizes once and the browser has one. A transition only -- the
//     provider reports the goal far more often than it changes, and Codex sends
//     a full report after every completed tool call, so a row per report would
//     bury the transcript. The progress counters go out on the ephemeral
//     session-info channel instead, and never here.
