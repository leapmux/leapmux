import { describe, expect, it } from 'vitest'
import { ALL_PROVIDERS } from '~/generated/contracts/providers'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { __resetProviderRegistryForTest, pluginFor, providerFor, registerProvider, retainedOutcome, retainedRowIsFinal } from './registry'
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

/** A plugin shaped well enough to register, for the registration-refusal tests below. */
function stubPlugin() {
  return {
    transcript: {
      classify: () => ({ kind: 'hidden' } as never),
      spanRole: () => 'other' as const,
      extractRow: () => null,
      extractDivider: () => null,
    },
  }
}

describe('provider registration', () => {
  it.each(ALL_PROVIDERS)('registers the provider %s', (provider) => {
    expect(pluginFor(provider)).toBeDefined()
  })

  // The registry is populated by SIDE-EFFECT imports, so a provider nobody imported and
  // a provider whose plugin failed to register read the same from `pluginFor`. The
  // count is what separates the two: every supported provider holds exactly one
  // entry, which the duplicate-refusal below keeps from ever becoming two.
  it('registers every supported provider exactly once', () => {
    expect(ALL_PROVIDERS).toHaveLength(10)
    for (const provider of ALL_PROVIDERS)
      expect(pluginFor(provider), AgentProvider[provider]).toBeDefined()
  })

  it('refuses UNSPECIFIED, which names no provider', () => {
    __resetProviderRegistryForTest()
    try {
      expect(() => registerProvider(AgentProvider.UNSPECIFIED, stubPlugin()))
        .toThrow('UNSPECIFIED names no provider')
      expect(pluginFor(AgentProvider.UNSPECIFIED)).toBeUndefined()
    }
    finally {
      __resetProviderRegistryForTest()
    }
  })

  // Last-write-wins silently left the registry holding whichever plugin imported
  // later -- a bundling decision, not a program decision.
  it('refuses a second registration of a provider already registered', () => {
    __resetProviderRegistryForTest()
    try {
      registerProvider(AgentProvider.CLAUDE_CODE, stubPlugin())
      expect(() => registerProvider(AgentProvider.CLAUDE_CODE, stubPlugin()))
        .toThrow('CLAUDE_CODE is already registered')
    }
    finally {
      __resetProviderRegistryForTest()
    }
  })
})

// A turn that ends while a tool call runs leaves no final frame, so the worker keeps the
// agent's last frame and records the outcome in its completion column. The rule is
// LeapMux's, and four renderers used to spell it separately with three different answers.
describe('retainedOutcome', () => {
  it.each([
    [MessageCompletion.COMPLETE, 'succeeded'],
    [MessageCompletion.INTERRUPTED, 'interrupted'],
    [MessageCompletion.ERROR, 'failed'],
  ])('reads %s as the shared outcome word %s', (completion, outcome) => {
    expect(retainedOutcome(completion)).toBe(outcome)
  })

  // Null says that LeapMux recorded nothing, so the provider's own bytes state the
  // outcome and a caller keeps whatever they say.
  it('reports no outcome for an unset or unrecognized completion', () => {
    expect(retainedOutcome(undefined)).toBeNull()
    expect(retainedOutcome(MessageCompletion.UNSPECIFIED)).toBeNull()
    expect(retainedOutcome(99 as MessageCompletion)).toBeNull()
  })

  // The two answer one question each, off ONE reading of the completion column.
  it('agrees with retainedRowIsFinal on every completion', () => {
    for (const completion of [undefined, MessageCompletion.UNSPECIFIED, MessageCompletion.COMPLETE, MessageCompletion.INTERRUPTED, MessageCompletion.ERROR]) {
      expect(retainedRowIsFinal(completion)).toBe(retainedOutcome(completion) !== null)
    }
  })
})
