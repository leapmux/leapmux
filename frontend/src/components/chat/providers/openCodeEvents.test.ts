import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerFor } from './registry'
import { input } from './testUtils'
import './opencode/plugin'
import './kilo/plugin'

describe('open code family native notifications', () => {
  for (const provider of [AgentProvider.OPENCODE, AgentProvider.KILO]) {
    const plugin = providerFor(provider)!
    it(`reads retry notices for provider ${provider}`, () => {
      const frame = {
        type: 'session.status',
        properties: {
          sessionID: 's',
          status: { type: 'retry', attempt: 2, message: ' rate limited ' },
        },
      }
      const entries = [
        {
          kind: 'retry',
          scope: 'api',
          attempt: 2,
          error: 'rate limited',
        },
      ]
      expect(plugin.transcript.notificationEntry?.(frame)).toEqual(entries)
      expect(plugin.transcript.classify(input(frame, null, provider))).toEqual({ kind: 'notification', entries })
      expect(plugin.session?.compactionBoundaryFromMessage?.(input(frame))).toBeNull()
    })

    it(`reads a compaction boundary without counts for provider ${provider}`, () => {
      const frame = {
        type: 'session.compacted',
        properties: { sessionID: 's' },
      }
      const entries = [
        { kind: 'compaction', phase: 'end' },
      ]
      expect(plugin.transcript.notificationEntry?.(frame)).toEqual(entries)
      expect(plugin.transcript.classify(input(frame, null, provider))).toEqual({ kind: 'notification', entries })
      expect(plugin.session?.compactionBoundaryFromMessage?.(input(frame))).toEqual({})
    })

    it(`validates optional retry counters for provider ${provider}`, () => {
      for (const attempt of [
        0,
        -1,
        1.5,
        Number.NaN,
        Number.POSITIVE_INFINITY,
        Number.MAX_SAFE_INTEGER + 1,
        '2',
        undefined,
      ]) {
        const frame = {
          type: 'session.status',
          properties: {
            status: { type: 'retry', attempt, message: '  ' },
          },
        }
        const entry = {
          kind: 'retry',
          scope: 'api',
          ...(attempt === 0
            ? { attempt: 0 }
            : {}),
        }
        expect(plugin.transcript.notificationEntry?.(frame)).toEqual([entry])
      }
    })

    it(`leaves unrelated or malformed frames unknown for provider ${provider}`, () => {
      for (const frame of [
        {},
        { type: 'session.status' },
        { type: 'session.status', properties: null },
        {
          type: 'session.status',
          properties: { status: [] },
        },
        {
          type: 'session.status',
          properties: {
            status: { type: 'idle' },
          },
        },
        { type: 'session.future', properties: {} },
      ]) {
        expect(plugin.transcript.notificationEntry?.(frame)).toEqual([])
        expect(plugin.transcript.classify(input(frame, null, provider))).toEqual({ kind: 'unknown' })
        expect(plugin.session?.compactionBoundaryFromMessage?.(input(frame))).toBeNull()
      }
    })
  }
})
