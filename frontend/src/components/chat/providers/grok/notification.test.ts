import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerFor } from '../registry'
import { input } from '../testUtils'
import './plugin'

const retryFrame = {
  jsonrpc: '2.0',
  method: '_x.ai/session_notification',
  params: { sessionId: 's1', update: { sessionUpdate: 'retry_state', type: 'retrying', attempt: 2, max_retries: 15, reason: 'reqwest error stream' } },
}

function notification(update: Record<string, unknown>): Record<string, unknown> {
  return { jsonrpc: '2.0', method: '_x.ai/session_notification', params: { sessionId: 's1', update } }
}

describe('grok native notifications', () => {
  const plugin = providerFor(AgentProvider.GROK_BUILD)!

  it('reads a retry state as one api retry entry', () => {
    const entries = [{ kind: 'retry', scope: 'api', attempt: 2, maxAttempts: 15, error: 'reqwest error stream' }]
    expect(plugin.transcript.notificationEntry?.(retryFrame)).toEqual(entries)
    expect(plugin.transcript.classify(input(retryFrame, undefined, AgentProvider.GROK_BUILD))).toEqual({ kind: 'notification', entries })
  })

  it('reads a completed compaction with its token counts', () => {
    const frame = notification({ sessionUpdate: 'auto_compact_completed', tokens_before: 120000, tokens_after: 30000 })
    const entries = [{ kind: 'compaction', phase: 'end', detail: { pre: 120000, post: 30000 }, outcome: 'completed' }]
    expect(plugin.transcript.notificationEntry?.(frame)).toEqual(entries)
    expect(plugin.session?.compactionBoundaryFromMessage?.(input(frame))).toEqual({ pre: 120000, post: 30000 })
  })

  it('reads a failed compaction with its error', () => {
    const frame = notification({ sessionUpdate: 'auto_compact_failed', error: 'the provider refused the summary' })
    expect(plugin.transcript.notificationEntry?.(frame)).toEqual([
      { kind: 'compaction', phase: 'end', outcome: 'failed', error: 'the provider refused the summary' },
    ])
  })

  it('reads a cancelled compaction as aborted', () => {
    const frame = notification({ sessionUpdate: 'auto_compact_cancelled' })
    expect(plugin.transcript.notificationEntry?.(frame)).toEqual([
      { kind: 'compaction', phase: 'end', outcome: 'aborted' },
    ])
    expect(plugin.session?.compactionBoundaryFromMessage?.(input(frame))).toEqual({})
  })

  it('leaves other frames unknown', () => {
    for (const frame of [
      notification({ sessionUpdate: 'auto_compact_started', percent_of_window: 81 }),
      notification({ sessionUpdate: 'retry_state', type: 'exhausted' }),
      {},
    ]) {
      expect(plugin.transcript.notificationEntry?.(frame)).toEqual([])
      expect(plugin.transcript.classify(input(frame, undefined, AgentProvider.GROK_BUILD))).toEqual({ kind: 'unknown' })
    }
  })
})
