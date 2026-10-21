// Package acp supplies the shared base for the Agent Client Protocol.
// These providers use it:
//   - Cursor.
//   - Dirac.
//   - fast-agent.
//   - Gemini.
//   - Goose.
//   - Grok Build.
//   - Junie.
//   - Kilo.
//   - Kiro.
//   - OpenCode.
//   - Qwen Code.
//   - Reasonix.
//
// Base owns the JSON-RPC process and session state. It handles shared protocol messages.
// A provider calls Start with a StartSpec. Its Configure function returns Hooks that supply its extensions.
package acp
