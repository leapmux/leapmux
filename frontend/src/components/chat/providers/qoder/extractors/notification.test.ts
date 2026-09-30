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

  it('shows a compacting status and hides its final status', () => {
    expect(qoderNotificationEntry({ type: QODER_FRAME_KIND.System, subtype: QODER_SYSTEM_SUBTYPE.Status, status: 'compacting' }))
      .toEqual([{ kind: 'compaction', phase: 'start' }])
    expect(qoderNotificationEntry({ type: QODER_FRAME_KIND.System, subtype: QODER_SYSTEM_SUBTYPE.Status, status: null }))
      .toEqual([])
  })

  it('ignores unrelated native system and non-system frames', () => {
    expect(qoderNotificationEntry({ type: QODER_FRAME_KIND.System, subtype: QODER_SYSTEM_SUBTYPE.Init })).toEqual([])
    expect(qoderNotificationEntry({ type: QODER_FRAME_KIND.Assistant, subtype: QODER_SYSTEM_SUBTYPE.CompactBoundary })).toEqual([])
    expect(qoderCompactionBoundary(input({ type: QODER_FRAME_KIND.Assistant, subtype: QODER_SYSTEM_SUBTYPE.CompactBoundary }))).toBeNull()
  })
})
