import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { renderExtractedRow } from '~/components/chat/rowRenderers'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerRow, providerToolCall } from '~/test-support/toolCallFixture'
import { MUSE_NATIVE_WORKFLOW_COMPLETE_FRAME, MUSE_NATIVE_WORKFLOW_FAILED_FRAME, MUSE_NATIVE_WORKFLOW_LAUNCH_FRAME } from '../toolResults.fixtures'
import '~/components/chat/providers'
import '~/components/chat/providers/testMocks'

interface WorkflowFrame extends Record<string, unknown> {
  params: {
    item: Record<string, unknown>
  }
}

function completedWorkflowFrame(): WorkflowFrame {
  return JSON.parse(MUSE_NATIVE_WORKFLOW_COMPLETE_FRAME)
}

function wrappedReconciliation(value: unknown): string {
  return `<workflow-launch-reconciled>${JSON.stringify(value)}</workflow-launch-reconciled>`
}

describe('museWorkflowResult', () => {
  it('shows the actual computed result from a completed native workflow item', () => {
    const frame = JSON.parse(MUSE_NATIVE_WORKFLOW_COMPLETE_FRAME)
    const row = providerRow(AgentProvider.MUSE_CODE, frame)
    expect(row?.kind).toBe('tool')
    if (row?.kind !== 'tool')
      throw new Error('The native workflow completion requires a result row.')
    expect(row.call.id).toBe(frame.params.item.itemId)
    expect(row.call.kind).toBe('other')
    expect(row.call.status).toBe('completed')
    expect(row.call.result).toEqual({ content: [{ type: 'text', text: '{"marker":"MUSE_WORKFLOW_42"}' }] })
    expect(JSON.stringify(frame)).toBe(MUSE_NATIVE_WORKFLOW_COMPLETE_FRAME)
  })

  it('shows the actual native exception without copying a launch receipt', () => {
    const frame = JSON.parse(MUSE_NATIVE_WORKFLOW_FAILED_FRAME)
    const row = providerRow(AgentProvider.MUSE_CODE, frame)
    expect(row?.kind).toBe('tool')
    if (row?.kind !== 'tool')
      throw new Error('The native workflow exception requires a result row.')
    expect(row.call.status).toBe('failed')
    expect(row.call.result).toEqual({ failure: true, text: 'workflow script promise rejected: Error: MUSE_WORKFLOW_ERROR_42' })
    expect(JSON.stringify(frame)).toBe(MUSE_NATIVE_WORKFLOW_FAILED_FRAME)
  })

  it('keeps the native launch receipt separate from the computed result', () => {
    const frame = JSON.parse(MUSE_NATIVE_WORKFLOW_LAUNCH_FRAME)
    const call = providerToolCall(AgentProvider.MUSE_CODE, frame)
    expect(call).not.toBeNull()
    expect(JSON.stringify(call?.result)).toContain('launched')
    expect(JSON.stringify(call?.result)).not.toContain('MUSE_WORKFLOW_42')
    expect(JSON.stringify(frame)).toBe(MUSE_NATIVE_WORKFLOW_LAUNCH_FRAME)
  })

  it('keeps an unknown native final state without a success or failure', () => {
    const frame = JSON.parse(MUSE_NATIVE_WORKFLOW_COMPLETE_FRAME)
    frame.params.item.status = 'futureFinal'
    const row = providerRow(AgentProvider.MUSE_CODE, frame)
    expect(row?.kind).toBe('tool')
    if (row?.kind !== 'tool')
      throw new Error('The unknown native workflow final state requires a result row.')
    expect(row.call.status).toBe('incomplete')
    expect(row.call.result).toBeUndefined()
  })

  it.each([
    undefined,
    null,
    0,
    false,
    {},
    [],
    '',
    'Native text outside the reconciliation',
    '<workflow-launch-reconciled>{</workflow-launch-reconciled>',
    '<workflow-launch-reconciled>null</workflow-launch-reconciled>',
    '<workflow-launch-reconciled>{"type":"workflow_launch_reconciled"}',
    '<other>{"type":"workflow_launch_reconciled"}</other>',
    '<workflow-launch-reconciled>{"type":"another_type"}</workflow-launch-reconciled>',
  ])('keeps a malformed reconciliation absent without constructing output: %j', (message) => {
    const frame = completedWorkflowFrame()
    frame.params.item.message = message
    const before = JSON.stringify(frame)
    const row = providerRow(AgentProvider.MUSE_CODE, frame)
    expect(row?.kind).toBe('tool')
    if (row?.kind !== 'tool')
      throw new Error('The settled native workflow requires a result row.')
    expect(row.call.status).toBe('incomplete')
    expect(row.call.result).toBeUndefined()
    expect(JSON.stringify(frame)).toBe(before)
  })

  it.each([
    undefined,
    null,
    '',
    '   ',
    0,
    [],
    {},
  ])('rejects an invalid reconciliation call identity: %j', (callId) => {
    const frame = completedWorkflowFrame()
    frame.params.item.message = wrappedReconciliation({
      type: 'workflow_launch_reconciled',
      call_id: callId,
      launch_command_id: 'native-command',
      final_summary: { status: 'completed', summary: 'An unowned summary' },
      latest_failure: null,
    })
    const call = providerToolCall(AgentProvider.MUSE_CODE, frame)
    expect(call?.status).toBe('incomplete')
    expect(call?.result).toBeUndefined()
  })

  it.each([undefined, null, '', '   ', 0, [], {}])('rejects an invalid native launch command identity: %j', (commandId) => {
    const frame = completedWorkflowFrame()
    frame.params.item.message = wrappedReconciliation({
      type: 'workflow_launch_reconciled',
      call_id: 'native-call',
      launch_command_id: commandId,
      final_summary: { status: 'completed', summary: 'An unowned summary' },
      latest_failure: null,
    })
    const call = providerToolCall(AgentProvider.MUSE_CODE, frame)
    expect(call?.status).toBe('incomplete')
    expect(call?.result).toBeUndefined()
  })

  it.each([
    null,
    {},
    { status: 'completed' },
    { status: 'completed', summary: 0 },
    { status: 'completed', summary: null },
    { status: 'failed', summary: 'An inconsistent summary' },
  ])('rejects a malformed or inconsistent computed summary: %j', (summary) => {
    const frame = completedWorkflowFrame()
    frame.params.item.message = wrappedReconciliation({
      type: 'workflow_launch_reconciled',
      call_id: 'native-call',
      launch_command_id: 'native-command',
      final_summary: summary,
      latest_failure: null,
    })
    const call = providerToolCall(AgentProvider.MUSE_CODE, frame)
    expect(call?.status).toBe('incomplete')
    expect(call?.result).toBeUndefined()
  })

  it('keeps an actual empty computed result separate from absent output', () => {
    const frame = completedWorkflowFrame()
    frame.params.item.message = wrappedReconciliation({
      type: 'workflow_launch_reconciled',
      call_id: 'native-call',
      launch_command_id: 'native-command',
      final_summary: { status: 'completed', summary: '' },
      latest_failure: null,
    })
    const call = providerToolCall(AgentProvider.MUSE_CODE, frame)
    expect(call?.status).toBe('completed')
    expect(call?.result).toEqual({ content: [{ type: 'text', text: '' }] })
  })

  it.each([' \n Native text 文 \n ', '0'])('keeps computed output bytes without trimming them: %j', (summary) => {
    const frame = completedWorkflowFrame()
    frame.params.item.message = wrappedReconciliation({
      type: 'workflow_launch_reconciled',
      call_id: 'native-call',
      launch_command_id: 'native-command',
      final_summary: { status: 'completed', summary },
      latest_failure: null,
    })
    const call = providerToolCall(AgentProvider.MUSE_CODE, frame)
    expect(call?.status).toBe('completed')
    expect(call?.result).toEqual({ content: [{ type: 'text', text: summary }] })
  })

  it('keeps active native workflow state on its background-task surface', () => {
    const frame = completedWorkflowFrame()
    frame.params.item.status = 'inProgress'
    expect(providerRow(AgentProvider.MUSE_CODE, frame)).toEqual({ kind: 'hidden' })
  })

  it('keeps retained finality without treating it as a computed native result', () => {
    const frame = completedWorkflowFrame()
    frame.params.item.status = 'inProgress'
    delete frame.params.item.message
    const call = providerToolCall(AgentProvider.MUSE_CODE, frame, { completion: MessageCompletion.FINISHED })
    expect(call?.status).toBe('incomplete')
    expect(call?.result).toBeUndefined()
  })

  it.each([
    ['completed', MUSE_NATIVE_WORKFLOW_COMPLETE_FRAME, 'MUSE_WORKFLOW_42'],
    ['failed', MUSE_NATIVE_WORKFLOW_FAILED_FRAME, 'workflow script promise rejected: Error: MUSE_WORKFLOW_ERROR_42'],
  ])('draws the native %s workflow output through the neutral renderer', (status, native, output) => {
    const row = providerRow(AgentProvider.MUSE_CODE, JSON.parse(native))
    expect(row?.kind).toBe('tool')
    if (row?.kind !== 'tool')
      throw new Error('The native workflow output requires a tool row.')
    expect(row.call.status).toBe(status)
    const { container } = render(() => renderExtractedRow({ kind: 'row', row, completion: null }, undefined))
    expect(container.textContent).toContain(output)
    expect(container.textContent).not.toContain('<workflow-launch-reconciled>')
    expect(container.textContent).not.toContain('resumeFromRunId')
  })
})
