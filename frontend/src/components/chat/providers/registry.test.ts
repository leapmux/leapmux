import type { ProviderPlugin } from './capabilities'
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

/** Supply every required transcript method for registration tests. */
function stubPlugin(): ProviderPlugin {
  return {
    transcript: {
      classify: () => ({ kind: 'hidden' }),
      spanRole: () => 'other',
      extractRow: () => null,
      extractDivider: () => null,
    },
  }
}

describe('provider registration', () => {
  it.each(ALL_PROVIDERS)('registers the provider %s', (provider) => {
    expect(pluginFor(provider)).toBeDefined()
  })

  // Every supported provider requires a plugin import and one registration.
  // Duplicate registration must fail, so import order cannot replace a plugin.
  it('registers every supported provider exactly once', () => {
    expect(ALL_PROVIDERS).toHaveLength(30)
    expect(new Set(ALL_PROVIDERS).size).toBe(ALL_PROVIDERS.length)
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

  // A duplicate registration must fail. Import order must not choose the plugin.
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

// An interrupted tool can leave no final native frame.
// The Worker keeps its last frame and records the outcome in the completion column.
// Every provider uses the same completion rule.
describe('retainedOutcome', () => {
  it('keeps a final row without inventing an outcome for finished completion', () => {
    const completion = MessageCompletion.FINISHED
    expect(retainedRowIsFinal(completion)).toBe(true)
    expect(retainedOutcome(completion)).toBeNull()
  })

  it.each([
    [MessageCompletion.COMPLETE, 'succeeded'],
    [MessageCompletion.INTERRUPTED, 'interrupted'],
    [MessageCompletion.ERROR, 'failed'],
  ])('reads %s as the shared outcome word %s', (completion, outcome) => {
    expect(retainedOutcome(completion)).toBe(outcome)
  })

  // An unset completion keeps the native outcome.
  it('reports no outcome for an unset or unrecognized completion', () => {
    expect(retainedOutcome(undefined)).toBeNull()
    expect(retainedOutcome(MessageCompletion.UNSPECIFIED)).toBeNull()
    expect(retainedOutcome(99 as MessageCompletion)).toBeNull()
  })

  // Both helpers read the same completion column.
  it('derives finality for known outcomes and absent metadata', () => {
    for (const completion of [undefined, MessageCompletion.UNSPECIFIED, MessageCompletion.COMPLETE, MessageCompletion.INTERRUPTED, MessageCompletion.ERROR]) {
      expect(retainedRowIsFinal(completion)).toBe(retainedOutcome(completion) !== null)
    }
  })
})
