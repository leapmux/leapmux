import { describe, expect, it } from 'vitest'
import { MESSAGE_METADATA_FIELD } from '~/generated/contracts/worker-vocab'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { extractChatRow, extractedRow } from '../../../rowExtraction'
import { resolveMessageForRendering } from '../../registry'
import { deepseekHarnessResultDivider } from './resultDivider'
// Side-effect import: the pipeline case resolves the frame through the registered plugin.
import '../plugin'

/** The native `turn/end` session event that the Worker stores, with the Worker's turn duration merged in. */
function turnEnd(reason: Record<string, unknown>, durationMs?: unknown): Record<string, unknown> {
  return {
    type: 'turn/end',
    seq: 4,
    time: 1_700_000_002_300,
    data: { turn: 1, reason },
    ...(durationMs === undefined ? {} : { [MESSAGE_METADATA_FIELD.DurationMs]: durationMs }),
  }
}

describe('deepseekHarnessResultDivider', () => {
  it.each(['user', 'parent', 'disposed', 'legacy'])('shows a native aborted turn as interrupted for %s', (kind) => {
    expect(deepseekHarnessResultDivider({ type: 'turn/end', data: { turn: 1, reason: { kind: 'aborted', reason: { kind } } } })).toMatchObject({ label: expect.stringContaining('Turn interrupted') })
  })

  it('shows the message from the native structured error', () => {
    expect(deepseekHarnessResultDivider({ type: 'turn/end', data: { turn: 1, reason: { kind: 'error', error: { message: 'The exact native failure.', code: 'HTTP_400', status: 400 } } } })).toMatchObject({ isError: true, label: expect.stringContaining('The exact native failure.') })
  })

  // The native turn end states no duration. The Worker measures each turn from the times of its native start and end
  // events, and adds the duration to the turn end.
  it('states the duration that the Worker measured for a completed turn', () => {
    expect(deepseekHarnessResultDivider(turnEnd({ kind: 'completed' }, 2300))).toEqual({ label: 'Turn ended (2.3s)' })
  })

  it('keeps a measured zero as a duration', () => {
    expect(deepseekHarnessResultDivider(turnEnd({ kind: 'completed' }, 0))).toEqual({ label: 'Turn ended (0ms)' })
  })

  it('states the duration before the token limit', () => {
    expect(deepseekHarnessResultDivider(turnEnd({ kind: 'max-tokens' }, 2300))).toEqual({ label: 'Turn ended (2.3s, token limit)' })
  })

  it('states the duration of a turn that the user interrupted', () => {
    expect(deepseekHarnessResultDivider(turnEnd({ kind: 'aborted', reason: { kind: 'user' } }, 1500))).toEqual({ label: 'Turn interrupted (1.5s)' })
    expect(deepseekHarnessResultDivider(turnEnd({ kind: 'completed' }, 1500), MessageCompletion.INTERRUPTED)).toEqual({ label: 'Turn interrupted (1.5s)' })
  })

  it('states the duration and the native error of a failed turn', () => {
    expect(deepseekHarnessResultDivider(turnEnd({ kind: 'error', error: { message: 'The exact native failure.' } }, 900)))
      .toEqual({ label: 'Turn failed (900ms) — The exact native failure.', isError: true })
  })

  it.each([
    ['absent', undefined],
    ['not a number', '2300'],
  ])('states no duration when the duration is %s', (_case, durationMs) => {
    expect(deepseekHarnessResultDivider(turnEnd({ kind: 'completed' }, durationMs))).toEqual({ label: 'Turn ended' })
  })

  it('reads the duration from the Worker metadata of the stored turn end', () => {
    const frame = turnEnd({ kind: 'completed' })
    const resolved = resolveMessageForRendering({
      wrapper: null,
      topLevel: frame,
      parentObject: frame,
      rawText: '',
      supplementalContent: undefined,
      messageMetadata: { [MESSAGE_METADATA_FIELD.DurationMs]: 2300, [MESSAGE_METADATA_FIELD.ToolUses]: 0 },
    }, AgentProvider.DEEPSEEK_HARNESS)
    const row = extractedRow(extractChatRow(AgentProvider.DEEPSEEK_HARNESS, resolved, { kind: 'result_divider' }))
    expect(row?.kind === 'divider' ? row.divider.label : null).toBe('Turn ended (2.3s)')
  })
})
