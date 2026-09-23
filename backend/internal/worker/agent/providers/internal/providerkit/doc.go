// Package providerkit holds the plumbing that several providers share:
//
//   - Process, the base of a provider process, with its stdout and stderr
//     handling
//   - JSONRPCProcess, which adds request correlation and the publication of
//     control requests
//   - the helpers for turn state, tool spans, attachments, option groups, effort
//     labels and goal commands
//
// Only the provider packages under providers/ can import it. The Go internal
// rule enforces that.
package providerkit
