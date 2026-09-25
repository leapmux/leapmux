// Package amp drives Amp (`amp`), Sourcegraph's coding agent, through its
// stream-JSON execute mode: `amp --execute --stream-json --stream-json-input`.
//
// Amp's stream resembles Claude Code's stream-json, but it is a protocol of its
// own, and this package shares no code with the claude package. The
// differences decide the design:
//
//   - Amp prints one `result` for each PROCESS, not for each turn. A turn ends
//     with the assistant message whose stop reason is `end_turn`.
//   - Amp runs the agent loop on its server. The CLI only executes the tools
//     that the server leases to it, so an interrupt (SIGINT) makes the CLI
//     cancel and EXIT, and any server error ends the process too. The agent
//     therefore SUPERVISES a sequence of CLI processes: it starts one for the
//     first message, and it resumes the thread in a new one after each exit.
//   - Amp creates its server thread when the process starts. The agent starts
//     no process before the first message, so a tab that sends nothing leaves
//     no empty thread in the user's account, and a mode change before the first
//     message costs no restart.
//   - Amp has no in-band permission prompt. The worker adds a `delegate`
//     permission rule to a settings file it generates for each agent, and Amp
//     then runs the worker's own executable as a helper for each tool call that
//     its local executor runs. The helper asks the agent over a Unix domain
//     socket in the agent's private directory, and the agent decides from its
//     CURRENT permission mode.
package amp
