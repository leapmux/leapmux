// Package agent is the provider-neutral API of the agent runtime. It holds:
//
//   - the Agent interface that each running agent implements
//   - the Provider plugin interface, and ProviderDefaults to embed
//   - the ProviderServices facets that a provider reports its output through
//   - the start Options, and the Registration of each provider
//   - the Registry of the registrations, and the Manager that starts and tracks
//     the agents
//
// Package agent imports no provider. Each provider lives in a package of its own
// under providers/, and the composition root, package providers, lists them. The
// import graph enforces that split: a provider imports this package, so this
// package cannot import a provider.
package agent
