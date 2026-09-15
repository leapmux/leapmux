import { describe, expect, it } from 'vitest'
import { ALL_PROVIDERS } from '~/generated/contracts/providers'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { pluginFor, providerFor, retainedOutcome, retainedRowIsFinal } from './registry'
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

describe('provider registration', () => {
  it.each(ALL_PROVIDERS)('registers the provider %s', (provider) => {
    expect(pluginFor(provider)).toBeDefined()
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
