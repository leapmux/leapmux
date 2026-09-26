import type { ParsedMessageContent } from '~/lib/messageParser'
import { describe, expect, it } from 'vitest'
import { ALL_PROVIDERS } from '~/generated/contracts/providers'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { __resetProviderRegistryForTest, __resetResolvedMessageMemoForTest, pluginFor, providerFor, registerProvider, resolveMessageForRendering, retainedOutcome, retainedRowIsFinal } from './registry'
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

/** One parse with the shape the parser hands the resolver. */
function rawParse(parent: Record<string, unknown> = {}): ParsedMessageContent {
  return { wrapper: null, topLevel: parent, parentObject: parent, rawText: '', supplementalContent: undefined, messageMetadata: undefined }
}

describe('resolveMessageForRendering', () => {
  it('returns a stable identity for one parse under one provider', () => {
    const parsed = rawParse({ type: 'assistant' })
    expect(resolveMessageForRendering(parsed, AgentProvider.CLAUDE_CODE))
      .toBe(resolveMessageForRendering(parsed, AgentProvider.CLAUDE_CODE))
  })

  it('returns the parse itself when the provider merges nothing', () => {
    const parsed = rawParse({ type: 'tool.updated' })
    // ZCode's merge only repairs a `scheduled` input; this frame states none, so
    // the resolved object IS the parse -- one object for every reader.
    const resolved = resolveMessageForRendering(parsed, AgentProvider.ZCODE)
    expect(resolved).toBe(parsed)
    expect(resolveMessageForRendering(resolved, AgentProvider.ZCODE)).toBe(resolved)
  })

  // One parse whose ZCode merge repairs an omitted input, so ZCode answers a
  // NEW object and a provider that merges nothing answers the parse itself.
  function scheduledParse(): ParsedMessageContent {
    return {
      wrapper: null,
      topLevel: { type: 'tool.updated', payload: { kind: 'scheduled', toolCallId: 'c1', toolName: 'Bash', inputOmitted: true } },
      parentObject: { type: 'tool.updated', payload: { kind: 'scheduled', toolCallId: 'c1', toolName: 'Bash', inputOmitted: true } },
      rawText: '',
      supplementalContent: { type: 'tool.updated', payload: { kind: 'scheduled', toolCallId: 'c1', input: { command: 'ls' } } },
      messageMetadata: undefined,
    }
  }

  it('answers a different object per provider for the same parse', () => {
    const parsed = scheduledParse()
    const claude = resolveMessageForRendering(parsed, AgentProvider.CLAUDE_CODE)
    const zcode = resolveMessageForRendering(parsed, AgentProvider.ZCODE)
    // Claude merges nothing for this frame: the parse itself. ZCode repairs the
    // omitted input: a new object, memoized per provider.
    expect(claude).toBe(parsed)
    expect(zcode).not.toBe(parsed)
    expect(claude).not.toBe(zcode)
    expect(claude).toBe(resolveMessageForRendering(parsed, AgentProvider.CLAUDE_CODE))
    expect(zcode).toBe(resolveMessageForRendering(parsed, AgentProvider.ZCODE))
  })

  it('drops the memo when the test resets it', () => {
    const parsed = scheduledParse()
    const before = resolveMessageForRendering(parsed, AgentProvider.ZCODE)
    __resetResolvedMessageMemoForTest()
    const after = resolveMessageForRendering(parsed, AgentProvider.ZCODE)
    expect(after).not.toBe(before)
    expect(after).toBe(resolveMessageForRendering(parsed, AgentProvider.ZCODE))
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
  // entry, which the duplicate-refusal below keeps from ever becoming two. A
  // pending provider has no plugin yet and is excluded until its package lands.
  it('registers every supported provider exactly once', () => {
    expect(ALL_PROVIDERS).toHaveLength(26)
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

  it('invalidates resolved content when registration changes or resets', () => {
    const provider = 999 as AgentProvider
    const parsed = rawParse({ value: 'stored' })
    __resetProviderRegistryForTest()
    try {
      // No plugin exists yet. This result must not survive the registration.
      expect(resolveMessageForRendering(parsed, provider)).toBe(parsed)
      registerProvider(provider, {
        ...stubPlugin(),
        transcript: {
          ...stubPlugin().transcript,
          resolveMessage: () => ({ value: 'resolved' }),
        },
      })
      const registered = resolveMessageForRendering(parsed, provider)
      expect(registered).not.toBe(parsed)
      expect(registered.parentObject).toEqual({ value: 'resolved' })

      __resetProviderRegistryForTest()
      expect(resolveMessageForRendering(parsed, provider)).toBe(parsed)
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
