import type { ChatRowIR, ToolCallRow } from '../../../ir/row'
import { describe, expect, it } from 'vitest'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { piExtractRow } from './row'

const NO_SIDES = { own: undefined, current: undefined, request: undefined, result: undefined, role: 'other' as const }

function toolRow(payload: Record<string, unknown>, completion?: MessageCompletion): ToolCallRow | null {
  const parsed = {
    wrapper: null,
    topLevel: payload,
    parentObject: payload,
    rawText: '',
    supplementalContent: undefined,
    messageMetadata: undefined,
    completion,
  }
  const row: ChatRowIR | null = piExtractRow({
    parsed,
    category: { kind: 'tool_use' },
    sides: NO_SIDES,
    completion,
  } as never)
  return row && row.kind === 'tool' ? row : null
}

describe('pi retained outcome', () => {
  // `piToolCallIR` takes the completion for exactly this: a turn the reader stopped
  // leaves the `tool_execution_start` frame stored, `retainedRowIsFinal` marks the
  // row finished, and without the completion the status resolved to `completed`.
  // The row then drew a green finished command for one the reader cancelled, and
  // every consumer of `call.status` -- the command body's icon, the generic body's
  // failure flag -- read the wrong word.
  it('words an interrupted tool call cancelled rather than completed', () => {
    const row = toolRow({
      type: 'tool_execution_start',
      toolCallId: 'pi-1',
      toolName: 'bash',
      args: { command: 'sleep 30' },
    }, MessageCompletion.INTERRUPTED)
    expect(row?.call.status).toBe('cancelled')
  })

  it('words a turn that ended in an error failed', () => {
    const row = toolRow({
      type: 'tool_execution_start',
      toolCallId: 'pi-2',
      toolName: 'bash',
      args: { command: 'false' },
    }, MessageCompletion.ERROR)
    expect(row?.call.status).toBe('failed')
  })
})
