// Package acp is the shared base of the providers that speak the Agent Client
// Protocol: Cursor, Goose, Kilo, OpenCode and Reasonix. Base holds the JSON-RPC
// process, the session state and the handling of each ACP message. A provider
// starts through Start with a StartSpec, and states what it changes about the
// base in the Hooks that its Configure function returns.
package acp
