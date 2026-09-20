import type { NotificationEntry } from './model/notification'
import { describe, expect, it } from 'vitest'
import { NOTIFICATION_TYPE } from '~/generated/contracts/worker-vocab'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { COMPACTING_LABEL, flattenNotificationEntries, leapmuxNotificationEntry, notificationEntriesFor } from './notificationEntries'
import { providerFor } from './providers/registry'

// Side-effect-register the OpenCode plugin, so `notificationEntriesFor` dispatches
// the way production does. OpenCode stands for the whole Agent Client Protocol
// family here: those five daemons send JSON-RPC alone, so none of the five supplies
// a `notificationEntry` hook, and the neutral extractor is the only reader a row of
// theirs ever gets.
await import('./providers/opencode/plugin')

/** The one text block a single retry entry flattens into. */
function retryText(entry: Omit<Extract<NotificationEntry, { kind: 'retry' }>, 'kind'>): string {
  const blocks = flattenNotificationEntries([{ kind: 'retry', ...entry }])
  expect(blocks).toHaveLength(1)
  // The length is asserted above; `?.` is the type-level guard alone.
  return blocks[0]?.kind === 'text' ? blocks[0].text : ''
}

/**
 * ONE wording for every provider: what the agent retries, which attempt it is on,
 * how long it waits, and what went wrong.
 */
describe('flattenNotificationEntries retry', () => {
  it('states a sub-second wait in milliseconds', () => {
    // `Math.round(delayMs / 1000)` answered 0 here, so the row read "API retry 1/3 in
    // 0s" -- a wait the reader cannot tell from no wait at all.
    expect(retryText({ scope: 'api', attempt: 1, maxAttempts: 3, delayMs: 300 })).toBe('API retry 1/3 in 300ms')
    expect(retryText({ scope: 'api', delayMs: 499 })).toBe('API retry in 499ms')
  })

  it('states a whole-second wait in seconds', () => {
    expect(retryText({ scope: 'api', attempt: 1, maxAttempts: 3, delayMs: 2000 })).toBe('API retry 1/3 in 2s')
    expect(retryText({ scope: 'summarization', delayMs: 45_000 })).toBe('Summary retry in 45s')
  })

  it('states no wait at all for a zero or absent delay', () => {
    expect(retryText({ scope: 'api', attempt: 2 })).toBe('API retry 2')
    expect(retryText({ scope: 'api', attempt: 2, delayMs: 0 })).toBe('API retry 2')
  })

  it('names the reason beside the wait', () => {
    expect(retryText({ scope: 'api', attempt: 1, maxAttempts: 3, delayMs: 250, error: '529 overloaded' }))
      .toBe('API retry 1/3 in 250ms (529 overloaded)')
  })

  // A retry that WORKED ends the stall, and one that gave up states no wait -- the
  // agent is not waiting for anything.
  it('states the outcome rather than a wait once the stall ended', () => {
    expect(retryText({ scope: 'api', attempt: 2, maxAttempts: 3, delayMs: 300, succeeded: true }))
      .toBe('API retry 2/3 succeeded')
    expect(retryText({ scope: 'api', attempt: 3, maxAttempts: 3, delayMs: 300, willRetry: false, error: '529 overloaded' }))
      .toBe('API retry 3/3 gave up (529 overloaded)')
  })
})

/**
 * `compacting` is LeapMux's OWN envelope, and `PLAIN_ROW_TYPES` already promises a
 * plain row for it: a provider classifier that meets one answers `notification`. The
 * neutral extractor must therefore answer it, because there is no shared switch below
 * a plugin. A provider with no `notificationEntry` hook has nothing below the neutral
 * extractor at all, so a missing case draws a row that holds no block.
 */
describe('leapmuxNotificationEntry', () => {
  it('reads the compacting envelope as the neutral start of a compaction', () => {
    expect(leapmuxNotificationEntry({ type: NOTIFICATION_TYPE.Compacting }, AgentProvider.OPENCODE))
      .toStrictEqual([{ kind: 'compaction', phase: 'start' }])
  })

  it('draws the compacting row for a provider that supplies no notificationEntry hook', () => {
    // The premise, stated rather than assumed: this provider has no hook of its own.
    expect(providerFor(AgentProvider.OPENCODE)?.transcript.notificationEntry).toBeUndefined()
    expect(flattenNotificationEntries(notificationEntriesFor({ type: NOTIFICATION_TYPE.Compacting }, AgentProvider.OPENCODE)))
      .toStrictEqual([{ kind: 'divider', text: COMPACTING_LABEL, loading: true }])
  })
})
