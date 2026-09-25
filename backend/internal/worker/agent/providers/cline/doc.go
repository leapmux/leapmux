// Package cline drives Cline (`cline`) through its hub WebSocket protocol.
//
// Each agent starts a private Cline hub daemon as a child process, connects to
// it as a native hub client, and runs one Cline session on it. The hub is the
// surface of Cline that carries questions, plan approval, steering, the session
// list, images, reasoning effort, token usage and agent teams; Cline's ACP mode
// carries a small part of that, so this provider does not build on
// providers/acp.
//
// The files follow the provider package roles of AGENTS.md:
//
//   - connection.go: the daemon process, its discovery record and its end.
//   - rpc.go: the WebSocket, the commands and their replies, the event stream.
//   - events.go: event dispatch.
//   - output.go: the transcript rows and the turn's end.
//   - control.go: approvals, questions, the plan tool and their answers.
//   - subagent.go and team.go: subagents, teammate runs and their transcripts.
//   - session_lifecycle.go: session open, rebuild, and context clear.
//   - settings.go: the model, the effort and the mode.
//
// # What the private daemon shares
//
// The daemon uses the user's own Cline configuration and data: the account,
// the providers, the settings and the session history. It keeps private only
// its port, its discovery record (and the instance lock beside it), and its
// agenda task database (connection.go). That split has costs, which follow.
//
//   - A session runs in one daemon at a time. Cline writes a session's
//     conversation file whole, in place, so two runtimes of one session would
//     each overwrite the other's turns. The worker refuses to open a session
//     that another of its agents runs (hostedSessions), and to resume a
//     session that another Cline process holds, which Cline's session index
//     records (sessionHolder). It cannot stop the user's own Cline from
//     opening the session after the resume, which has the same hazard.
//     Different sessions are safe: the session index is a SQLite database in
//     WAL mode.
//   - Cline's durable hub event log and its run queue are files that take
//     the name of a fixed owner id (`hub-production`), so every daemon of the
//     user shares them. The worker scopes its stream to its own session and
//     replays from its own cursor, so it never reads another daemon's events.
//     The run queue is not safe: each daemon's start recovers the whole
//     queue (recoverOnStartup in hub-run-queue.ts of Cline 3.0.64). It marks
//     each running row interrupted, and it claims each queued row, which then
//     fails, because the session does not run in that daemon. So a run that
//     another client of the user queued with `run.enqueue` can fail when a
//     LeapMux agent starts. The worker enqueues no runs, and Cline's own
//     clients of 3.0.64 send no `run.enqueue`. The queue cannot move without
//     the session index (CLINE_DB_DATA_DIR), and Cline has no switch that
//     moves the queue alone.
//   - Cline's scheduled automations (cron) live in one shared database, and
//     every daemon polls it. A daemon claims a run with a lease, so no run
//     runs twice while the daemons live; a LeapMux daemon can run the user's
//     automation, and its stop cancels a run that it claimed. Cline has no
//     switch that keeps a daemon out of the schedule.
//   - A resumed session keeps the process id of the daemon that created it in
//     Cline's index. When that process is gone, Cline's reconciler can mark
//     the session failed in the history, although the session runs.
//   - Cline reads `telemetryOptOut` when the daemon starts, so a change of the
//     setting reaches a running agent at its next start.
//   - Cline accepts a WebSocket with no token from any client that states a
//     local Origin, when the daemon's host string is a local one. Any local
//     process can state that Origin: another user of the machine, or a web
//     page on a localhost origin. The daemon listens on a loopback host that
//     the rule does not cover (hubListen), and the start refuses a daemon
//     that accepts a connection with no token (refuseTokenlessHub).
//   - An update of Cline can replace the program under a running daemon; the
//     next agent starts the new one.
//
// # Subagents
//
// Cline states no agent on a subagent's output, and runs a subagent's tools
// without approval. See subagent.go for how the worker attributes the output
// and when a child transcript comes from Cline's store instead.
//
// # What Cline lacks
//
// Cline has no MCP elicitation, no to-do list and no session goal on any
// surface, so this provider offers none. It has no manual compaction on the
// hub either: Cline compacts by itself, and the transcript shows its notices.
package cline
