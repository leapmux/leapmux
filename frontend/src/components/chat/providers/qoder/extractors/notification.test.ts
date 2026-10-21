import { describe, expect, it } from 'vitest'
import { QODER_FRAME_KIND, QODER_SYSTEM_SUBTYPE } from '~/generated/contracts/qoder-protocol'
import { input } from '../../testUtils'
import { qoderCompactionBoundary, qoderNotificationEntry } from './notification'

describe('qoderNotificationEntry', () => {
  it('keeps a completed native boundary with its reported token counts', () => {
    const frame = {
      type: QODER_FRAME_KIND.System,
      subtype: QODER_SYSTEM_SUBTYPE.CompactBoundary,
      compact_metadata: { trigger: 'manual', pre_tokens: 8000, post_tokens: 400 },
    }
    expect(qoderNotificationEntry(frame)).toEqual([
      { kind: 'compaction', phase: 'end', detail: { trigger: 'manual', pre: 8000, post: 400 } },
    ])
    expect(qoderCompactionBoundary(input(frame))).toEqual({ trigger: 'manual', pre: 8000, post: 400 })
  })

  it('keeps a boundary without optional metadata', () => {
    const frame = { type: QODER_FRAME_KIND.System, subtype: QODER_SYSTEM_SUBTYPE.CompactBoundary }
    expect(qoderNotificationEntry(frame)).toEqual([{ kind: 'compaction', phase: 'end', detail: {} }])
    expect(qoderCompactionBoundary(input(frame))).toEqual({})
  })

  it('reads no entry from an old compaction status', () => {
    expect(qoderNotificationEntry({ type: QODER_FRAME_KIND.System, subtype: QODER_SYSTEM_SUBTYPE.Status, status: 'compacting' }))
      .toEqual([])
    expect(qoderNotificationEntry({ type: QODER_FRAME_KIND.System, subtype: QODER_SYSTEM_SUBTYPE.Status, status: null }))
      .toEqual([])
  })

  it('reads a retry with its remaining delay and status fallback', () => {
    const frame = { type: QODER_FRAME_KIND.System, subtype: QODER_SYSTEM_SUBTYPE.ApiRetry, attempt: 1, max_retries: 10, retry_delay_ms: 35000, error_status: 429 }
    expect(qoderNotificationEntry(frame)).toEqual([{ kind: 'retry', scope: 'api', attempt: 1, maxAttempts: 10, delayMs: 35000, error: '429' }])
    expect(qoderNotificationEntry({ ...frame, attempt: -1, max_retries: 1.5, retry_delay_ms: Infinity, error_status: undefined }))
      .toEqual([{ kind: 'retry', scope: 'api' }])
  })

  it('reads the optional model queue counters without invalid numbers', () => {
    const frame = { type: QODER_FRAME_KIND.System, subtype: QODER_SYSTEM_SUBTYPE.ModelQueueStatus }
    expect(qoderNotificationEntry(frame)).toEqual([{ kind: 'status', text: 'Model request queued' }])
    expect(qoderNotificationEntry({ ...frame, queue_count: 2, wait_time_ms: 2500 }))
      .toEqual([{ kind: 'status', text: 'Model request queued (2 ahead), waited 2.5s' }])
    for (const value of [0, -1, NaN, Infinity]) {
      expect(qoderNotificationEntry({ ...frame, queue_count: value, wait_time_ms: value }))
        .toEqual([{ kind: 'status', text: 'Model request queued' }])
    }
  })

  it('reads a failed hook with its detail', () => {
    const frame = { type: QODER_FRAME_KIND.System, subtype: QODER_SYSTEM_SUBTYPE.HookResponse, hook_event: 'SessionStart', hook_name: 'check', exit_code: 1, stderr: 'Failure.' }
    expect(qoderNotificationEntry(frame)).toEqual([{ kind: 'hook', outcome: 'failed', event: 'SessionStart', name: 'check', exitCode: 1, detail: 'Failure.' }])
    expect(qoderNotificationEntry({ ...frame, stderr: ' ', output: 'Fallback.', exit_code: 1.5 }))
      .toEqual([{ kind: 'hook', outcome: 'failed', event: 'SessionStart', name: 'check', detail: 'Fallback.' }])
  })

  it('reads a size warning with zero and absent counts', () => {
    const frame = { type: QODER_FRAME_KIND.System, subtype: QODER_SYSTEM_SUBTYPE.AgentsMdSizeWarning }
    expect(qoderNotificationEntry({ ...frame, total_bytes: 40000, threshold_bytes: 0 }))
      .toEqual([{ kind: 'text', text: 'Warning: AGENTS.md files total 40000 bytes, above the 0-byte threshold' }])
    expect(qoderNotificationEntry({ ...frame, total_bytes: -1, threshold_bytes: NaN }))
      .toEqual([{ kind: 'text', text: 'Warning: AGENTS.md files exceed the configured size threshold' }])
  })

  it('ignores unrelated native system and non-system frames', () => {
    expect(qoderNotificationEntry({ type: QODER_FRAME_KIND.System, subtype: QODER_SYSTEM_SUBTYPE.Init })).toEqual([])
    expect(qoderNotificationEntry({ type: QODER_FRAME_KIND.Assistant, subtype: QODER_SYSTEM_SUBTYPE.CompactBoundary })).toEqual([])
    expect(qoderCompactionBoundary(input({ type: QODER_FRAME_KIND.Assistant, subtype: QODER_SYSTEM_SUBTYPE.CompactBoundary }))).toBeNull()
  })
})
