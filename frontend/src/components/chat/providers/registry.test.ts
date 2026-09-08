import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { pluginFor, providerFor } from './registry'
// Side-effect import: register every provider plugin so the registry is populated.
import '.'

describe('pluginFor', () => {
  it('resolves a registered provider to its plugin (same instance as providerFor)', () => {
    const plugin = pluginFor(AgentProvider.CLAUDE_CODE)
    expect(plugin).toBeDefined()
    expect(plugin).toBe(providerFor(AgentProvider.CLAUDE_CODE))
  })

  it('returns undefined for an absent provider -- no Claude (or any) guess', () => {
    expect(pluginFor(undefined)).toBeUndefined()
  })

  it('returns undefined for the UNSPECIFIED (proto-0) provider', () => {
    // proto-0 is `!= null` so it reaches providerFor, which has no entry for it.
    expect(pluginFor(AgentProvider.UNSPECIFIED)).toBeUndefined()
  })

  it('returns undefined for an unregistered enum value (backend/frontend skew)', () => {
    expect(pluginFor(999 as AgentProvider)).toBeUndefined()
  })
})

describe('supportsSessionGoal', () => {
  const supported = [
    AgentProvider.CLAUDE_CODE,
    AgentProvider.CODEX,
    AgentProvider.GOOSE,
    AgentProvider.REASONIX,
    AgentProvider.ZCODE,
    AgentProvider.GITHUB_COPILOT,
  ]
  const unsupported = [
    AgentProvider.CURSOR,
    AgentProvider.KILO,
    AgentProvider.OPENCODE,
    AgentProvider.PI,
  ]

  it('classifies every provider', () => {
    for (const provider of supported)
      expect(pluginFor(provider)?.supportsSessionGoal, AgentProvider[provider]).toBe(true)
    for (const provider of unsupported)
      expect(pluginFor(provider)?.supportsSessionGoal, AgentProvider[provider]).toBeFalsy()

    const classified = [...supported, ...unsupported].toSorted((a, b) => a - b)
    const allProviders = Object.values(AgentProvider)
      .filter((value): value is AgentProvider => typeof value === 'number' && value !== AgentProvider.UNSPECIFIED)
      .toSorted((a, b) => a - b)
    expect(classified).toEqual(allProviders)
  })
})
