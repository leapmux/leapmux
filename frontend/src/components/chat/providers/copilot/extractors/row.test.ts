import { describe, expect, it } from 'vitest'
import { COPILOT_TOOL } from '~/generated/contracts/copilot-protocol'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { copilotToolStart } from '~/test-support/copilotFixtures'
import { resolveMessageForRendering } from '../../registry'
import { copilotExtractRow } from './row'

describe('copilotExtractRow', () => {
  // A start row reads its own event alone. A subagent launch states its description and
  // its prompt there, so its call states the launch and no report.
  it('draws a start row from its own event, with no result and no finished status', () => {
    const frame = copilotToolStart('native-task', COPILOT_TOOL.Task, { description: 'Probe', prompt: 'Read the code.' })
    const resolved = resolveMessageForRendering({
      rawText: JSON.stringify(frame),
      topLevel: frame,
      parentObject: frame,
      wrapper: null,
    }, AgentProvider.GITHUB_COPILOT)
    const row = copilotExtractRow({
      resolved,
      category: { kind: 'tool_use' },
      span: {
        request: resolved,
        role: 'request',
        visibleRows: { request: false },
      },
    })
    expect(row).toMatchObject({
      kind: 'tool',
      role: 'request',
      call: { kind: 'agent', status: 'unstated' },
    })
    if (row?.kind !== 'tool')
      throw new Error('The Copilot start requires a tool row.')
    expect(row.call.result).toBeUndefined()
    expect(row.call.resultMetadata).toBeUndefined()
  })

  it('closes a retained start span without constructing a native result', () => {
    const frame = copilotToolStart('native-call', 'bash', { command: 'printf native' })
    const resolved = {
      ...resolveMessageForRendering({
        rawText: JSON.stringify(frame),
        topLevel: frame,
        parentObject: frame,
        wrapper: null,
      }, AgentProvider.GITHUB_COPILOT),
      completion: MessageCompletion.FINISHED,
    }
    const row = copilotExtractRow({
      resolved,
      category: { kind: 'tool_result' },
      span: {
        request: resolved,
        role: 'result',
        visibleRows: { request: true },
      },
    })
    expect(row?.kind).toBe('tool')
    if (row?.kind !== 'tool')
      throw new Error('The retained Copilot start requires a tool row.')
    expect(row.role).toBe('result')
    expect(row.call.status).toBe('incomplete')
    expect(row.call.result).toBeUndefined()
  })
})
