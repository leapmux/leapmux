import { describe, expect, it } from 'vitest'
import { MESSAGE_METADATA_FIELD } from '~/generated/contracts/worker-vocab'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { extractChatRow, extractedRow } from '../../../rowExtraction'
import { resolveMessageForRendering } from '../../registry'
import { commandCodeResultDivider } from './resultDivider'
// Side-effect import: the pipeline case resolves the frame through the registered plugin.
import '../plugin'

/** The native `turn/completed` frame that the Worker stores, with the Worker's turn duration merged in. */
function turnCompleted(params: Record<string, unknown>, durationMs?: unknown): Record<string, unknown> {
  return {
    jsonrpc: '2.0',
    method: 'turn/completed',
    params: { turnId: 'turn_1', ...params },
    ...(durationMs === undefined ? {} : { [MESSAGE_METADATA_FIELD.DurationMs]: durationMs }),
  }
}

describe('commandCodeResultDivider', () => {
  it('returns null for a frame that is not a completed turn', () => {
    expect(commandCodeResultDivider({ method: 'turn/started', params: { turnId: 'turn_1' } })).toBeNull()
    expect(commandCodeResultDivider('turn/completed')).toBeNull()
    expect(commandCodeResultDivider(null)).toBeNull()
  })

  // Command Code states no duration for a turn. The Worker measures each turn and adds the duration to the turn end.
  it('states the duration that the Worker measured for a completed turn', () => {
    expect(commandCodeResultDivider(turnCompleted({ stopReason: 'end_turn' }, 2300))).toEqual({ label: 'Turn ended (2.3s)' })
  })

  it('keeps a measured zero as a duration', () => {
    expect(commandCodeResultDivider(turnCompleted({ stopReason: 'end_turn' }, 0))).toEqual({ label: 'Turn ended (0ms)' })
  })

  it('states the duration before the turn limit', () => {
    expect(commandCodeResultDivider(turnCompleted({ stopReason: 'max_turns' }, 2300))).toEqual({ label: 'Turn ended (2.3s, turn limit)' })
  })

  it('states the duration of a turn that the user interrupted', () => {
    expect(commandCodeResultDivider(turnCompleted({ stopReason: 'interrupted' }, 1500))).toEqual({ label: 'Turn interrupted (1.5s)' })
    expect(commandCodeResultDivider(turnCompleted({ stopReason: 'end_turn' }, 1500), MessageCompletion.INTERRUPTED)).toEqual({ label: 'Turn interrupted (1.5s)' })
  })

  it('states the duration and the native error of a failed turn', () => {
    expect(commandCodeResultDivider(turnCompleted({ stopReason: 'run_error', error: { message: 'Native connection failed.' } }, 900)))
      .toEqual({ label: 'Turn failed (900ms) — Native connection failed.', isError: true })
  })

  it.each([
    ['absent', undefined],
    ['not a number', '2300'],
  ])('states no duration when the duration is %s', (_case, durationMs) => {
    expect(commandCodeResultDivider(turnCompleted({ stopReason: 'end_turn' }, durationMs))).toEqual({ label: 'Turn ended' })
  })

  it('reads the duration from the Worker metadata of the stored turn end', () => {
    const frame = turnCompleted({ stopReason: 'end_turn' })
    const resolved = resolveMessageForRendering({
      wrapper: null,
      topLevel: frame,
      parentObject: frame,
      rawText: '',
      supplementalContent: undefined,
      messageMetadata: { [MESSAGE_METADATA_FIELD.DurationMs]: 2300, [MESSAGE_METADATA_FIELD.ToolUses]: 0 },
    }, AgentProvider.COMMAND_CODE)
    const row = extractedRow(extractChatRow(AgentProvider.COMMAND_CODE, resolved, { kind: 'result_divider' }))
    expect(row?.kind === 'divider' ? row.divider.label : null).toBe('Turn ended (2.3s)')
  })
})
