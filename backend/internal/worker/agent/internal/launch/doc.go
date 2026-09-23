// Package launch starts an agent provider's program inside the user's shell.
//
// It answers three questions for every provider, and it knows no provider:
//
//   - Where is the program? A Locator probes bare names through the user's
//     login shell, or runs a resolver the provider supplies. CheckBinary caches
//     each conclusive probe for the worker's lifetime.
//   - How is it started? Wrap builds the shell command in the user's own
//     dialect (POSIX, csh, Nushell or PowerShell), with the preamble delimiter
//     and metadata lines the caller reads back from stdout.
//   - What does the environment the user's shell sets up allow? An
//     EnvGatedArgs value makes arguments depend on variables that only the
//     shell, after its profile runs, can see.
//
// The package imports no agent package. Package agent imports it, and the
// Go internal rule keeps every caller outside the agent tree from doing so.
package launch
