import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerFor } from '../registry'
import { input } from '../testUtils'
import './plugin'

const plugin = providerFor(AgentProvider.QWEN_CODE)!
function chunk(text: string, meta: Record<string, unknown>) {
  return {
    sessionUpdate: 'agent_message_chunk',
    content: { type: 'text', text },
    _meta: meta,
  }
}
function entries(frame: Record<string, unknown>) {
  return plugin.transcript.notificationEntry?.(frame)
}
function category(frame: Record<string, unknown>) {
  return plugin.transcript.classify(input(frame, null, AgentProvider.QWEN_CODE))
}

describe('qwen native notifications', () => {
  it.each([
    'vision_bridge_notice',
    'todo_stop_guard',
    '',
    'future_notice',
  ])('reads a discrete notice from %s', (source) => {
    const frame = chunk('A notice', { qwenDiscreteMessage: true, source })
    expect(entries(frame)).toEqual([
      { kind: 'text', text: 'A notice' },
    ])
    expect(category(frame)).toEqual({
      kind: 'notification',
      entries: [
        { kind: 'text', text: 'A notice' },
      ],
    })
  })

  it.each(['background_task_completed', 'background_notification', 'background_notification_response'])('leaves %s to its existing worker path', (source) => {
    const frame = chunk('background text', { qwenDiscreteMessage: true, source })
    expect(entries(frame)).toEqual([])
    expect(category(frame)).toEqual({ kind: 'unknown' })
  })

  it('reads an empty stop-hook chunk as a continued hook', () => {
    const frame = chunk('', {
      stopHookLoop: {
        iterationCount: 2,
        reasons: [
          'Run checks',
          ' Fix failures ',
          '',
          null,
          2,
        ],
        stopHookCount: 1,
      },
    })
    expect(entries(frame)).toEqual([
      {
        kind: 'hook',
        outcome: 'continued',
        event: 'Stop',
        detail: 'Run checks; Fix failures',
      },
    ])
    expect(category(frame).kind).toBe('notification')
  })

  it('reads a stop hook without optional reasons', () => {
    expect(entries(chunk('', { stopHookLoop: {} }))).toEqual([
      { kind: 'hook', outcome: 'continued', event: 'Stop' },
    ])
  })

  it('reads native compaction counts and preserves its warning', () => {
    const frame = chunk('Context compressed.', {
      contextCompression: {
        phase: 'done',
        originalTokenCount: 60000,
        newTokenCount: 44624,
        warning: 'little was saved',
      },
    })
    expect(entries(frame)).toEqual([
      {
        kind: 'compaction',
        phase: 'end',
        detail: { pre: 60000, post: 44624 },
      },
      { kind: 'text', text: 'little was saved' },
    ])
    expect(plugin.session?.compactionBoundaryFromMessage?.(input(frame))).toEqual({ pre: 60000, post: 44624 })
  })

  it('preserves zero counts and omits invalid counts', () => {
    for (const count of [
      0,
      -1,
      Number.NaN,
      Number.POSITIVE_INFINITY,
      '10',
      undefined,
    ]) {
      const frame = chunk('', {
        contextCompression: { phase: 'done', originalTokenCount: count, newTokenCount: count },
      })
      const detail = count === 0
        ? { pre: 0, post: 0 }
        : {}
      expect(entries(frame)).toEqual([
        { kind: 'compaction', phase: 'end', detail },
      ])
      expect(plugin.session?.compactionBoundaryFromMessage?.(input(frame))).toEqual(detail)
    }
  })

  it('marks an old start for the shared safety net and keeps its unknown classification', () => {
    const frame = chunk('Compressing context...', {
      contextCompression: { phase: 'progress' },
    })
    expect(plugin.transcript.compactionStart?.(frame)).toBe(true)
    expect(entries(frame)).toEqual([])
    expect(category(frame)).toEqual({ kind: 'unknown' })
    expect(plugin.session?.compactionBoundaryFromMessage?.(input(frame))).toBeNull()
  })

  it('keeps malformed or unrelated frames unknown', () => {
    for (const frame of [
      {},
      chunk('', { qwenDiscreteMessage: true }),
      chunk('ordinary', { qwenDiscreteMessage: 'true' }),
      chunk('', {
        contextCompression: { phase: 'future' },
      }),
      chunk('', { contextCompression: null }),
      chunk('', { stopHookLoop: null }),
      {
        ...chunk('thought', { qwenDiscreteMessage: true }),
        sessionUpdate: 'agent_thought_chunk',
      },
    ]) {
      expect(entries(frame)).toEqual([])
      expect(plugin.transcript.compactionStart?.(frame)).toBe(false)
      expect(category(frame)).toEqual({ kind: 'unknown' })
    }
  })

  it('reads a native-only notification thread through the registered plugin', () => {
    const frame = chunk('notice', { qwenDiscreteMessage: true })
    expect(plugin.transcript.classify(input(frame, {
      old_seqs: [],
      messages: [frame],
    }, AgentProvider.QWEN_CODE)))
      .toEqual({
        kind: 'notification',
        entries: [
          { kind: 'text', text: 'notice' },
        ],
      })
  })
})
