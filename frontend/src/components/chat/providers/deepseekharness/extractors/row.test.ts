import type { ToolSpanRole } from '~/lib/messageSpan'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { input } from '../../testUtils'
import { classifyDeepseekHarnessMessage } from '../classification'
import { deepseekHarnessExtractRow } from './row'
import '../plugin'

const provider = AgentProvider.DEEPSEEK_HARNESS
const request = {
  type: 'tool/call',
  seq: 1,
  time: 1000,
  data: { callId: 'native-role-call', name: 'bash', arguments: '{"command":"printf native"}' },
}
const result = {
  type: 'tool/result',
  seq: 2,
  time: 1001,
  data: { message: { toolCallId: 'native-role-call', content: [{ type: 'text', text: 'Native bytes.' }] } },
}

function extract(frame: Record<string, unknown>, role: ToolSpanRole) {
  const resolved = input(frame, null, provider)
  return deepseekHarnessExtractRow({
    resolved,
    category: classifyDeepseekHarnessMessage(resolved),
    span: { request: input(request, null, provider), result: undefined, role, visibleRows: { request: false, result: false } },
  })
}

describe('deepseekHarnessExtractRow', () => {
  it.each(['request', 'result'] as const)('refuses an explicit no-side context for a native %s', (side) => {
    const frame = side === 'request' ? request : result
    expect(extract(frame, 'none')).toBeNull()
    expect(extract(frame, side)).toMatchObject({ kind: 'tool', role: side, call: { id: 'native-role-call' } })
  })

  it.each(['request', 'result'] as const)('keeps the unknown-role category fallback for a native %s', (side) => {
    expect(extract(side === 'request' ? request : result, 'other')).toMatchObject({ kind: 'tool', role: side })
  })
})
