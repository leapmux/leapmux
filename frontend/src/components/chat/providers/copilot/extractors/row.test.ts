import { describe, expect, it } from 'vitest'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { copilotToolStart } from '~/test-support/copilotFixtures'
import { resolveMessageForRendering } from '../../registry'
import { copilotExtractRow } from './row'

describe('copilotExtractRow', () => {
  it('closes a retained start span without constructing a native result', () => {
    const frame = copilotToolStart('native-call', 'bash', { command: 'printf native' })
    const resolved = { ...resolveMessageForRendering({ rawText: JSON.stringify(frame), topLevel: frame, parentObject: frame, wrapper: null }, AgentProvider.GITHUB_COPILOT), completion: MessageCompletion.FINISHED }
    const row = copilotExtractRow({
      resolved,
      category: { kind: 'tool_result' },
      span: { request: resolved, result: resolved, role: 'result', visibleRows: { request: true, result: true } },
    })
    expect(row?.kind).toBe('tool')
    if (row?.kind !== 'tool')
      throw new Error('The retained Copilot start requires a tool row.')
    expect(row.role).toBe('result')
    expect(row.call.status).toBe('incomplete')
    expect(row.call.result).toBeUndefined()
  })
})
