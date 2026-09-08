import { describe, expect, it } from 'vitest'
import { ALL_PROVIDERS, PROVIDER_SUPPORTS_SESSION_GOAL } from '~/generated/contracts/providers'
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

describe('session-goal support', () => {
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

  /**
   * The answer is a CONTRACT, not a plugin field, so this reads the generated
   * table. The Go twin asserts the same table against the agents that implement
   * GoalWriter (`TestProviderSessionGoalContractMatchesGoalWriters`), so the
   * two languages cannot classify a provider differently.
   */
  it('classifies every provider', () => {
    for (const provider of supported)
      expect(PROVIDER_SUPPORTS_SESSION_GOAL[provider], AgentProvider[provider]).toBe(true)
    for (const provider of unsupported)
      expect(PROVIDER_SUPPORTS_SESSION_GOAL[provider], AgentProvider[provider]).toBe(false)

    const classified = [...supported, ...unsupported].toSorted((a, b) => a - b)
    const allProviders = Object.values(AgentProvider)
      .filter((value): value is AgentProvider => typeof value === 'number' && value !== AgentProvider.UNSPECIFIED)
      .toSorted((a, b) => a - b)
    expect(classified).toEqual(allProviders)
  })

  // The contract's own schema requires the field, so a provider with no entry
  // fails `task generate-contracts`. This is the runtime half of that guard.
  it('states an answer for every provider, with none missing', () => {
    for (const provider of ALL_PROVIDERS)
      expect(PROVIDER_SUPPORTS_SESSION_GOAL[provider], AgentProvider[provider]).toBeTypeOf('boolean')
  })
})
