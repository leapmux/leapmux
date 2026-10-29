import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerFor } from '../registry'
import { input } from '../testUtils'
import { kiroNotificationEntry } from './notification'
import './plugin'

const refusalChunk = {
  sessionUpdate: 'agent_message_chunk',
  content: { type: 'text', text: 'Probe refusal explanation.' },
  _meta: {
    kiro: {
      refusal: { category: 'CYBER', explanation: 'Probe refusal explanation.', recommendedModel: 'kiro-probe-alt' },
    },
  },
}

function infoUpdate(kiro: Record<string, unknown>): Record<string, unknown> {
  return {
    sessionUpdate: 'session_info_update',
    _meta: { kiro },
  }
}

describe('kiroNotificationEntry', () => {
  const plugin = providerFor(AgentProvider.KIRO)!

  it('reads a refusal chunk as its own text entry', () => {
    expect(plugin.transcript.notificationEntry?.(refusalChunk)).toEqual([
      { kind: 'text', text: 'The model refused this request (CYBER) Probe refusal explanation. Recommended model: kiro-probe-alt' },
    ])
    expect(plugin.transcript.classify(input(refusalChunk, undefined, AgentProvider.KIRO)).kind).toBe('notification')
  })

  it('reads a summarization end as a compaction boundary', () => {
    expect(kiroNotificationEntry(infoUpdate({ kind: 'summarization_completed' }))).toEqual([
      { kind: 'compaction', phase: 'end', outcome: 'completed' },
    ])
    expect(kiroNotificationEntry(infoUpdate({
      kind: 'summarization_failed',
      summarization: { status: 'context_too_small' },
    }))).toEqual([
      { kind: 'compaction', phase: 'end', outcome: 'failed' },
    ])
    expect(plugin.session?.compactionBoundaryFromMessage?.(input(infoUpdate({ kind: 'summarization_completed' })))).toEqual({})
  })

  it('reads a v1 hook failure and cancellation', () => {
    expect(kiroNotificationEntry(infoUpdate({
      kind: 'hook_update',
      hook: {
        name: 'lint',
        status: 'failed',
        command: 'echo',
        result: { output: 'boom', exitCode: 1 },
      },
    }))).toEqual([
      {
        kind: 'hook',
        outcome: 'failed',
        name: 'lint',
        detail: 'boom',
        exitCode: 1,
      },
    ])
    expect(kiroNotificationEntry(infoUpdate({
      kind: 'hook_update',
      hook: { name: 'lint', status: 'canceled', command: 'echo' },
    }))).toEqual([
      { kind: 'hook', outcome: 'blocked', name: 'lint' },
    ])
    expect(kiroNotificationEntry(infoUpdate({
      kind: 'hook_update',
      hook: { name: 'lint', status: 'completed' },
    }))).toEqual([])
  })

  it('leaves other frames unknown', () => {
    expect(kiroNotificationEntry(infoUpdate({ kind: 'turn_end', stopReason: 'end_turn' }))).toEqual([])
    expect(kiroNotificationEntry({
      sessionUpdate: 'agent_message_chunk',
      content: { type: 'text', text: 'hi' },
      _meta: {
        kiro: { replayId: 'x' },
      },
    })).toEqual([])
  })
})
