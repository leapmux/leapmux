// Package kiro implements the Kiro CLI provider on the ACP base. Kiro runs its
// v3 engine through `kiro-cli-chat acp --agent-engine v3 --auth-method cli`,
// and it extends the Agent Client Protocol under `_kiro/` with these parts:
//
//   - Questions and MCP forms, as extension requests.
//   - Subagents that stream in the parent session under a tag.
//   - Workflows and goals whose steps run in sessions of their own.
//   - Markers that bracket every turn, the turns that Kiro starts by itself
//     included.
//   - Steering of a running turn.
//   - A context compaction that runs through a request.
package kiro
