import { describe, expect, it } from 'vitest'
import { KILO_EVENT, KILO_STATUS_TYPE } from '~/generated/contracts/kilo-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerFor } from '../../registry'
import { input } from '../../testUtils'
import '../plugin'

const plugin = providerFor(AgentProvider.KILO)!

describe('kiloNativeNotifications', () => {
  it('exposes retry notices through the registered transcript reader', () => {
    expect(plugin.transcript.notificationEntry?.({
      type: KILO_EVENT.SessionStatus,
      properties: {
        status: {
          type: KILO_STATUS_TYPE.Retry,
          attempt: 0,
          message: '  Wait for the provider  ',
        },
      },
    })).toEqual([
      {
        kind: 'retry',
        scope: 'api',
        attempt: 0,
        error: 'Wait for the provider',
      },
    ])
  })

  it.each([
    {},
    { attempt: -1 },
    { attempt: 1.5 },
    { attempt: Number.POSITIVE_INFINITY },
    { attempt: '3', message: 0 },
  ])('omits invalid optional retry facts: %j', (facts) => {
    expect(plugin.transcript.notificationEntry?.({
      type: KILO_EVENT.SessionStatus,
      properties: {
        status: { type: KILO_STATUS_TYPE.Retry, ...facts },
      },
    }))
      .toEqual([
        { kind: 'retry', scope: 'api' },
      ])
  })

  it.each([
    {},
    { type: 'unrelated' },
    { type: KILO_EVENT.SessionStatus },
    {
      type: KILO_EVENT.SessionStatus,
      properties: { status: [] },
    },
    {
      type: KILO_EVENT.SessionStatus,
      properties: {
        status: { type: 'idle' },
      },
    },
  ])('ignores unrelated or malformed notices: %j', (event) => {
    expect(plugin.transcript.notificationEntry?.(event)).toEqual([])
  })

  it('registers both the compaction notice and its meter boundary', () => {
    const event = {
      type: KILO_EVENT.SessionCompacted,
      properties: { sessionID: 'native-session' },
    }
    expect(plugin.transcript.notificationEntry?.(event)).toEqual([
      { kind: 'compaction', phase: 'end' },
    ])
    expect(plugin.session?.compactionBoundaryFromMessage?.(input(event))).toEqual({})
    expect(plugin.session?.compactionBoundaryFromMessage?.(input({ type: 'unrelated' }))).toBeNull()
  })
})
